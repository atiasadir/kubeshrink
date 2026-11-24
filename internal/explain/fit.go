// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"sort"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// candidate is another node that could receive evicted pods, with the
// requests it still has free.
type candidate struct {
	node *snapshot.Node
	free snapshot.Resources
}

type rejection struct {
	node   string
	reason string
}

// checkRoomElsewhere asks: if this node disappeared, would each of its pods
// fit on some other node? It is a first-fit estimate by requests, largest pod
// first, and reserves capacity as it places pods so two pods cannot claim the
// same free space.
func checkRoomElsewhere(c *nodeContext) []Finding {
	if len(c.movable) == 0 {
		return nil
	}
	var cands []*candidate
	for i := range c.snap.Nodes {
		n := &c.snap.Nodes[i]
		if n.Metadata.Name == c.node.Metadata.Name || n.Spec.Unschedulable {
			continue
		}
		free := snapshot.NodeAllocatable(n)
		for _, p := range c.snap.PodsOnNode(n.Metadata.Name) {
			free = free.Sub(snapshot.PodRequests(p))
		}
		cands = append(cands, &candidate{node: n, free: free})
	}
	if len(cands) == 0 {
		return []Finding{noOtherNodeFinding(c)}
	}
	pods := append([]*snapshot.Pod(nil), c.movable...)
	sort.SliceStable(pods, func(i, j int) bool {
		a, b := snapshot.PodRequests(pods[i]), snapshot.PodRequests(pods[j])
		if a.MilliCPU != b.MilliCPU {
			return a.MilliCPU > b.MilliCPU
		}
		if a.MemoryBytes != b.MemoryBytes {
			return a.MemoryBytes > b.MemoryBytes
		}
		return podRef(pods[i]) < podRef(pods[j])
	})
	var out []Finding
	for _, p := range pods {
		req := snapshot.PodRequests(p)
		var rejs []rejection
		placed := false
		for _, cand := range cands {
			if reason := whyNotFit(p, req, cand); reason != "" {
				rejs = append(rejs, rejection{cand.node.Metadata.Name, reason})
				continue
			}
			cand.free = cand.free.Sub(req)
			placed = true
			break
		}
		if !placed {
			out = append(out, noRoomFinding(p, req, rejs, len(cands)))
		}
	}
	return out
}

// whyNotFit returns "" when the pod fits on the candidate, else the first reason it does not.
func whyNotFit(p *snapshot.Pod, req snapshot.Resources, c *candidate) string {
	for _, k := range sortedKeys(p.Spec.NodeSelector) {
		if lv, ok := c.node.Metadata.Labels[k]; !ok || lv != p.Spec.NodeSelector[k] {
			return fmt.Sprintf("node selector %s=%s not matched", k, p.Spec.NodeSelector[k])
		}
	}
	for _, t := range c.node.Spec.Taints {
		if t.Effect == "PreferNoSchedule" {
			continue
		}
		if !tolerates(p.Spec.Tolerations, t) {
			return fmt.Sprintf("taint %s=%s:%s not tolerated", t.Key, t.Value, t.Effect)
		}
	}
	if req.MilliCPU > c.free.MilliCPU {
		return fmt.Sprintf("not enough cpu (needs %s, free %s)", snapshot.FormatCPU(req.MilliCPU), snapshot.FormatCPU(c.free.MilliCPU))
	}
	if req.MemoryBytes > c.free.MemoryBytes {
		return fmt.Sprintf("not enough memory (needs %s, free %s)", snapshot.FormatMemory(req.MemoryBytes), snapshot.FormatMemory(c.free.MemoryBytes))
	}
	if c.free.Pods < 1 {
		return "no pod slots left"
	}
	return ""
}

func tolerates(tols []snapshot.Toleration, t snapshot.Taint) bool {
	for _, tol := range tols {
		if tol.Effect != "" && tol.Effect != t.Effect {
			continue
		}
		if tol.Key == "" && tol.Operator == "Exists" {
			return true
		}
		if tol.Key != t.Key {
			continue
		}
		if tol.Operator == "Exists" || tol.Value == t.Value {
			return true
		}
	}
	return false
}

const maxRejectionsShown = 5

// noOtherNodeFinding reports once per node, rather than once per pod, when
// there is no other schedulable node at all (e.g. a single-node cluster).
func noOtherNodeFinding(c *nodeContext) Finding {
	ref := nodeRef(c.node)
	ev := []Evidence{{Object: "cluster", Field: "other schedulable nodes", Value: "0"}}
	for _, p := range c.movable {
		ev = append(ev, Evidence{Object: podRef(p), Field: "would need to move", Value: snapshot.PodRequests(p).String()})
	}
	return Finding{
		Rule: "no-room-elsewhere", Severity: Blocker, Subject: ref,
		AppliesTo: bothAutoscalers, Confidence: High,
		Claim:    fmt.Sprintf("There is no other schedulable node, so the %d pod(s) that would need to move have nowhere to go. Autoscalers keep the node (or must first add a replacement).", len(c.movable)),
		Evidence: ev,
		Alternatives: []string{
			"Other nodes may exist but be cordoned or missing from the snapshot.",
			"Karpenter may replace this node with a cheaper one instead of deleting it.",
		},
	}
}

func noRoomFinding(p *snapshot.Pod, req snapshot.Resources, rejs []rejection, candidates int) Finding {
	ref := podRef(p)
	ev := []Evidence{{Object: ref, Field: "sum of container requests", Value: req.String()}}
	if len(p.Spec.NodeSelector) > 0 {
		ev = append(ev, Evidence{Object: ref, Field: ".spec.nodeSelector", Value: fmtLabels(p.Spec.NodeSelector)})
	}
	for i, r := range rejs {
		if i == maxRejectionsShown {
			ev = append(ev, Evidence{Object: fmt.Sprintf("%d more node(s)", len(rejs)-maxRejectionsShown), Field: "fit check", Value: "also rejected"})
			break
		}
		ev = append(ev, Evidence{Object: snapshot.Ref("Node", "", r.node), Field: "fit check", Value: r.reason})
	}
	claim := fmt.Sprintf("None of the %d other schedulable node(s) in the snapshot can take this pod (checked node selector, taints, and CPU, memory and pod-count requests). Draining this node would leave the pod Pending, so autoscalers keep the node.", candidates)
	unknowns := []string{
		"Placement is a first-fit estimate by requests; a different packing might succeed.",
		"Pod affinity, topology spread, host ports and volume zones were not evaluated.",
	}
	if snapshot.HasValue(p.Spec.Affinity) {
		unknowns = append(unknowns, "This pod declares affinity rules, which can only narrow the nodes it may use.")
	}
	if snapshot.HasValue(p.Spec.TopologySpreadConstraints) {
		unknowns = append(unknowns, "This pod declares topology spread constraints, which were not evaluated.")
	}
	return Finding{
		Rule: "no-room-elsewhere", Severity: Blocker, Subject: ref,
		AppliesTo: bothAutoscalers, Confidence: Medium,
		Claim:    claim,
		Evidence: ev,
		Alternatives: []string{
			"Karpenter may replace this node with a cheaper one instead of deleting it; this rule only checks moving pods onto existing nodes.",
			"Requests may be larger than real usage; right-sizing (for example with VPA recommendations) could create room.",
		},
		Unknowns: unknowns,
	}
}
