// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"sort"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// nodeContext is what every rule receives: the snapshot, the node being
// explained, and the pods that would have to be evicted for it to go away.
type nodeContext struct {
	snap    *snapshot.Snapshot
	node    *snapshot.Node
	movable []*snapshot.Pod
}

// ExplainNode answers "why can't this node be removed?" by running every rule
// in Catalog against the node and collecting their findings.
func ExplainNode(s *snapshot.Snapshot, name string) (*NodeReport, error) {
	node := s.Node(name)
	if node == nil {
		return nil, fmt.Errorf("node %q not found (%d node(s) loaded)", name, len(s.Nodes))
	}
	c := &nodeContext{snap: s, node: node}
	r := &NodeReport{
		Node:        name,
		PodsToMove:  []string{},
		Findings:    []Finding{},
		DataNotes:   append([]string(nil), s.Notes...),
		Limitations: Limitations,
	}
	for i := range s.Pods {
		p := &s.Pods[i]
		if p.Spec.NodeName != name {
			continue
		}
		switch {
		case snapshot.IsTerminal(p):
			r.IgnoredPods = append(r.IgnoredPods, IgnoredPod{podRef(p), "finished (phase " + p.Status.Phase + "); nothing to move"})
		case isMirrorPod(p):
			r.IgnoredPods = append(r.IgnoredPods, IgnoredPod{podRef(p), "static pod managed by the kubelet; not evicted"})
		case isDaemonSetPod(p):
			r.IgnoredPods = append(r.IgnoredPods, IgnoredPod{podRef(p), "DaemonSet pod; autoscalers do not need to reschedule it"})
		default:
			c.movable = append(c.movable, p)
			r.PodsToMove = append(r.PodsToMove, podRef(p))
		}
	}
	for _, rule := range Catalog {
		if rule.check != nil {
			r.Findings = append(r.Findings, rule.check(c)...)
		}
	}
	applyNodeManager(r, node)
	sortFindings(r.Findings)
	summarize(r)
	return r, nil
}

// ExplainAllNodes explains every node in the snapshot, in name order.
func ExplainAllNodes(s *snapshot.Snapshot) []*NodeReport {
	names := make([]string, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		names = append(names, n.Metadata.Name)
	}
	sort.Strings(names)
	out := make([]*NodeReport, 0, len(names))
	for _, n := range names {
		r, _ := ExplainNode(s, n) // cannot fail: the name came from the snapshot
		out = append(out, r)
	}
	return out
}

var severityOrder = map[Severity]int{Blocker: 0, PossibleBlocker: 1, Warning: 2, Context: 3}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if severityOrder[a.Severity] != severityOrder[b.Severity] {
			return severityOrder[a.Severity] < severityOrder[b.Severity]
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Rule < b.Rule
	})
}

// Counts returns the number of findings per severity.
func (r *NodeReport) Counts() map[Severity]int {
	counts := map[Severity]int{}
	for _, f := range r.Findings {
		counts[f.Severity]++
	}
	return counts
}

func summarize(r *NodeReport) {
	counts := r.Counts()
	switch {
	case counts[Blocker] > 0:
		r.Verdict = VerdictBlocked
	case counts[PossibleBlocker] > 0:
		r.Verdict = VerdictPossiblyBlocked
	default:
		r.Verdict = VerdictNoBlockerFound
	}
	r.Summary = fmt.Sprintf("%d blocker(s), %d possible blocker(s), %d warning(s), %d context finding(s); %d pod(s) would need to move.",
		counts[Blocker], counts[PossibleBlocker], counts[Warning], counts[Context], len(r.PodsToMove))
	if r.Verdict == VerdictNoBlockerFound {
		r.Summary += " No rule found a reason this node must stay. That is not proof it is removable; see the limitations."
	}
}

const labelKarpenterNodePool = "karpenter.sh/nodepool"

// applyNodeManager records which autoscaler manages the node when its labels
// say so, and drops findings that only matter to the other autoscaler. The
// omission is reported so nothing disappears silently.
func applyNodeManager(r *NodeReport, n *snapshot.Node) {
	pool, ok := n.Metadata.Labels[labelKarpenterNodePool]
	if !ok {
		return
	}
	r.ManagedBy = Karpenter
	kept := r.Findings[:0]
	omitted := 0
	for _, f := range r.Findings {
		if len(f.AppliesTo) == 1 && f.AppliesTo[0] == CA {
			omitted++
			continue
		}
		kept = append(kept, f)
	}
	r.Findings = kept
	if omitted > 0 {
		r.DataNotes = append(r.DataNotes, fmt.Sprintf("Node is managed by Karpenter (label %s=%s); %d Cluster Autoscaler-only finding(s) were omitted.", labelKarpenterNodePool, pool, omitted))
	}
}

func isDaemonSetPod(p *snapshot.Pod) bool {
	cr := p.Metadata.ControllerRef()
	return cr != nil && cr.Kind == "DaemonSet"
}

func isMirrorPod(p *snapshot.Pod) bool {
	if _, ok := p.Metadata.Annotations[annMirrorPod]; ok {
		return true
	}
	cr := p.Metadata.ControllerRef()
	return cr != nil && cr.Kind == "Node"
}
