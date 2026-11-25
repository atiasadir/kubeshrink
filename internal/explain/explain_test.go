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

func ruleSet(r *NodeReport) map[string]Severity { return rulesOf(r.Findings) }

// rulesOf maps each rule that fired to its severity.
func rulesOf(findings []Finding) map[string]Severity {
	out := map[string]Severity{}
	for _, f := range findings {
		out[f.Rule] = f.Severity
	}
	return out
}

// inNodePool puts node n1, with one pod to move, in Karpenter NodePool "pool".
func inNodePool(d snapshot.NodePoolDisruption) *snapshot.Snapshot {
	s := base(pod("ns", "p", "n1", "100m", "128Mi"))
	s.Nodes[0].Metadata.Labels[labelKarpenterNodePool] = "pool"
	s.NodePools = []snapshot.NodePool{{Metadata: snapshot.ObjectMeta{Name: "pool"}, Spec: snapshot.NodePoolSpec{Disruption: d}}}
	return s
}

// withNodeClaim gives Karpenter node n1 a NodeClaim whose Consolidatable
// condition has the given status.
func withNodeClaim(status string) *snapshot.Snapshot {
	s := inNodePool(snapshot.NodePoolDisruption{ConsolidationPolicy: "WhenEmptyOrUnderutilized"})
	s.NodeClaims = []snapshot.NodeClaim{{
		Metadata: snapshot.ObjectMeta{Name: "pool-x1"},
		Status:   snapshot.NodeClaimStatus{NodeName: "n1", Conditions: []snapshot.Condition{{Type: "Consolidatable", Status: status}}},
	}}
	return s
}

// withEvent adds a Karpenter event saying it cannot replace the named node.
func withEvent(nodeName string) *snapshot.Snapshot {
	s := base(pod("ns", "p", "n1", "100m", "128Mi"))
	s.Events = []snapshot.Event{{
		Metadata:       snapshot.ObjectMeta{Name: "e1", Namespace: "default"},
		InvolvedObject: snapshot.ObjectReference{Kind: "Node", Name: nodeName},
		Reason:         "Unconsolidatable",
		Message:        "Can't replace with a cheaper node",
		Source:         snapshot.EventSource{Component: "karpenter"},
	}}
	return s
}

// withSpread is one replica of Deployment app with a hostname topology spread
// constraint; whenUnsatisfiable decides whether the scheduler enforces it.
func withSpread(whenUnsatisfiable string) *snapshot.Snapshot {
	s := base(pod("ns", "p", "n1", "100m", "128Mi", func(p *snapshot.Pod) {
		p.Spec.TopologySpreadConstraints = []snapshot.TopologySpreadConstraint{{
			MaxSkew: 1, TopologyKey: "kubernetes.io/hostname", WhenUnsatisfiable: whenUnsatisfiable,
			LabelSelector: &snapshot.LabelSelector{MatchLabels: map[string]string{"app": "app"}},
		}}
	}))
	s.Workloads = []snapshot.Workload{deployment("ns", "app", 1, nil)}
	return s
}

// withCPUHPA is Deployment app with a VPA in the given mode and an HPA that
// scales it on CPU utilization.
func withCPUHPA(mode string) *snapshot.Snapshot {
	s := sized("1", "2Gi", vpa("ns", "Deployment", "app", mode, rec(res("1", "2Gi"), nil)))
	h := hpa("ns", "app", "app", 1)
	h.Spec.Metrics = []snapshot.HPAMetric{{Type: "Resource", Resource: &snapshot.HPAResourceSource{Name: "cpu"}}}
	s.HPAs = []snapshot.HorizontalPodAutoscaler{h}
	return s
}

// withMinAllowed is Deployment app whose VPA has the given target and
// minAllowed, and an uncapped target of 100m CPU and 256Mi.
func withMinAllowed(target, minAllowed map[string]string) *snapshot.Snapshot {
	v := vpa("ns", "Deployment", "app", "Auto", snapshot.ContainerRecommendation{ContainerName: "c", Target: target, UncappedTarget: res("100m", "256Mi")})
	v.Spec.ResourcePolicy = &snapshot.VPAResourcePolicy{ContainerPolicies: []snapshot.VPAContainerPolicy{{ContainerName: "c", MinAllowed: minAllowed}}}
	return sized("1", "2Gi", v)
}

// ruleCase is one row of the rule table: a small snapshot, the rules that must
// fire on node n1 (or in a namespace, when namespace is set) with their
// severity, and the rules that must not fire.
type ruleCase struct {
	name      string
	snap      func() *snapshot.Snapshot
	namespace string              // explain this namespace instead of node n1
	want      map[string]Severity // rule -> severity that must be present
	notWant   []string            // rules that must be absent
}

