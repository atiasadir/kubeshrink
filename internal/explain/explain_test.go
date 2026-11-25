// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// --- builders for small synthetic snapshots ---

func i32(v int32) *int32 { return &v }
func yes() *bool         { b := true; return &b }

func node(name, cpu, mem string, mods ...func(*snapshot.Node)) snapshot.Node {
	n := snapshot.Node{
		Metadata: snapshot.ObjectMeta{Name: name, Labels: map[string]string{}, Annotations: map[string]string{}},
		Status:   snapshot.NodeStatus{Allocatable: map[string]string{"cpu": cpu, "memory": mem, "pods": "110"}},
	}
	for _, m := range mods {
		m(&n)
	}
	return n
}

// pod builds a running pod owned by ReplicaSet "app-rs1" (so Deployment "app") unless a mod changes it.
func pod(ns, name, nodeName, cpu, mem string, mods ...func(*snapshot.Pod)) snapshot.Pod {
	p := snapshot.Pod{
		Metadata: snapshot.ObjectMeta{
			Name: name, Namespace: ns,
			Labels:          map[string]string{"app": "app"},
			Annotations:     map[string]string{},
			OwnerReferences: []snapshot.OwnerReference{{Kind: "ReplicaSet", Name: "app-rs1", Controller: yes()}},
		},
		Spec: snapshot.PodSpec{
			NodeName:   nodeName,
			Containers: []snapshot.Container{{Name: "c", Resources: snapshot.ResourceRequirements{Requests: map[string]string{"cpu": cpu, "memory": mem}}}},
		},
		Status: snapshot.PodStatus{Phase: "Running"},
	}
	for _, m := range mods {
		m(&p)
	}
	return p
}

func annotate(k, v string) func(*snapshot.Pod) {
	return func(p *snapshot.Pod) { p.Metadata.Annotations[k] = v }
}

func owner(kind, name string) func(*snapshot.Pod) {
	return func(p *snapshot.Pod) {
		p.Metadata.OwnerReferences = nil
		if kind != "" {
			p.Metadata.OwnerReferences = []snapshot.OwnerReference{{Kind: kind, Name: name, Controller: yes()}}
		}
	}
}

func pdb(ns, app string, allowed *int32) snapshot.PodDisruptionBudget {
	return snapshot.PodDisruptionBudget{
		Metadata: snapshot.ObjectMeta{Name: app + "-pdb", Namespace: ns},
		Spec:     snapshot.PDBSpec{Selector: &snapshot.LabelSelector{MatchLabels: map[string]string{"app": app}}},
		Status:   snapshot.PDBStatus{DisruptionsAllowed: allowed, CurrentHealthy: 1, DesiredHealthy: 1},
	}
}

func hpa(ns, name, target string, minReplicas int32) snapshot.HorizontalPodAutoscaler {
	return snapshot.HorizontalPodAutoscaler{
		Metadata: snapshot.ObjectMeta{Name: name, Namespace: ns},
		Spec:     snapshot.HPASpec{ScaleTargetRef: snapshot.CrossVersionObjectReference{Kind: "Deployment", Name: target}, MinReplicas: i32(minReplicas), MaxReplicas: 10},
	}
}

func deployment(ns, name string, replicas int32, ann map[string]string) snapshot.Workload {
	return snapshot.Workload{Kind: "Deployment", Metadata: snapshot.ObjectMeta{Name: name, Namespace: ns, Annotations: ann}, Spec: snapshot.WorkloadSpec{Replicas: i32(replicas)}}
}

// base returns a snapshot with the node under test (n1) and a roomy second node (n2).
func base(pods ...snapshot.Pod) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		Nodes:   []snapshot.Node{node("n1", "4", "16Gi"), node("n2", "8", "32Gi")},
		Pods:    pods,
		Skipped: map[string]int{},
	}
}

func ruleSet(r *NodeReport) map[string]Severity {
	out := map[string]Severity{}
	for _, f := range r.Findings {
		out[f.Rule] = f.Severity
	}
	return out
}

