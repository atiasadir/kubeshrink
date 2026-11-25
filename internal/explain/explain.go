// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atiasadir/kubeshrink/internal/capacity"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// AutoscalerAuto asks the analyzer to detect Cluster Autoscaler from the
// snapshot; AutoscalerNone means no node autoscaler manages a node.
const (
	AutoscalerAuto    = "auto"
	AutoscalerNone    = "none"
	AutoscalerUnknown = "unknown"
)

// Options tune an analysis.
type Options struct {
	// NodeAutoscaler says what manages nodes without a Karpenter label:
	// AutoscalerAuto (the default) looks for Cluster Autoscaler in the
	// snapshot; CA or AutoscalerNone override that.
	NodeAutoscaler string
}

// Analyzer explains one snapshot. It caches node reports, so cluster and
// namespace views explain each node once.
type Analyzer struct {
	snap   *snapshot.Snapshot
	opts   Options
	att    *capacity.Attribution
	caSeen bool
	nodes  map[string]*NodeReport
}

// NewAnalyzer prepares an analysis of the snapshot.
func NewAnalyzer(s *snapshot.Snapshot, opts Options) *Analyzer {
	return &Analyzer{snap: s, opts: opts, att: capacity.Attribute(s), caSeen: clusterAutoscalerSeen(s), nodes: map[string]*NodeReport{}}
}

// ExplainNode explains one node with default options.
func ExplainNode(s *snapshot.Snapshot, name string) (*NodeReport, error) {
	return NewAnalyzer(s, Options{}).Node(name)
}

// ExplainAllNodes explains every node with default options.
func ExplainAllNodes(s *snapshot.Snapshot) []*NodeReport {
	return NewAnalyzer(s, Options{}).Nodes()
}

// nodeContext is what every node rule receives: the snapshot, the node being
// explained, and the pods that would have to move for it to go away.
type nodeContext struct {
	snap    *snapshot.Snapshot
	node    *snapshot.Node
	movable []*snapshot.Pod
	notes   *[]string
}

func (c *nodeContext) note(msg string) { addNote(c.notes, msg) }

// Node answers "why can't this node be removed?" by running every node rule
// in Catalog against it.
func (a *Analyzer) Node(name string) (*NodeReport, error) {
	if r, ok := a.nodes[name]; ok {
		return r, nil
	}
	s := a.snap
	node := s.Node(name)
	if node == nil {
		return nil, fmt.Errorf("node %q not found (%d node(s) loaded)", name, len(s.Nodes))
	}
	pool, _ := capacity.PoolOf(node)
	r := &NodeReport{
		Node:        name,
		Pool:        pool,
		Allocatable: snapshot.NodeAllocatable(node),
		PodsToMove:  []string{},
		Findings:    []Finding{},
		DataNotes:   append([]string(nil), s.Notes...),
		Limitations: Limitations,
	}
	c := &nodeContext{snap: s, node: node, notes: &r.DataNotes}
	for i := range s.Pods {
		p := &s.Pods[i]
		if p.Spec.NodeName != name {
			continue
		}
		switch {
		case snapshot.IsTerminal(p):
			r.IgnoredPods = append(r.IgnoredPods, IgnoredPod{podRef(p), "finished (phase " + p.Status.Phase + "); nothing to move"})
			continue
		case snapshot.IsMirrorPod(p):
			r.IgnoredPods = append(r.IgnoredPods, IgnoredPod{podRef(p), "static pod managed by the kubelet; not evicted"})
		case snapshot.IsDaemonSetPod(p):
			r.IgnoredPods = append(r.IgnoredPods, IgnoredPod{podRef(p), "DaemonSet pod; autoscalers do not need to reschedule it"})
		default:
			c.movable = append(c.movable, p)
			r.PodsToMove = append(r.PodsToMove, podRef(p))
		}
		r.Requested = r.Requested.Add(snapshot.PodRequests(p))
	}
	for _, rule := range Catalog {
		if rule.nodeCheck != nil {
			r.Findings = append(r.Findings, rule.nodeCheck(c)...)
		}
	}
	a.applyManager(r, node)
	annotateKarpenterEvents(r, len(c.movable))
	r.Findings = finalize(r.Findings, true)
	summarize(r)
	a.nodes[name] = r
	return r, nil
}

