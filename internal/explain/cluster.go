// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/atiasadir/kubeshrink/internal/capacity"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// ClusterReport answers "why does this cluster run more nodes than its
// requests need?".
type ClusterReport struct {
	Summary       string               `json:"summary"`
	TakenAt       string               `json:"takenAt,omitempty"`
	ServerVersion string               `json:"serverVersion,omitempty"`
	Nodes         int                  `json:"nodes"`
	NodesNeeded   int                  `json:"nodesNeeded"`
	Allocatable   capacity.Amount      `json:"allocatable"`
	Requested     capacity.Amount      `json:"requested"`
	PackingCPU    float64              `json:"packingCPU"`
	PackingMemory float64              `json:"packingMemory"`
	Pools         []capacity.PoolUsage `json:"pools"`
	NodePools     []NodePoolSettings   `json:"nodePools,omitempty"`
	ManagedBy     map[string]int       `json:"managedBy"`
	Verdicts      map[string]int       `json:"verdicts"`
	TopReasons    []Reason             `json:"topReasons"`
	ReplicaFloors []FloorGroup         `json:"replicaFloors"`
	Size          SizeSummary          `json:"size"`
	Namespaces    []NamespaceLine      `json:"namespaces"`
	// OmittedNamespaces counts namespaces left out by Truncate.
	OmittedNamespaces int      `json:"omittedNamespaces,omitempty"`
	DataNotes         []string `json:"dataNotes,omitempty"`
	Limitations       []string `json:"limitations"`
}

// SizeSummary compares requests with VPA recommendations across the cluster.
type SizeSummary struct {
	VPA VPACoverage `json:"vpaCoverage"`
	// AboveTargets is how much running pods request above their VPA targets.
	AboveTargets capacity.Amount `json:"aboveTargets"`
	// ContainersWithTargets counts running containers that have a VPA target.
	ContainersWithTargets int `json:"containersWithTargets"`
	// NodesNeededAtTargets estimates how many nodes the pods would fit on if
	// every container with a VPA target requested it, keeping actual requests
	// elsewhere. Request-based like NodesNeeded; lower bounds are never used.
	NodesNeededAtTargets int `json:"nodesNeededAtTargets"`
}

// VPACoverage counts long-running workloads by what their VPA can say.
type VPACoverage struct {
	Workloads          int `json:"workloads"`
	WithVPA            int `json:"withVPA"`
	WithRecommendation int `json:"withRecommendation"`
	Oversized          int `json:"oversized"`
}

func coverage(sizes []WorkloadSize, ns string) VPACoverage {
	var c VPACoverage
	for _, w := range sizes {
		if ns != "" && w.Namespace != ns {
			continue
		}
		c.Workloads++
		if w.HasVPA {
			c.WithVPA++
		}
		if w.HasRecommendation {
			c.WithRecommendation++
		}
		if w.Oversized {
			c.Oversized++
		}
	}
	return c
}

// NodePoolSettings is one Karpenter NodePool and its disruption settings.
type NodePoolSettings struct {
	Name     string `json:"name"`
	Nodes    int    `json:"nodes"`
	Settings string `json:"settings"`
}

// Reason is one rule that keeps nodes, with the nodes it keeps.
type Reason struct {
	Rule     string   `json:"rule"`
	Layer    Layer    `json:"layer"`
	Title    string   `json:"title"`
	Severity Severity `json:"severity"`
	Nodes    []string `json:"nodes"`
	Example  string   `json:"example"`
}

// Pinned is a workload held at its autoscaler minimum, or at the replica
// count of a paused KEDA ScaledObject.
type Pinned struct {
	Workload   string             `json:"workload"`
	Namespace  string             `json:"namespace"`
	Controller string             `json:"controller"` // keda, hpa or keda-paused
	Replicas   int32              `json:"replicas"`
	Min        int32              `json:"min"`
	Pods       int                `json:"pods"`
	Requested  snapshot.Resources `json:"requested"`
}

// FloorGroup counts pinned workloads that share a controller and a minimum,
// e.g. "22 KEDA workloads pinned at 2".
type FloorGroup struct {
	Controller string             `json:"controller"`
	Min        int32              `json:"min"`
	Workloads  int                `json:"workloads"`
	Pods       int                `json:"pods"`
	Requested  snapshot.Resources `json:"requested"`
}