func TestRules(t *testing.T) {
	tests := []struct {
		name    string
		snap    func() *snapshot.Snapshot
		want    map[string]Severity // rule -> severity that must be present
		notWant []string            // rules that must be absent
	}{
		{
			name:    "Karpenter needs a new node for a pod spread by hostname",
			snap:    func() *snapshot.Snapshot { return hostnameSpreadSnap("DoNotSchedule", true, false) },
			want:    map[string]Severity{"karpenter-needs-new-node": PossibleBlocker},
			notWant: []string{"no-room-elsewhere"},
		},
		{
			name:    "Karpenter applies a ScheduleAnyway hostname spread too",
			snap:    func() *snapshot.Snapshot { return hostnameSpreadSnap("ScheduleAnyway", true, false) },
			want:    map[string]Severity{"karpenter-needs-new-node": PossibleBlocker},
			notWant: []string{"no-room-elsewhere"},
		},
		{
			name:    "A node without a matching pod leaves Karpenter room",
			snap:    func() *snapshot.Snapshot { return hostnameSpreadSnap("DoNotSchedule", true, true) },
			notWant: []string{"karpenter-needs-new-node", "no-room-elsewhere"},
		},
		{
			name:    "Hostname spread on nodes Karpenter does not manage",
			snap:    func() *snapshot.Snapshot { return hostnameSpreadSnap("DoNotSchedule", false, false) },
			notWant: []string{"karpenter-needs-new-node", "no-room-elsewhere"},
		},
		{
			name: "PDB with zero disruptions blocks",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.PDBs = []snapshot.PodDisruptionBudget{pdb("ns", "app", i32(0))}
				return s
			},
			want: map[string]Severity{"pdb-zero-disruptions": Blocker},
		},
		{
			name: "PDB with room does not block",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.PDBs = []snapshot.PodDisruptionBudget{pdb("ns", "app", i32(1))}
				return s
			},
			notWant: []string{"pdb-zero-disruptions", "multiple-pdbs"},
		},
		{
			name: "PDB in another namespace is ignored",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.PDBs = []snapshot.PodDisruptionBudget{pdb("other", "app", i32(0))}
				return s
			},
			notWant: []string{"pdb-zero-disruptions"},
		},
		{
			name: "PDB without status is only a possible blocker",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.PDBs = []snapshot.PodDisruptionBudget{pdb("ns", "app", nil)}
				return s
			},
			want: map[string]Severity{"pdb-zero-disruptions": PossibleBlocker},
		},
		{
			name: "two PDBs on one pod block eviction",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				second := pdb("ns", "app", i32(5))
				second.Metadata.Name = "second"
				s.PDBs = []snapshot.PodDisruptionBudget{pdb("ns", "app", i32(5)), second}
				return s
			},
			want:    map[string]Severity{"multiple-pdbs": Blocker},
			notWant: []string{"pdb-zero-disruptions"},
		},
		{
			name: "bare pod blocks Cluster Autoscaler",
			snap: func() *snapshot.Snapshot { return base(pod("ns", "p", "n1", "100m", "128Mi", owner("", ""))) },
			want: map[string]Severity{"pod-without-controller": Blocker},
		},
		{
			name: "bare pod marked safe-to-evict does not",
			snap: func() *snapshot.Snapshot {
				return base(pod("ns", "p", "n1", "100m", "128Mi", owner("", ""), annotate(annCASafeToEvict, "true")))
			},
			notWant: []string{"pod-without-controller"},
		},
		{
			name: "emptyDir is a possible blocker",
			snap: func() *snapshot.Snapshot {
				return base(pod("ns", "p", "n1", "100m", "128Mi", func(p *snapshot.Pod) {
					p.Spec.Volumes = []snapshot.Volume{{Name: "tmp", EmptyDir: []byte("{}")}}
				}))
			},
			want: map[string]Severity{"local-storage": PossibleBlocker},
		},
		{
			name: "emptyDir listed in safe-to-evict-local-volumes is fine",
			snap: func() *snapshot.Snapshot {
				return base(pod("ns", "p", "n1", "100m", "128Mi", annotate(annCASafeToEvictLocalVolumes, "tmp"), func(p *snapshot.Pod) {
					p.Spec.Volumes = []snapshot.Volume{{Name: "tmp", EmptyDir: []byte("{}")}}
				}))
			},
			notWant: []string{"local-storage"},
		},
		{
			name: "karpenter do-not-disrupt on a pod",
			snap: func() *snapshot.Snapshot {
				return base(pod("ns", "p", "n1", "100m", "128Mi", annotate(annKarpenterDoNotDisrupt, "true")))
			},
			want: map[string]Severity{"karpenter-do-not-disrupt": Blocker},
		},
		{
			name: "karpenter do-not-disrupt and CA scale-down-disabled on the node",
			snap: func() *snapshot.Snapshot {
				s := base()
				s.Nodes[0].Metadata.Annotations[annKarpenterDoNotDisrupt] = "true"
				s.Nodes[0].Metadata.Annotations[annCAScaleDownDisabled] = "true"
				return s
			},
			want: map[string]Severity{"karpenter-do-not-disrupt": Blocker, "ca-scale-down-disabled": Blocker},
		},
		{
			name: "safe-to-evict=false",
			snap: func() *snapshot.Snapshot {
				return base(pod("ns", "p", "n1", "100m", "128Mi", annotate(annCASafeToEvict, "false")))
			},
			want: map[string]Severity{"ca-safe-to-evict-false": Blocker},
		},
		{
			name: "kube-system pod without PDB",
			snap: func() *snapshot.Snapshot { return base(pod("kube-system", "dns", "n1", "100m", "128Mi")) },
			want: map[string]Severity{"kube-system-pod": PossibleBlocker},
		},
		{
			name: "DaemonSet pods are ignored entirely",
			snap: func() *snapshot.Snapshot {
				return base(pod("kube-system", "proxy", "n1", "100m", "128Mi", owner("DaemonSet", "proxy"), annotate(annKarpenterDoNotDisrupt, "true")))
			},
			notWant: []string{"kube-system-pod", "karpenter-do-not-disrupt", "no-room-elsewhere"},
		},
		{
			name: "pod too big for any other node",
			snap: func() *snapshot.Snapshot { return base(pod("ns", "p", "n1", "9", "1Gi")) },
			want: map[string]Severity{"no-room-elsewhere": Blocker},
		},
		{
			name: "untolerated taint leaves no room",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.Nodes[1].Spec.Taints = []snapshot.Taint{{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}}
				return s
			},
			want: map[string]Severity{"no-room-elsewhere": Blocker},
		},
		{
			name: "tolerated taint is fine",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi", func(p *snapshot.Pod) {
					p.Spec.Tolerations = []snapshot.Toleration{{Key: "dedicated", Operator: "Equal", Value: "gpu", Effect: "NoSchedule"}}
				}))
				s.Nodes[1].Spec.Taints = []snapshot.Taint{{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}}
				return s
			},
			notWant: []string{"no-room-elsewhere"},
		},
		{
			name: "cordoned node is not a destination",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.Nodes[1].Spec.Unschedulable = true
				return s
			},
			want: map[string]Severity{"no-room-elsewhere": Blocker},
		},
		{
			name: "utilization above the CA threshold",
			snap: func() *snapshot.Snapshot { return base(pod("ns", "p", "n1", "3", "1Gi")) },
			want: map[string]Severity{"utilization-above-threshold": PossibleBlocker},
		},
		{
			name: "workload at its HPA floor",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.Workloads = []snapshot.Workload{deployment("ns", "app", 2, nil)}
				s.HPAs = []snapshot.HorizontalPodAutoscaler{hpa("ns", "app", "app", 2)}
				return s
			},
			want: map[string]Severity{"replica-floor": Context},
		},
		{
			name: "workload above its HPA floor",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.Workloads = []snapshot.Workload{deployment("ns", "app", 5, nil)}
				s.HPAs = []snapshot.HorizontalPodAutoscaler{hpa("ns", "app", "app", 2)}
				return s
			},
			notWant: []string{"replica-floor"},
		},
		{
			name: "KEDA paused replicas",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.ScaledObjects = []snapshot.ScaledObject{{
					Metadata: snapshot.ObjectMeta{Name: "app", Namespace: "ns", Annotations: map[string]string{annKedaPausedReplicas: "3"}},
					Spec:     snapshot.ScaledObjectSpec{ScaleTargetRef: snapshot.CrossVersionObjectReference{Name: "app"}},
				}}
				return s
			},
			want: map[string]Severity{"keda-paused": Context},
		},
		{
			name: "KEDA's own HPA is not a conflict",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.ScaledObjects = []snapshot.ScaledObject{{
					Metadata: snapshot.ObjectMeta{Name: "app", Namespace: "ns"},
					Spec:     snapshot.ScaledObjectSpec{ScaleTargetRef: snapshot.CrossVersionObjectReference{Name: "app"}, MinReplicaCount: i32(1)},
				}}
				s.HPAs = []snapshot.HorizontalPodAutoscaler{hpa("ns", "keda-hpa-app", "app", 1)}
				return s
			},
			want:    map[string]Severity{"replica-floor": Context},
			notWant: []string{"keda-hpa-conflict"},
		},
		{
			name: "kube-downscaler target below the HPA floor",
			snap: func() *snapshot.Snapshot {
				s := base(pod("ns", "p", "n1", "100m", "128Mi"))
				s.Workloads = []snapshot.Workload{deployment("ns", "app", 3, map[string]string{annDownscalerDowntimeReplicas: "0", annDownscalerExclude: "true"})}
				s.HPAs = []snapshot.HorizontalPodAutoscaler{hpa("ns", "app", "app", 2)}
				return s
			},
			want: map[string]Severity{"downscaler-vs-hpa-floor": Warning, "downscaler-excluded": Context},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ExplainNode(tt.snap(), "n1")
			if err != nil {
				t.Fatal(err)
			}
			got := ruleSet(r)
			for rule, sev := range tt.want {
				if got[rule] != sev {
					t.Errorf("rule %s: severity %q, want %q (all: %v)", rule, got[rule], sev, got)
				}
			}
			for _, rule := range tt.notWant {
				if _, ok := got[rule]; ok {
					t.Errorf("rule %s should not fire (all: %v)", rule, got)
				}
			}
			for _, f := range r.Findings {
				if len(f.Evidence) == 0 {
					t.Errorf("finding %s on %s has no evidence", f.Rule, f.Subject)
				}
			}
		})
	}
}