// ruleCases has, for every catalog rule, at least one case where the rule
// fires and one where it must not; TestEveryRuleHasCases keeps it that way.
var ruleCases = []ruleCase{
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
		notWant: []string{"pod-without-controller", "ca-safe-to-evict-false"},
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
		notWant: []string{"keda-hpa-conflict", "keda-paused"},
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
	{
		name: "a separate HPA next to a KEDA ScaledObject",
		snap: func() *snapshot.Snapshot {
			s := base(pod("ns", "p", "n1", "100m", "128Mi"))
			s.ScaledObjects = []snapshot.ScaledObject{{
				Metadata: snapshot.ObjectMeta{Name: "app", Namespace: "ns"},
				Spec:     snapshot.ScaledObjectSpec{ScaleTargetRef: snapshot.CrossVersionObjectReference{Name: "app"}, MinReplicaCount: i32(1)},
			}}
			s.HPAs = []snapshot.HorizontalPodAutoscaler{hpa("ns", "app-extra", "app", 1)}
			return s
		},
		want: map[string]Severity{"keda-hpa-conflict": Warning},
	},
	{
		name: "kube-downscaler target at the HPA floor",
		snap: func() *snapshot.Snapshot {
			s := base(pod("ns", "p", "n1", "100m", "128Mi"))
			s.Workloads = []snapshot.Workload{deployment("ns", "app", 3, map[string]string{annDownscalerDowntimeReplicas: "2"})}
			s.HPAs = []snapshot.HorizontalPodAutoscaler{hpa("ns", "app", "app", 2)}
			return s
		},
		notWant: []string{"downscaler-vs-hpa-floor", "downscaler-excluded"},
	},
	{
		name: "CA scale-down-disabled set to false",
		snap: func() *snapshot.Snapshot {
			s := base()
			s.Nodes[0].Metadata.Annotations[annCAScaleDownDisabled] = "false"
			return s
		},
		notWant: []string{"ca-scale-down-disabled"},
	},
	{
		name:    "utilization below the CA threshold",
		snap:    func() *snapshot.Snapshot { return base(pod("ns", "p", "n1", "1", "1Gi")) },
		notWant: []string{"utilization-above-threshold"},
	},
	{
		name: "requests above the VPA upper bound",
		snap: func() *snapshot.Snapshot {
			return sized("1", "2Gi", vpa("ns", "Deployment", "app", "Auto", rec(res("200m", "512Mi"), res("400m", "1Gi"))))
		},
		namespace: "ns",
		want:      map[string]Severity{"requests-above-vpa": Warning},
	},
	{
		name: "requests within 25% of the VPA target",
		snap: func() *snapshot.Snapshot {
			return sized("1", "2Gi", vpa("ns", "Deployment", "app", "Auto", rec(res("900m", "1800Mi"), res("2", "4Gi"))))
		},
		namespace: "ns",
		notWant:   []string{"requests-above-vpa"},
	},
	{
		name: "VPA in mode Off with a different target",
		snap: func() *snapshot.Snapshot {
			return sized("1", "2Gi", vpa("ns", "Deployment", "app", "Off", rec(res("200m", "512Mi"), nil)))
		},
		namespace: "ns",
		want:      map[string]Severity{"vpa-not-applied": Warning},
	},
	{
		name: "VPA in mode Auto applies its target",
		snap: func() *snapshot.Snapshot {
			return sized("1", "2Gi", vpa("ns", "Deployment", "app", "Auto", rec(res("200m", "512Mi"), nil)))
		},
		namespace: "ns",
		notWant:   []string{"vpa-not-applied"},
	},
	{
		name:      "VPA in mode Auto and an HPA on CPU",
		snap:      func() *snapshot.Snapshot { return withCPUHPA("Auto") },
		namespace: "ns",
		want:      map[string]Severity{"vpa-hpa-conflict": Warning},
	},
	{
		name:      "VPA in mode Off and an HPA on CPU",
		snap:      func() *snapshot.Snapshot { return withCPUHPA("Off") },
		namespace: "ns",
		notWant:   []string{"vpa-hpa-conflict"},
	},
	{
		name:      "VPA minAllowed above the uncapped target",
		snap:      func() *snapshot.Snapshot { return withMinAllowed(res("1", "2Gi"), res("1", "2Gi")) },
		namespace: "ns",
		want:      map[string]Severity{"vpa-min-allowed": Context},
	},
	{
		name:      "VPA minAllowed below the uncapped target",
		snap:      func() *snapshot.Snapshot { return withMinAllowed(res("100m", "256Mi"), res("50m", "128Mi")) },
		namespace: "ns",
		notWant:   []string{"vpa-min-allowed"},
	},
	{
		name:      "enforced topology spread",
		snap:      func() *snapshot.Snapshot { return withSpread("DoNotSchedule") },
		namespace: "ns",
		want:      map[string]Severity{"spread-constraint": Context},
	},
	{
		name:      "best-effort topology spread",
		snap:      func() *snapshot.Snapshot { return withSpread("ScheduleAnyway") },
		namespace: "ns",
		notWant:   []string{"spread-constraint"},
	},
	{
		name: "NodePool that only consolidates empty nodes",
		snap: func() *snapshot.Snapshot {
			return inNodePool(snapshot.NodePoolDisruption{ConsolidationPolicy: "WhenEmpty"})
		},
		want: map[string]Severity{"karpenter-consolidation-policy": Blocker},
	},
	{
		name: "NodePool that consolidates underutilized nodes, with room in its budget",
		snap: func() *snapshot.Snapshot {
			return inNodePool(snapshot.NodePoolDisruption{ConsolidationPolicy: "WhenEmptyOrUnderutilized", ConsolidateAfter: "1m", Budgets: []snapshot.Budget{{Nodes: "1"}}})
		},
		notWant: []string{"karpenter-consolidation-policy", "karpenter-consolidation-disabled", "karpenter-budget"},
	},
	{
		name: "NodePool consolidateAfter Never",
		snap: func() *snapshot.Snapshot { return inNodePool(snapshot.NodePoolDisruption{ConsolidateAfter: "Never"}) },
		want: map[string]Severity{"karpenter-consolidation-disabled": Blocker},
	},
	{
		name: "NodePool budget of zero nodes",
		snap: func() *snapshot.Snapshot {
			return inNodePool(snapshot.NodePoolDisruption{Budgets: []snapshot.Budget{{Nodes: "0"}}})
		},
		want: map[string]Severity{"karpenter-budget": Blocker},
	},
	{
		name: "NodePool budget of zero nodes for drift only",
		snap: func() *snapshot.Snapshot {
			return inNodePool(snapshot.NodePoolDisruption{Budgets: []snapshot.Budget{{Nodes: "0", Reasons: []string{"Drifted"}}}})
		},
		notWant: []string{"karpenter-budget"},
	},
	{
		name: "NodeClaim marked not consolidatable",
		snap: func() *snapshot.Snapshot { return withNodeClaim("False") },
		want: map[string]Severity{"karpenter-not-consolidatable-yet": Context},
	},
	{
		name:    "NodeClaim marked consolidatable",
		snap:    func() *snapshot.Snapshot { return withNodeClaim("True") },
		notWant: []string{"karpenter-not-consolidatable-yet"},
	},
	{
		name: "Karpenter reports it cannot replace the node",
		snap: func() *snapshot.Snapshot { return withEvent("n1") },
		want: map[string]Severity{"autoscaler-event": PossibleBlocker},
	},
	{
		name:    "the same event about another node",
		snap:    func() *snapshot.Snapshot { return withEvent("n2") },
		notWant: []string{"autoscaler-event"},
	},
}

