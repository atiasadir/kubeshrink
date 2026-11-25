// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// candidate is one other node in the simulated cluster: what it still has
// free and which pods it would run.
type candidate struct {
	node *snapshot.Node
	free snapshot.Resources
	pods []*snapshot.Pod
}

func (c *candidate) domain(key string) (string, bool) {
	v, ok := c.node.Metadata.Labels[key]
	return v, ok
}

type placedPod struct {
	pod *snapshot.Pod
	on  *candidate
}

// simulation is the cluster without the node being drained. Cordoned nodes
// stay in it: they take no new pods, but their pods still count for pod
// affinity and topology spread.
type simulation struct {
	nodes []*candidate
	anti  []placedPod // pods with required anti-affinity, which repel newcomers
}

func newSimulation(s *snapshot.Snapshot, drained string) *simulation {
	sim := &simulation{}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if n.Metadata.Name == drained {
			continue
		}
		c := &candidate{node: n, free: snapshot.NodeAllocatable(n)}
		sim.nodes = append(sim.nodes, c)
		for _, p := range s.PodsOnNode(n.Metadata.Name) {
			sim.place(p, snapshot.PodRequests(p), c)
		}
	}
	return sim
}

func (sim *simulation) place(p *snapshot.Pod, req snapshot.Resources, c *candidate) {
	c.free = c.free.Sub(req)
	c.pods = append(c.pods, p)
	if len(snapshot.RequiredPodAntiAffinity(p)) > 0 {
		sim.anti = append(sim.anti, placedPod{p, c})
	}
}

// inDomain returns the pods on every node whose label key has the value.
func (sim *simulation) inDomain(key, value string) []*snapshot.Pod {
	var out []*snapshot.Pod
	for _, c := range sim.nodes {
		if v, ok := c.domain(key); ok && v == value {
			out = append(out, c.pods...)
		}
	}
	return out
}

type rejection struct {
	node, kind, reason string
}

// checkRoomElsewhere asks: if this node disappeared, would each of its pods
// fit on some other node? Pods are placed largest first on the first node
// that passes every check, and each placement reserves its requests, so two
// pods cannot claim the same free space.
func checkRoomElsewhere(c *nodeContext) []Finding {
	if len(c.movable) == 0 {
		return nil
	}
	sim := newSimulation(c.snap, c.node.Metadata.Name)
	schedulable := 0
	for _, cand := range sim.nodes {
		if !cand.node.Spec.Unschedulable {
			schedulable++
		}
	}
	if schedulable == 0 {
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
		for _, cand := range sim.nodes {
			if cand.node.Spec.Unschedulable {
				continue
			}
			if kind, reason := sim.whyNotFit(p, req, cand); kind != "" {
				rejs = append(rejs, rejection{cand.node.Metadata.Name, kind, reason})
				continue
			}
			sim.place(p, req, cand)
			placed = true
			break
		}
		if !placed {
			out = append(out, noRoomFinding(p, req, rejs, schedulable))
		}
	}
	return out
}

// whyNotFit returns the first reason the pod cannot run on the candidate, as
// a short kind and a sentence, or empty strings when it fits.
func (sim *simulation) whyNotFit(p *snapshot.Pod, req snapshot.Resources, c *candidate) (kind, reason string) {
	for _, k := range sortedKeys(p.Spec.NodeSelector) {
		if lv, ok := c.node.Metadata.Labels[k]; !ok || lv != p.Spec.NodeSelector[k] {
			return "node selector", fmt.Sprintf("node selector %s=%s not matched", k, p.Spec.NodeSelector[k])
		}
	}
	if ns := snapshot.RequiredNodeAffinity(p); ns != nil && !ns.Matches(c.node) {
		return "node affinity", "required node affinity not matched"
	}
	for _, t := range c.node.Spec.Taints {
		if t.Restricts() && !p.Tolerates(t) {
			return "taint", fmt.Sprintf("taint %s not tolerated", t)
		}
	}
	if req.MilliCPU > c.free.MilliCPU {
		return "cpu", fmt.Sprintf("not enough cpu (needs %s, free %s)", snapshot.FormatCPU(req.MilliCPU), snapshot.FormatCPU(c.free.MilliCPU))
	}
	if req.MemoryBytes > c.free.MemoryBytes {
		return "memory", fmt.Sprintf("not enough memory (needs %s, free %s)", snapshot.FormatMemory(req.MemoryBytes), snapshot.FormatMemory(c.free.MemoryBytes))
	}
	if c.free.Pods < 1 {
		return "pod slots", "no pod slots left"
	}
	if port := hostPortConflict(p, c.pods); port != "" {
		return "host port", "host port " + port + " already in use"
	}
	if r := sim.antiAffinityConflict(p, c); r != "" {
		return "pod anti-affinity", r
	}
	if r := sim.affinityUnmet(p, c); r != "" {
		return "pod affinity", r
	}
	if r := sim.spreadViolation(p, c); r != "" {
		return "topology spread", r
	}
	return "", ""
}