// Nodes explains every node, in name order.
func (a *Analyzer) Nodes() []*NodeReport {
	names := make([]string, 0, len(a.snap.Nodes))
	for _, n := range a.snap.Nodes {
		names = append(names, n.Metadata.Name)
	}
	sort.Strings(names)
	out := make([]*NodeReport, 0, len(names))
	for _, n := range names {
		r, _ := a.Node(n) // cannot fail: the name came from the snapshot
		out = append(out, r)
	}
	return out
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

const (
	labelKarpenterNodePool    = "karpenter.sh/nodepool"
	labelKarpenterProvisioner = "karpenter.sh/provisioner-name"
)

// karpenterPool returns the NodePool (or, on old Karpenter versions, the
// Provisioner) that launched the node, or "" if Karpenter does not manage it.
func karpenterPool(n *snapshot.Node) string {
	if v := n.Metadata.Labels[labelKarpenterNodePool]; v != "" {
		return v
	}
	return n.Metadata.Labels[labelKarpenterProvisioner]
}

// managerOf decides which node autoscaler, if any, manages the node.
func (a *Analyzer) managerOf(n *snapshot.Node) string {
	if karpenterPool(n) != "" {
		return Karpenter
	}
	switch a.opts.NodeAutoscaler {
	case CA, AutoscalerNone:
		return a.opts.NodeAutoscaler
	}
	if a.caSeen {
		return CA
	}
	return AutoscalerUnknown
}

// noAutoscalerSeenNote explains why findings for both autoscalers are kept
// when the snapshot shows neither.
const noAutoscalerSeenNote = "No node autoscaler was found in the snapshot (no Karpenter labels on nodes, no Cluster Autoscaler pods or events). Findings for both are kept, because managed offerings run Cluster Autoscaler outside the cluster. If no autoscaler runs, nothing removes nodes automatically: rerun with --node-autoscaler none to drop autoscaler-specific findings."

// noAutoscalerNote is the cluster-level consequence of --node-autoscaler none.
const noAutoscalerNote = "No node autoscaler manages these nodes (--node-autoscaler none), so nothing removes them automatically: capacity only shrinks when someone deletes nodes or resizes node groups."

// applyManager records who manages the node and drops findings that only
// matter to an autoscaler that does not manage it, with a note saying so.
func (a *Analyzer) applyManager(r *NodeReport, n *snapshot.Node) {
	r.ManagedBy = a.managerOf(n)
	switch r.ManagedBy {
	case AutoscalerUnknown:
		addNote(&r.DataNotes, noAutoscalerSeenNote)
		return
	case AutoscalerNone:
		addNote(&r.DataNotes, noAutoscalerNote)
	}
	kept := r.Findings[:0]
	omitted := 0
	for _, f := range r.Findings {
		if len(f.AppliesTo) == 1 && f.AppliesTo[0] != r.ManagedBy {
			omitted++
			continue
		}
		kept = append(kept, f)
	}
	r.Findings = kept
	if omitted == 0 {
		return
	}
	switch r.ManagedBy {
	case Karpenter:
		addNote(&r.DataNotes, fmt.Sprintf("%s %s is managed by Karpenter (NodePool %s); %d finding(s) that only apply to Cluster Autoscaler were omitted.", nodeNotePrefix, r.Node, karpenterPool(n), omitted))
	case CA:
		addNote(&r.DataNotes, fmt.Sprintf("%s %s is managed by Cluster Autoscaler; %d finding(s) that only apply to Karpenter were omitted.", nodeNotePrefix, r.Node, omitted))
	default:
		addNote(&r.DataNotes, fmt.Sprintf("%s %s has no node autoscaler (--node-autoscaler none); %d finding(s) that only apply to Cluster Autoscaler or Karpenter were omitted.", nodeNotePrefix, r.Node, omitted))
	}
}

// nodeNotePrefix starts per-node notes, so cluster-wide views can summarize them.
const nodeNotePrefix = "Node"

func isPerNodeNote(note string) bool { return strings.HasPrefix(note, nodeNotePrefix+" ") }

// clusterAutoscalerSeen reports whether the snapshot shows Cluster Autoscaler
// running in the cluster: its workload, its pods or its events.
func clusterAutoscalerSeen(s *snapshot.Snapshot) bool {
	for i := range s.Workloads {
		if strings.Contains(s.Workloads[i].Metadata.Name, "cluster-autoscaler") {
			return true
		}
	}
	for i := range s.Pods {
		if strings.Contains(s.Pods[i].Metadata.Name, "cluster-autoscaler") {
			return true
		}
	}
	for i := range s.Events {
		if autoscalerOf(&s.Events[i]) == CA {
			return true
		}
	}
	return false
}