// NamespaceLine is one namespace in the cluster view.
type NamespaceLine struct {
	Namespace     string          `json:"namespace"`
	Pods          int             `json:"pods"`
	Nodes         int             `json:"nodes"`
	Requested     capacity.Amount `json:"requested"`
	IdleShare     capacity.Amount `json:"idleShare"`
	PackingCPU    float64         `json:"packingCPU"`
	PackingMemory float64         `json:"packingMemory"`
	Pinned        int             `json:"pinnedWorkloads"`
}

// ExplainCluster explains the whole snapshot with default options.
func ExplainCluster(s *snapshot.Snapshot) *ClusterReport { return NewAnalyzer(s, Options{}).Cluster() }

// Cluster answers "why does this cluster run more nodes than its requests
// need?": how full the nodes are on paper, how few nodes the requests would
// fit on, which rules keep the nodes, which workloads sit at a floor, and
// which namespaces hold the most idle capacity.
func (a *Analyzer) Cluster() *ClusterReport {
	s := a.snap
	alloc, req := a.att.Totals()
	pinned := a.Pinned()
	r := &ClusterReport{
		TakenAt: s.TakenAt, ServerVersion: s.ServerVersion,
		Nodes: len(s.Nodes), Allocatable: alloc, Requested: req,
		Pools: a.att.Pools(), ManagedBy: map[string]int{}, Verdicts: map[string]int{},
		TopReasons: []Reason{}, ReplicaFloors: groupFloors(pinned), Namespaces: []NamespaceLine{},
		Limitations: Limitations,
	}
	r.PackingCPU, r.PackingMemory = capacity.Ratio(req, alloc)
	for _, p := range r.Pools {
		r.NodesNeeded += p.MinNodes
	}
	mod, changed, above := atVPATargets(s)
	r.Size = SizeSummary{VPA: coverage(a.Sizes(), ""), AboveTargets: above, ContainersWithTargets: changed, NodesNeededAtTargets: r.NodesNeeded}
	if changed > 0 {
		r.Size.NodesNeededAtTargets = 0
		for _, p := range capacity.Attribute(mod).Pools() {
			r.Size.NodesNeededAtTargets += p.MinNodes
		}
	}
	reasons := map[string]*Reason{}
	perNode := map[string]int{}
	for _, nr := range a.Nodes() {
		r.ManagedBy[nr.ManagedBy]++
		r.Verdicts[nr.Verdict]++
		for _, n := range nr.DataNotes {
			if isPerNodeNote(n) {
				perNode[nr.ManagedBy]++
				continue
			}
			addNote(&r.DataNotes, n)
		}
		for _, f := range nr.Findings {
			if f.Severity != Blocker && f.Severity != PossibleBlocker {
				continue
			}
			rs := reasons[f.Rule]
			if rs == nil {
				ex := f.Meaning
				if ex == "" {
					ex = f.Claim
				}
				rs = &Reason{Rule: f.Rule, Layer: f.Layer, Title: ruleTitle(f.Rule), Severity: f.Severity, Example: ex}
				reasons[f.Rule] = rs
			}
			if severityOrder[f.Severity] < severityOrder[rs.Severity] {
				rs.Severity = f.Severity
			}
			if n := len(rs.Nodes); n == 0 || rs.Nodes[n-1] != nr.Node {
				rs.Nodes = append(rs.Nodes, nr.Node)
			}
		}
	}
	for _, rs := range reasons {
		r.TopReasons = append(r.TopReasons, *rs)
	}
	sort.Slice(r.TopReasons, func(i, j int) bool {
		a, b := r.TopReasons[i], r.TopReasons[j]
		if len(a.Nodes) != len(b.Nodes) {
			return len(a.Nodes) > len(b.Nodes)
		}
		if a.Severity != b.Severity {
			return severityOrder[a.Severity] < severityOrder[b.Severity]
		}
		return a.Rule < b.Rule
	})
	for _, m := range []string{Karpenter, CA, AutoscalerNone} {
		if perNode[m] == 0 {
			continue
		}
		switch m {
		case Karpenter:
			addNote(&r.DataNotes, fmt.Sprintf("Karpenter manages %d node(s) with omitted findings: rules that only apply to Cluster Autoscaler were left out there.", perNode[m]))
		case CA:
			addNote(&r.DataNotes, fmt.Sprintf("Cluster Autoscaler manages %d node(s) with omitted findings: rules that only apply to Karpenter were left out there.", perNode[m]))
		default:
			addNote(&r.DataNotes, fmt.Sprintf("%d node(s) have no node autoscaler (--node-autoscaler none): autoscaler-specific findings were left out there.", perNode[m]))
		}
	}
	pinnedIn := map[string]map[string]bool{}
	for _, p := range pinned {
		if pinnedIn[p.Namespace] == nil {
			pinnedIn[p.Namespace] = map[string]bool{}
		}
		pinnedIn[p.Namespace][p.Workload] = true
	}
	for _, u := range a.att.NamespacesByIdle() {
		line := NamespaceLine{Namespace: u.Namespace, Pods: u.Pods, Nodes: len(u.Shares), Requested: u.Requested, IdleShare: u.Idle, Pinned: len(pinnedIn[u.Namespace])}
		line.PackingCPU, line.PackingMemory = u.Packing()
		r.Namespaces = append(r.Namespaces, line)
	}
	for i := range s.NodePools {
		np := &s.NodePools[i]
		n := 0
		for j := range s.Nodes {
			if karpenterPool(&s.Nodes[j]) == np.Metadata.Name {
				n++
			}
		}
		r.NodePools = append(r.NodePools, NodePoolSettings{Name: np.Metadata.Name, Nodes: n, Settings: poolSettings(np)})
	}
	r.Summary = fmt.Sprintf("%d node(s) run; by requests alone their pods would fit on %d. Pods request %s of allocatable CPU and %s of memory. Verdicts: %d blocked, %d possibly blocked, %d with no blocker found.",
		r.Nodes, r.NodesNeeded, pct(r.PackingCPU), pct(r.PackingMemory),
		r.Verdicts[VerdictBlocked], r.Verdicts[VerdictPossiblyBlocked], r.Verdicts[VerdictNoBlockerFound])
	return r
}