func hostPorts(p *snapshot.Pod) []string {
	var out []string
	ctrs := append([]snapshot.Container(nil), p.Spec.Containers...)
	for _, ic := range p.Spec.InitContainers {
		if ic.IsSidecar() {
			ctrs = append(ctrs, ic)
		}
	}
	for _, ctr := range ctrs {
		for _, port := range ctr.Ports {
			if port.HostPort == 0 {
				continue
			}
			proto := port.Protocol
			if proto == "" {
				proto = "TCP"
			}
			out = append(out, fmt.Sprintf("%d/%s", port.HostPort, proto))
		}
	}
	return out
}

func hostPortConflict(p *snapshot.Pod, others []*snapshot.Pod) string {
	want := hostPorts(p)
	if len(want) == 0 {
		return ""
	}
	for _, o := range others {
		for _, used := range hostPorts(o) {
			for _, w := range want {
				if w == used {
					return w
				}
			}
		}
	}
	return ""
}

// antiAffinityConflict checks required pod anti-affinity both ways: the
// incoming pod's own terms, and the terms of pods already in the domain.
func (sim *simulation) antiAffinityConflict(p *snapshot.Pod, c *candidate) string {
	for _, t := range snapshot.RequiredPodAntiAffinity(p) {
		v, ok := c.domain(t.TopologyKey)
		if !ok {
			continue
		}
		for _, o := range sim.inDomain(t.TopologyKey, v) {
			if t.SelectsPod(p, o) {
				return fmt.Sprintf("pod anti-affinity on %s: %s already runs there", t.TopologyKey, podRef(o))
			}
		}
	}
	for _, a := range sim.anti {
		for _, t := range snapshot.RequiredPodAntiAffinity(a.pod) {
			if !t.SelectsPod(a.pod, p) {
				continue
			}
			v, ok := c.domain(t.TopologyKey)
			if ov, ook := a.on.domain(t.TopologyKey); ok && ook && v == ov {
				return fmt.Sprintf("%s there has pod anti-affinity on %s against this pod", podRef(a.pod), t.TopologyKey)
			}
		}
	}
	return ""
}

// affinityUnmet checks required pod affinity: some selected pod must already
// run in the candidate's domain, unless no pod matches anywhere and the pod
// matches its own term (the first pod of a group may start anywhere).
func (sim *simulation) affinityUnmet(p *snapshot.Pod, c *candidate) string {
	for _, t := range snapshot.RequiredPodAffinity(p) {
		v, ok := c.domain(t.TopologyKey)
		if !ok {
			return fmt.Sprintf("pod affinity needs node label %s, which this node lacks", t.TopologyKey)
		}
		if anySelected(p, t, sim.inDomain(t.TopologyKey, v)) {
			continue
		}
		var all []*snapshot.Pod
		for _, n := range sim.nodes {
			all = append(all, n.pods...)
		}
		if !anySelected(p, t, all) && t.SelectsPod(p, p) {
			continue
		}
		return fmt.Sprintf("pod affinity on %s: no matching pod runs in this node's domain", t.TopologyKey)
	}
	return ""
}

func anySelected(owner *snapshot.Pod, t snapshot.PodAffinityTerm, pods []*snapshot.Pod) bool {
	for _, o := range pods {
		if t.SelectsPod(owner, o) {
			return true
		}
	}
	return false
}