// Two pods each fit on n2 alone, but not together: the second must be reported.
func TestFitReservesCapacity(t *testing.T) {
	s := base(pod("ns", "a", "n1", "2", "1Gi"), pod("ns", "b", "n1", "1500m", "1Gi"))
	s.Nodes[1] = node("n2", "3", "32Gi")
	r, err := ExplainNode(s, "n1")
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, f := range r.Findings {
		if f.Rule == "no-room-elsewhere" {
			subjects = append(subjects, f.Subject)
		}
	}
	if len(subjects) != 1 || subjects[0] != "Pod/ns/b" {
		t.Fatalf("no-room-elsewhere subjects = %v, want [Pod/ns/b]", subjects)
	}
}

// With no other node at all, report once for the node rather than once per pod.
func TestSingleNodeReportsOnce(t *testing.T) {
	s := base(pod("ns", "a", "n1", "100m", "128Mi"), pod("ns", "b", "n1", "100m", "128Mi"))
	s.Nodes = s.Nodes[:1]
	r, err := ExplainNode(s, "n1")
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, f := range r.Findings {
		if f.Rule == "no-room-elsewhere" {
			subjects = append(subjects, f.Subject)
		}
	}
	if len(subjects) != 1 || subjects[0] != "Node/n1" {
		t.Fatalf("no-room-elsewhere subjects = %v, want [Node/n1]", subjects)
	}
}