func TestRules(t *testing.T) {
	for _, tt := range ruleCases {
		t.Run(tt.name, func(t *testing.T) {
			findings := explainCase(t, tt)
			got := rulesOf(findings)
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
			for _, f := range findings {
				if len(f.Evidence) == 0 {
					t.Errorf("finding %s on %s has no evidence", f.Rule, f.Subject)
				}
			}
		})
	}
}

// explainCase explains node n1, or the case's namespace when it names one.
func explainCase(t *testing.T, c ruleCase) []Finding {
	t.Helper()
	if c.namespace != "" {
		r, err := ExplainNamespace(c.snap(), c.namespace)
		if err != nil {
			t.Fatal(err)
		}
		return r.Findings
	}
	r, err := ExplainNode(c.snap(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	return r.Findings
}

// TestEveryRuleHasCases fails when a catalog rule has no case in ruleCases
// where it fires, or none where it must not.
func TestEveryRuleHasCases(t *testing.T) {
	fires, silent := map[string]bool{}, map[string]bool{}
	for _, c := range ruleCases {
		for rule := range c.want {
			fires[rule] = true
		}
		for _, rule := range c.notWant {
			silent[rule] = true
		}
	}
	for _, r := range Catalog {
		if !fires[r.ID] {
			t.Errorf("rule %s has no case in ruleCases where it fires", r.ID)
		}
		if !silent[r.ID] {
			t.Errorf("rule %s has no case in ruleCases where it must not fire", r.ID)
		}
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