// Truncate keeps the first n namespaces; n <= 0 keeps them all.
func (r *ClusterReport) Truncate(n int) {
	if n > 0 && len(r.Namespaces) > n {
		r.OmittedNamespaces = len(r.Namespaces) - n
		r.Namespaces = r.Namespaces[:n]
	}
}

// Pinned lists, in workload order, every workload held at an autoscaler
// minimum or at a paused KEDA replica count. A workload with two such
// controllers is listed once per controller.
func (a *Analyzer) Pinned() []Pinned {
	s := a.snap
	out := []Pinned{}
	for _, w := range a.workloads() {
		n, _ := replicas(s, w)
		sos, hpas := autoscalersOf(s, w.key)
		var pins []Pinned
		for _, so := range sos {
			if v, ok := so.Metadata.Annotations[annKedaPausedReplicas]; ok {
				if m, err := strconv.Atoi(v); err == nil {
					pins = append(pins, Pinned{Controller: "keda-paused", Min: int32(m)})
				}
			}
		}
		for _, f := range floorsAt(sos, hpas, n) {
			pins = append(pins, Pinned{Controller: strings.ToLower(f.controller), Min: f.min})
		}
		if len(pins) == 0 {
			continue
		}
		pods := workloadPods(s, w.key)
		req := requestsOf(pods)
		for _, p := range pins {
			p.Workload, p.Namespace, p.Replicas, p.Pods, p.Requested = w.key.ref(), w.key.Namespace, n, len(pods), req
			out = append(out, p)
		}
	}
	return out
}

// workloads lists, in name order, the workloads of every pod that is not
// finished and not bound to its node.
func (a *Analyzer) workloads() []workloadRef {
	s := a.snap
	seen := map[workloadKey]bool{}
	var ws []workloadRef
	for i := range s.Pods {
		p := &s.Pods[i]
		if snapshot.IsTerminal(p) || snapshot.IsNodeBound(p) {
			continue
		}
		w, ok := workloadOf(s, p)
		if !ok || seen[w.key] {
			continue
		}
		seen[w.key] = true
		ws = append(ws, w)
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].key.ref() < ws[j].key.ref() })
	return ws
}

func groupFloors(ps []Pinned) []FloorGroup {
	idx := map[string]int{}
	out := []FloorGroup{}
	for _, p := range ps {
		k := p.Controller + "/" + strconv.Itoa(int(p.Min))
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, FloorGroup{Controller: p.Controller, Min: p.Min})
		}
		out[i].Workloads++
		out[i].Pods += p.Pods
		out[i].Requested = out[i].Requested.Add(p.Requested)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Workloads != out[j].Workloads {
			return out[i].Workloads > out[j].Workloads
		}
		if out[i].Controller != out[j].Controller {
			return out[i].Controller < out[j].Controller
		}
		return out[i].Min < out[j].Min
	})
	return out
}