// On a Karpenter-managed node, Cluster Autoscaler-only findings are dropped and the omission is noted.
func TestKarpenterNodeOmitsCAOnlyFindings(t *testing.T) {
	s := base(pod("ns", "p", "n1", "100m", "128Mi", owner("", ""), annotate(annKarpenterDoNotDisrupt, "true"), func(p *snapshot.Pod) {
		p.Spec.Volumes = []snapshot.Volume{{Name: "tmp", EmptyDir: []byte("{}")}}
	}))
	s.Nodes[0].Metadata.Labels[labelKarpenterNodePool] = "general"
	r, err := ExplainNode(s, "n1")
	if err != nil {
		t.Fatal(err)
	}
	got := ruleSet(r)
	for _, rule := range []string{"local-storage", "pod-without-controller"} {
		if _, ok := got[rule]; ok {
			t.Errorf("CA-only rule %s should be omitted on a Karpenter node", rule)
		}
	}
	if _, ok := got["karpenter-do-not-disrupt"]; !ok {
		t.Error("Karpenter findings must be kept")
	}
	if r.ManagedBy != Karpenter || !strings.Contains(strings.Join(r.DataNotes, "\n"), "2 finding(s) that only apply to Cluster Autoscaler were omitted") {
		t.Errorf("ManagedBy = %q, DataNotes = %v", r.ManagedBy, r.DataNotes)
	}
}

func TestUnknownNode(t *testing.T) {
	if _, err := ExplainNode(base(), "missing"); err == nil {
		t.Fatal("expected an error for an unknown node")
	}
}