// spreadViolation checks enforced topology spread constraints the way the
// scheduler does: count matching pods per domain over the nodes that pass
// the pod's node selection, then compare the skew with maxSkew.
func (sim *simulation) spreadViolation(p *snapshot.Pod, c *candidate) string {
	for _, tsc := range p.Spec.TopologySpreadConstraints {
		if !tsc.Enforced() {
			continue
		}
		v, ok := c.domain(tsc.TopologyKey)
		if !ok {
			return fmt.Sprintf("topology spread needs node label %s, which this node lacks", tsc.TopologyKey)
		}
		counts := map[string]int{v: 0}
		for _, n := range sim.nodes {
			dv, ok := n.domain(tsc.TopologyKey)
			if !ok || !snapshot.MatchesNodeSelection(p, n.node) {
				continue
			}
			counts[dv] += 0
			for _, o := range n.pods {
				if o.Metadata.Namespace == p.Metadata.Namespace && tsc.LabelSelector.Matches(o.Metadata.Labels) {
					counts[dv]++
				}
			}
		}
		low := -1
		for _, n := range counts {
			if low < 0 || n < low {
				low = n
			}
		}
		if tsc.MinDomains != nil && len(counts) < int(*tsc.MinDomains) {
			low = 0
		}
		self := 0
		if tsc.LabelSelector.Matches(p.Metadata.Labels) {
			self = 1
		}
		if skew := counts[v] + self - low; skew > int(tsc.MaxSkew) {
			return fmt.Sprintf("topology spread on %s: placing it here makes the skew %d, above maxSkew %d", tsc.TopologyKey, skew, tsc.MaxSkew)
		}
	}
	return ""
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
		Meaning:  "Nothing else can run these pods today.",
		Evidence: ev,
		Alternatives: []string{
			"Other nodes may exist but be cordoned or missing from the snapshot.",
			"Karpenter may replace this node with a cheaper one instead of deleting it.",
		},
	}
}

func noRoomFinding(p *snapshot.Pod, req snapshot.Resources, rejs []rejection, candidates int) Finding {
	ref := podRef(p)
	ev := []Evidence{{Object: ref, Field: "requests (containers, init containers, overhead)", Value: req.String()}}
	if len(p.Spec.NodeSelector) > 0 {
		ev = append(ev, Evidence{Object: ref, Field: ".spec.nodeSelector", Value: fmtLabels(p.Spec.NodeSelector)})
	}
	kinds := map[string]int{}
	for i, r := range rejs {
		kinds[r.kind]++
		if i < maxRejectionsShown {
			ev = append(ev, Evidence{Object: snapshot.Ref("Node", "", r.node), Field: "fit check", Value: r.reason})
		}
	}
	if len(rejs) > maxRejectionsShown {
		ev = append(ev, Evidence{Object: fmt.Sprintf("%d more node(s)", len(rejs)-maxRejectionsShown), Field: "fit check", Value: "also rejected"})
	}
	return Finding{
		Rule: "no-room-elsewhere", Severity: Blocker, Subject: ref,
		AppliesTo: bothAutoscalers, Confidence: Medium,
		Claim:    fmt.Sprintf("None of the %d other schedulable node(s) in the snapshot can take this pod. Draining this node would leave it Pending, so autoscalers keep the node.", candidates),
		Meaning:  "The other nodes say no because of: " + kindSummary(kinds) + ".",
		Evidence: ev,
		Alternatives: []string{
			"Karpenter may replace this node with a cheaper one instead of deleting it; this rule only checks moving pods onto existing nodes.",
			"Requests may be larger than real usage; right-sizing (for example with VPA recommendations) could create room.",
		},
		Unknowns: []string{
			"Placement is a first-fit estimate by requests, largest pod first; a different packing might succeed.",
			"Volume zones, pod priority and preemption were not evaluated.",
		},
	}
}

// kindSummary renders counts such as "pod anti-affinity on 2 node(s), taint on 1 node(s)".
func kindSummary(kinds map[string]int) string {
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if kinds[keys[i]] != kinds[keys[j]] {
			return kinds[keys[i]] > kinds[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s on %d node(s)", k, kinds[k]))
	}
	return strings.Join(parts, ", ")
}
