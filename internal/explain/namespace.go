// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"

	"github.com/atiasadir/kubeshrink/internal/capacity"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// nsContext is what every namespace rule receives: the snapshot and the
// namespace's pods that are not bound to their node.
type nsContext struct {
	snap      *snapshot.Snapshot
	namespace string
	pods      []*snapshot.Pod
}

// NamespaceNode is one node a namespace uses.
type NamespaceNode struct {
	Node              string          `json:"node"`
	Pool              string          `json:"pool"`
	Requested         capacity.Amount `json:"requested"`
	IdleShare         capacity.Amount `json:"idleShare"`
	NodePackingCPU    float64         `json:"nodePackingCPU"`
	NodePackingMemory float64         `json:"nodePackingMemory"`
	Alone             bool            `json:"alone"`
	Verdict           string          `json:"verdict"`
}

// NamespaceReport answers "why is this team's bin-packing low?".
type NamespaceReport struct {
	Namespace     string          `json:"namespace"`
	Summary       string          `json:"summary"`
	Pods          int             `json:"pods"`
	Requested     capacity.Amount `json:"requested"`
	IdleShare     capacity.Amount `json:"idleShare"`
	PackingCPU    float64         `json:"packingCPU"`
	PackingMemory float64         `json:"packingMemory"`
	Nodes         []NamespaceNode `json:"nodes"`
	Findings      []Finding       `json:"findings"`
	// OtherTenantFindings counts findings on the same nodes that are about
	// other namespaces' pods.
	OtherTenantFindings int      `json:"otherTenantFindings"`
	DataNotes           []string `json:"dataNotes,omitempty"`
	Limitations         []string `json:"limitations"`
}

// Counts returns the number of findings per severity.
func (r *NamespaceReport) Counts() map[Severity]int { return countBySeverity(r.Findings) }

// ExplainNamespace explains one namespace with default options.
func ExplainNamespace(s *snapshot.Snapshot, ns string) (*NamespaceReport, error) {
	return NewAnalyzer(s, Options{}).Namespace(ns)
}

// Namespace answers "why is this team's bin-packing low?": what the namespace
// requests, the share of idle capacity it holds on the nodes it uses, and the
// findings about its own workloads and about those nodes.
func (a *Analyzer) Namespace(ns string) (*NamespaceReport, error) {
	u := a.att.Namespaces[ns]
	pods := a.snap.PodsInNamespace(ns)
	if u == nil && len(pods) == 0 {
		return nil, fmt.Errorf("namespace %q has no running pods in the snapshot (%d namespace(s) loaded)", ns, len(a.snap.Namespaces()))
	}
	r := &NamespaceReport{
		Namespace: ns, Pods: len(pods), Nodes: []NamespaceNode{},
		DataNotes: append([]string(nil), a.snap.Notes...), Limitations: Limitations,
	}
	c := &nsContext{snap: a.snap, namespace: ns}
	for _, p := range pods {
		if !snapshot.IsNodeBound(p) {
			c.pods = append(c.pods, p)
		}
	}
	var fs []Finding
	for _, rule := range Catalog {
		if rule.nsCheck != nil {
			fs = append(fs, rule.nsCheck(c)...)
		}
	}
	if u != nil {
		r.Requested, r.IdleShare = u.Requested, u.Idle
		r.PackingCPU, r.PackingMemory = u.Packing()
		for _, sh := range u.Shares {
			nr, err := a.Node(sh.Node)
			if err != nil {
				continue
			}
			line := NamespaceNode{Node: sh.Node, Pool: nr.Pool, Requested: sh.Requested, IdleShare: sh.Idle, Alone: sh.Exclusive, Verdict: nr.Verdict}
			if nu := a.att.Node(sh.Node); nu != nil {
				line.NodePackingCPU, line.NodePackingMemory = nu.Packing()
			}
			r.Nodes = append(r.Nodes, line)
			for _, f := range nr.Findings {
				if f.Layer == LayerReplicas {
					continue // the namespace's own replica floors come from the namespace check
				}
				if owner, namespaced := subjectNamespace(f.Subject); namespaced && owner != ns {
					r.OtherTenantFindings++
					continue
				}
				fs = append(fs, f)
			}
			for _, n := range nr.DataNotes {
				addNote(&r.DataNotes, n)
			}
		}
	}
	r.Findings = finalize(dedupe(fs), true)
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	counts := r.Counts()
	r.Summary = fmt.Sprintf("%s runs %d pod(s) on %d node(s) and requests %s. Its share of the idle capacity on those nodes is %s, so its packing is %s CPU and %s memory. %d blocker(s), %d possible blocker(s), %d warning(s), %d context finding(s).",
		ns, r.Pods, len(r.Nodes), fmtAmount(r.Requested), fmtAmount(r.IdleShare), pct(r.PackingCPU), pct(r.PackingMemory),
		counts[Blocker], counts[PossibleBlocker], counts[Warning], counts[Context])
	return r, nil
}

// checkSpreadConstraints finds workloads whose replicas must stay apart:
// required anti-affinity against their own pods, or an enforced topology
// spread over them. Neither blocks a node by itself, but both limit packing.
func checkSpreadConstraints(c *nsContext) []Finding {
	var out []Finding
	seen := map[workloadKey]bool{}
	for _, p := range c.pods {
		w, ok := workloadOf(c.snap, p)
		if !ok || seen[w.key] {
			continue
		}
		seen[w.key] = true
		pods := workloadPods(c.snap, w.key)
		nodes := map[string]bool{}
		for _, q := range pods {
			if q.Spec.NodeName != "" {
				nodes[q.Spec.NodeName] = true
			}
		}
		wref := w.key.ref()
		for i, t := range snapshot.RequiredPodAntiAffinity(p) {
			if !t.SelectsPod(p, p) {
				continue
			}
			out = append(out, Finding{
				Rule: "spread-constraint", Severity: Context, Subject: wref, Confidence: High,
				Claim:    fmt.Sprintf("Required pod anti-affinity on %s allows at most one replica of this workload per %s. Its %d pod(s) run on %d node(s), each requesting %s.", t.TopologyKey, domainName(t.TopologyKey), len(pods), len(nodes), cpuMem(snapshot.PodRequests(p))),
				Meaning:  "Each replica needs its own " + domainName(t.TopologyKey) + ", so the room left around it can only be used by other workloads.",
				Evidence: []Evidence{{Object: podRef(p), Field: fmt.Sprintf(".spec.affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution[%d].topologyKey", i), Value: t.TopologyKey}},
			})
		}
		for i, tsc := range p.Spec.TopologySpreadConstraints {
			if !tsc.Enforced() || !tsc.LabelSelector.Matches(p.Metadata.Labels) {
				continue
			}
			out = append(out, Finding{
				Rule: "spread-constraint", Severity: Context, Subject: wref, Confidence: High,
				Claim:    fmt.Sprintf("An enforced topology spread over %s (maxSkew %d) keeps the replicas of this workload balanced across each %s. Its %d pod(s) run on %d node(s).", tsc.TopologyKey, tsc.MaxSkew, domainName(tsc.TopologyKey), len(pods), len(nodes)),
				Meaning:  "The scheduler refuses placements that unbalance the replicas, which limits how tightly they can be packed.",
				Evidence: []Evidence{{Object: podRef(p), Field: fmt.Sprintf(".spec.topologySpreadConstraints[%d]", i), Value: fmt.Sprintf("topologyKey=%s maxSkew=%d whenUnsatisfiable=%s", tsc.TopologyKey, tsc.MaxSkew, tsc.WhenUnsatisfiable)}},
			})
		}
	}
	return out
}