// TestExample pins the behaviour on the synthetic weekend scenario used in the README.
func TestExample(t *testing.T) {
	s, err := snapshot.Load("../../examples/weekend-scale-down")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		node    string
		verdict string
		rules   []string
	}{
		{"node-a", VerdictBlocked, []string{"pdb-zero-disruptions", "replica-floor", "downscaler-vs-hpa-floor"}},
		{"node-b", VerdictBlocked, []string{"pod-without-controller", "local-storage"}},
		{"node-c", VerdictBlocked, []string{"karpenter-do-not-disrupt", "no-room-elsewhere", "utilization-above-threshold", "keda-hpa-conflict", "autoscaler-event"}},
		{"node-d", VerdictNoBlockerFound, []string{"replica-floor", "keda-hpa-conflict"}},
	}
	for _, tt := range tests {
		t.Run(tt.node, func(t *testing.T) {
			r, err := ExplainNode(s, tt.node)
			if err != nil {
				t.Fatal(err)
			}
			if r.Verdict != tt.verdict {
				t.Errorf("verdict = %s, want %s", r.Verdict, tt.verdict)
			}
			got := ruleSet(r)
			for _, rule := range tt.rules {
				if _, ok := got[rule]; !ok {
					t.Errorf("missing rule %s (got %v)", rule, got)
				}
			}
		})
	}
}

func TestRenderers(t *testing.T) {
	s, err := snapshot.Load("../../examples/weekend-scale-down")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := ExplainNode(s, "node-a")
	var text bytes.Buffer
	if err := WriteNodeText(&text, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Node node-a: BLOCKED", "pdb-zero-disruptions", "Evidence:", "Limitations:"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text output missing %q", want)
		}
	}
	var js bytes.Buffer
	if err := WriteJSON(&js, r); err != nil {
		t.Fatal(err)
	}
	var decoded NodeReport
	if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Verdict != VerdictBlocked || len(decoded.Findings) != len(r.Findings) {
		t.Errorf("JSON round trip lost data: %+v", decoded)
	}
}

func TestCatalogIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Catalog {
		if seen[r.ID] {
			t.Errorf("duplicate rule ID %s", r.ID)
		}
		seen[r.ID] = true
	}
}

// hostnameSpreadSnap builds n1 and n2, each running one dns pod spread on the
// node hostname with maxSkew 1. karpenter puts both nodes in a NodePool;
// spare adds a node n3 without a dns pod.
func hostnameSpreadSnap(when string, karpenter, spare bool) *snapshot.Snapshot {
	spread := func(p *snapshot.Pod) {
		p.Metadata.Labels = map[string]string{"app": "dns"}
		p.Spec.TopologySpreadConstraints = []snapshot.TopologySpreadConstraint{{
			MaxSkew: 1, TopologyKey: labelHostname, WhenUnsatisfiable: when,
			LabelSelector: &snapshot.LabelSelector{MatchLabels: map[string]string{"app": "dns"}},
		}}
	}
	s := base(pod("ns", "dns-1", "n1", "100m", "70Mi", spread), pod("ns", "dns-2", "n2", "100m", "70Mi", spread))
	if spare {
		s.Nodes = append(s.Nodes, node("n3", "4", "16Gi"))
	}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		n.Metadata.Labels[labelHostname] = n.Metadata.Name
		if karpenter {
			n.Metadata.Labels[labelKarpenterNodePool] = "system"
		}
	}
	return s
}

// An autoscaler event that karpenter-needs-new-node explains no longer gets
// the note that the placement check disagrees with Karpenter.
func TestKarpenterEventNote(t *testing.T) {
	event := Finding{Rule: "autoscaler-event", Severity: PossibleBlocker}
	r := &NodeReport{Findings: []Finding{event}}
	annotateKarpenterEvents(r, 1)
	if len(r.Findings[0].Unknowns) != 1 {
		t.Fatalf("unexplained event: unknowns %v, want the disagreement note", r.Findings[0].Unknowns)
	}
	r = &NodeReport{Findings: []Finding{event, {Rule: "karpenter-needs-new-node", Severity: PossibleBlocker}}}
	annotateKarpenterEvents(r, 1)
	if len(r.Findings[0].Unknowns) != 0 {
		t.Errorf("explained event: unknowns %v, want none", r.Findings[0].Unknowns)
	}
}
