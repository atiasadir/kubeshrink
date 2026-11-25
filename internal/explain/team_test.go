// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

func teamSnapshot(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	s, err := snapshot.Load("../../examples/team-bin-packing")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTeamExampleCluster pins the cluster view of the synthetic team example used in the README.
func TestTeamExampleCluster(t *testing.T) {
	r := ExplainCluster(teamSnapshot(t))
	if r.Nodes != 4 || r.NodesNeeded != 2 {
		t.Errorf("nodes %d, needed %d; want 4, 2", r.Nodes, r.NodesNeeded)
	}
	if got := pct(r.PackingCPU) + "/" + pct(r.PackingMemory); got != "31%/19%" {
		t.Errorf("packing = %s, want 31%%/19%%", got)
	}
	if r.Verdicts[VerdictBlocked] != 4 || r.ManagedBy[Karpenter] != 4 || r.TakenAt == "" || r.ServerVersion != "v1.31.2" {
		t.Errorf("verdicts %v, managed by %v, taken %q, version %q", r.Verdicts, r.ManagedBy, r.TakenAt, r.ServerVersion)
	}
	var reasons []string
	for _, rs := range r.TopReasons {
		reasons = append(reasons, fmt.Sprintf("%s:%d", rs.Rule, len(rs.Nodes)))
	}
	if got := strings.Join(reasons, " "); got != "no-room-elsewhere:4 karpenter-consolidation-policy:1 autoscaler-event:1" {
		t.Errorf("top reasons = %s", got)
	}
	var order []string
	for _, n := range r.Namespaces {
		order = append(order, n.Namespace)
	}
	if got := strings.Join(order, ","); got != "ml,checkout,reports,monitoring,batch" {
		t.Errorf("namespaces by idle = %s", got)
	}
	floors := map[string]int{}
	for _, g := range r.ReplicaFloors {
		floors[fmt.Sprintf("%s/%d", g.Controller, g.Min)] = g.Workloads
	}
	// The HPA that KEDA created for the worker must not count as a second floor.
	if floors["hpa/3"] != 1 || floors["keda/2"] != 1 || len(floors) != 2 {
		t.Errorf("replica floors = %v", floors)
	}
	r.Truncate(2)
	if len(r.Namespaces) != 2 || r.OmittedNamespaces != 3 {
		t.Errorf("truncate kept %d, omitted %d", len(r.Namespaces), r.OmittedNamespaces)
	}
}

func TestTeamExampleNodes(t *testing.T) {
	a := NewAnalyzer(teamSnapshot(t), Options{})
	cases := []struct {
		node    string
		want    map[string]Severity
		notWant []string
	}{
		{"general-a", map[string]Severity{"no-room-elsewhere": Blocker, "replica-floor": Context}, []string{"karpenter-consolidation-policy", "utilization-above-threshold"}},
		{"general-b", map[string]Severity{"no-room-elsewhere": Blocker, "autoscaler-event": PossibleBlocker}, nil},
		{"general-c", map[string]Severity{"no-room-elsewhere": Blocker, "karpenter-not-consolidatable-yet": Context}, []string{"pod-without-controller"}},
		{"ml-a", map[string]Severity{"no-room-elsewhere": Blocker, "karpenter-consolidation-policy": Blocker}, nil},
	}
	for _, c := range cases {
		r, err := a.Node(c.node)
		if err != nil {
			t.Fatal(err)
		}
		got := ruleSet(r)
		for rule, sev := range c.want {
			if got[rule] != sev {
				t.Errorf("%s: rule %s = %q, want %q (all %v)", c.node, rule, got[rule], sev, got)
			}
		}
		for _, rule := range c.notWant {
			if _, ok := got[rule]; ok {
				t.Errorf("%s: rule %s should be absent", c.node, rule)
			}
		}
	}
	r, _ := a.Node("general-b")
	for _, f := range r.Findings {
		if f.Rule == "autoscaler-event" && (len(f.Evidence) != 2 || f.Meaning == "" || f.Subject != "Node/general-b") {
			t.Errorf("the event on the node and on its NodeClaim should be one finding about the node: %+v", f)
		}
	}
	r, _ = a.Node("general-a")
	for _, f := range r.Findings {
		if f.Rule == "no-room-elsewhere" && (f.Subject != "Pod/checkout/api-7d9f-a" || !strings.Contains(f.Meaning, "pod anti-affinity on 2 node(s), taint on 1 node(s)")) {
			t.Errorf("only the api pod should lack room, because of anti-affinity and a taint: %s %s", f.Subject, f.Meaning)
		}
	}
}

func TestTeamExampleNamespace(t *testing.T) {
	s := teamSnapshot(t)
	r, err := ExplainNamespace(s, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if r.Pods != 3 || len(r.Nodes) != 3 || pct(r.PackingCPU) != "31%" {
		t.Errorf("pods %d, nodes %d, cpu packing %s", r.Pods, len(r.Nodes), pct(r.PackingCPU))
	}
	got := map[string]int{}
	for _, f := range r.Findings {
		got[f.Rule]++
	}
	if got["no-room-elsewhere"] != 3 || got["spread-constraint"] != 1 || got["replica-floor"] != 1 || got["karpenter-consolidation-policy"] != 0 {
		t.Errorf("findings = %v", got)
	}
	last := -1
	for _, f := range r.Findings {
		if layerOrder[f.Layer] < last {
			t.Fatalf("findings are not grouped by layer: %s after layer %d", f.Layer, last)
		}
		last = layerOrder[f.Layer]
	}
	if _, err := ExplainNamespace(s, "missing"); err == nil {
		t.Error("expected an error for a namespace without pods")
	}
}

func TestPinnedControllers(t *testing.T) {
	s := teamSnapshot(t)
	s.ScaledObjects[0].Metadata.Annotations = map[string]string{annKedaPausedReplicas: "1"}
	got := map[string]bool{}
	for _, p := range NewAnalyzer(s, Options{}).Pinned() {
		got[p.Controller+" "+p.Workload] = true
	}
	if !got["hpa Deployment/checkout/api"] || !got["keda-paused Deployment/reports/worker"] || got["keda Deployment/reports/worker"] || len(got) != 2 {
		t.Errorf("pinned = %v", got)
	}
}

func TestNodeAutoscalerOptions(t *testing.T) {
	s := base(pod("ns", "p", "n1", "100m", "128Mi", owner("", "")))
	r, _ := NewAnalyzer(s, Options{}).Node("n1")
	if r.ManagedBy != AutoscalerUnknown || ruleSet(r)["pod-without-controller"] != Blocker || !strings.Contains(strings.Join(r.DataNotes, " "), "No node autoscaler was found") {
		t.Errorf("auto with nothing seen: managed by %q, rules %v, notes %v", r.ManagedBy, ruleSet(r), r.DataNotes)
	}
	r, _ = NewAnalyzer(s, Options{NodeAutoscaler: AutoscalerNone}).Node("n1")
	if _, ok := ruleSet(r)["pod-without-controller"]; ok || r.ManagedBy != AutoscalerNone {
		t.Errorf("none: managed by %q, rules %v", r.ManagedBy, ruleSet(r))
	}
	s.Pods = append(s.Pods, pod("kube-system", "cluster-autoscaler-6f7d9-x1", "n2", "100m", "128Mi"))
	if r, _ = NewAnalyzer(s, Options{}).Node("n1"); r.ManagedBy != CA {
		t.Errorf("a Cluster Autoscaler pod should mark unlabeled nodes as CA-managed, got %q", r.ManagedBy)
	}
}

func TestKarpenterNodePoolRules(t *testing.T) {
	mk := func(d snapshot.NodePoolDisruption) *snapshot.Snapshot {
		s := base(pod("ns", "p", "n1", "100m", "128Mi"))
		s.Nodes[0].Metadata.Labels[labelKarpenterNodePool] = "pool"
		s.NodePools = []snapshot.NodePool{{Metadata: snapshot.ObjectMeta{Name: "pool"}, Spec: snapshot.NodePoolSpec{Disruption: d}}}
		return s
	}
	cases := []struct {
		name string
		d    snapshot.NodePoolDisruption
		rule string
		sev  Severity
	}{
		{"WhenEmpty with pods to move", snapshot.NodePoolDisruption{ConsolidationPolicy: "WhenEmpty"}, "karpenter-consolidation-policy", Blocker},
		{"consolidateAfter Never", snapshot.NodePoolDisruption{ConsolidateAfter: "Never"}, "karpenter-consolidation-disabled", Blocker},
		{"zero budget", snapshot.NodePoolDisruption{Budgets: []snapshot.Budget{{Nodes: "0"}}}, "karpenter-budget", Blocker},
		{"scheduled zero budget", snapshot.NodePoolDisruption{Budgets: []snapshot.Budget{{Nodes: "0%", Schedule: "0 9 * * mon-fri", Duration: "8h"}}}, "karpenter-budget", PossibleBlocker},
		{"zero budget for drift only", snapshot.NodePoolDisruption{Budgets: []snapshot.Budget{{Nodes: "0", Reasons: []string{"Drifted"}}}}, "", ""},
	}
	for _, c := range cases {
		r, err := ExplainNode(mk(c.d), "n1")
		if err != nil {
			t.Fatal(err)
		}
		got := ruleSet(r)
		if c.rule == "" {
			for rule := range got {
				if strings.HasPrefix(rule, "karpenter-") {
					t.Errorf("%s: unexpected %s", c.name, rule)
				}
			}
			continue
		}
		if got[c.rule] != c.sev {
			t.Errorf("%s: %s = %q, want %q (all %v)", c.name, c.rule, got[c.rule], c.sev, got)
		}
	}
}

func TestNodeClaimWithoutConsolidatableCondition(t *testing.T) {
	s := teamSnapshot(t)
	for i := range s.NodeClaims {
		if s.NodeClaims[i].Status.NodeName == "general-a" {
			s.NodeClaims[i].Status.Conditions = nil
		}
	}
	r, _ := ExplainNode(s, "general-a")
	if got := ruleSet(r)["karpenter-not-consolidatable-yet"]; got != Context {
		t.Errorf("a NodeClaim without the condition, while others carry it, should be reported; got %q", got)
	}
}

func TestEventAge(t *testing.T) {
	if got := eventAge("2026-11-21T03:00:00Z", "2026-11-21T02:55:00Z"); got != "5m0s" {
		t.Errorf("eventAge = %q", got)
	}
	if got := eventAge("", "2026-11-21T02:55:00Z"); got != "" {
		t.Errorf("unknown snapshot time should give no age, got %q", got)
	}
}
