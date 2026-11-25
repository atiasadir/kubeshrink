// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"testing"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

func vpa(ns, kind, name, mode string, recs ...snapshot.ContainerRecommendation) snapshot.VerticalPodAutoscaler {
	v := snapshot.VerticalPodAutoscaler{
		Metadata: snapshot.ObjectMeta{Name: name, Namespace: ns},
		Spec:     snapshot.VPASpec{TargetRef: snapshot.CrossVersionObjectReference{Kind: kind, Name: name}},
	}
	if mode != "" {
		v.Spec.UpdatePolicy = &snapshot.VPAUpdatePolicy{UpdateMode: mode}
	}
	if len(recs) > 0 {
		v.Status.Recommendation = &snapshot.VPARecommendation{ContainerRecommendations: recs}
	}
	return v
}

func rec(target, upper map[string]string) snapshot.ContainerRecommendation {
	return snapshot.ContainerRecommendation{ContainerName: "c", Target: target, UpperBound: upper}
}

func res(cpu, mem string) map[string]string { return map[string]string{"cpu": cpu, "memory": mem} }

// sized is one pod of Deployment app (container c) on n1, with a VPA on the Deployment.
func sized(cpu, mem string, v snapshot.VerticalPodAutoscaler) *snapshot.Snapshot {
	s := base(pod("ns", "p", "n1", cpu, mem))
	s.VPAs = []snapshot.VerticalPodAutoscaler{v}
	return s
}

func nsRules(t *testing.T, s *snapshot.Snapshot) map[string]Finding {
	t.Helper()
	r, err := ExplainNamespace(s, "ns")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Finding{}
	for _, f := range r.Findings {
		out[f.Rule] = f
	}
	return out
}

func TestRequestsRules(t *testing.T) {
	cases := []struct {
		name string
		snap *snapshot.Snapshot
		rule string
		conf Confidence // empty: the rule must not fire
	}{
		{"above the upper bound", sized("1", "2Gi", vpa("ns", "Deployment", "app", "Auto", rec(res("200m", "512Mi"), res("400m", "1Gi")))), "requests-above-vpa", High},
		{"well above the target", sized("1", "2Gi", vpa("ns", "Deployment", "app", "Auto", rec(res("600m", "1Gi"), res("2", "4Gi")))), "requests-above-vpa", Medium},
		{"within 25% of the target", sized("1", "2Gi", vpa("ns", "Deployment", "app", "Auto", rec(res("900m", "1800Mi"), res("2", "4Gi")))), "requests-above-vpa", ""},
		{"no recommendation yet", sized("1", "2Gi", vpa("ns", "Deployment", "app", "Off")), "requests-above-vpa", ""},
		{"mode Off with a different target", sized("1", "2Gi", vpa("ns", "Deployment", "app", "Off", rec(res("200m", "512Mi"), nil))), "vpa-not-applied", High},
		{"mode Initial below the target", sized("100m", "128Mi", vpa("ns", "Deployment", "app", "Initial", rec(res("500m", "1Gi"), nil))), "vpa-not-applied", Medium},
		{"mode Auto applies its target", sized("1", "2Gi", vpa("ns", "Deployment", "app", "Auto", rec(res("200m", "512Mi"), nil))), "vpa-not-applied", ""},
	}
	for _, c := range cases {
		f, ok := nsRules(t, c.snap)[c.rule]
		switch {
		case c.conf == "" && ok:
			t.Errorf("%s: %s should not fire: %s", c.name, c.rule, f.Claim)
		case c.conf != "" && (!ok || f.Confidence != c.conf || f.Layer != LayerRequests || f.Severity != Warning):
			t.Errorf("%s: %s = %+v, want a %s-confidence warning in the requests layer", c.name, c.rule, f, c.conf)
		}
	}
}

func TestVPAHPAConflict(t *testing.T) {
	mk := func(mode string, policy *snapshot.VPAResourcePolicy) *snapshot.Snapshot {
		v := vpa("ns", "Deployment", "app", mode, rec(res("1", "2Gi"), nil))
		v.Spec.ResourcePolicy = policy
		s := sized("1", "2Gi", v)
		h := hpa("ns", "app", "app", 1)
		h.Spec.Metrics = []snapshot.HPAMetric{{Type: "Resource", Resource: &snapshot.HPAResourceSource{Name: "cpu"}}}
		s.HPAs = []snapshot.HorizontalPodAutoscaler{h}
		return s
	}
	if _, ok := nsRules(t, mk("Auto", nil))["vpa-hpa-conflict"]; !ok {
		t.Error("a VPA in Auto and an HPA on CPU should be reported")
	}
	if _, ok := nsRules(t, mk("Off", nil))["vpa-hpa-conflict"]; ok {
		t.Error("a VPA in mode Off changes nothing")
	}
	memoryOnly := &snapshot.VPAResourcePolicy{ContainerPolicies: []snapshot.VPAContainerPolicy{{ContainerName: "*", ControlledResources: []string{"memory"}}}}
	if _, ok := nsRules(t, mk("Auto", memoryOnly))["vpa-hpa-conflict"]; ok {
		t.Error("a VPA that only controls memory does not move a CPU-based HPA")
	}
}

func TestVPAMinAllowed(t *testing.T) {
	v := vpa("ns", "Deployment", "app", "Auto", snapshot.ContainerRecommendation{ContainerName: "c", Target: res("1", "2Gi"), UncappedTarget: res("100m", "256Mi")})
	v.Spec.ResourcePolicy = &snapshot.VPAResourcePolicy{ContainerPolicies: []snapshot.VPAContainerPolicy{{ContainerName: "c", MinAllowed: res("1", "2Gi")}}}
	f, ok := nsRules(t, sized("1", "2Gi", v))["vpa-min-allowed"]
	if !ok || f.Severity != Context || len(f.Evidence) != 4 {
		t.Errorf("vpa-min-allowed = %+v, want context citing minAllowed and the uncapped target for cpu and memory", f)
	}
}

func TestNodesNeededAtVPATargets(t *testing.T) {
	s := base(pod("ns", "a", "n1", "3", "1Gi"), pod("ns", "b", "n2", "6", "1Gi"))
	s.VPAs = []snapshot.VerticalPodAutoscaler{vpa("ns", "Deployment", "app", "Off", rec(res("500m", "512Mi"), nil))}
	r := ExplainCluster(s)
	if r.NodesNeeded != 2 || r.Size.NodesNeededAtTargets != 1 || r.Size.ContainersWithTargets != 2 {
		t.Errorf("needed %d, at targets %d, containers %d; want 2, 1, 2", r.NodesNeeded, r.Size.NodesNeededAtTargets, r.Size.ContainersWithTargets)
	}
	if got := shortAmount(r.Size.AboveTargets); got != "8.0 CPU, 1.0Gi" {
		t.Errorf("above targets = %s, want 8.0 CPU, 1.0Gi", got)
	}
	if want := (VPACoverage{Workloads: 1, WithVPA: 1, WithRecommendation: 1, Oversized: 1}); r.Size.VPA != want {
		t.Errorf("coverage = %+v, want %+v", r.Size.VPA, want)
	}
	if s.Pods[0].Spec.Containers[0].Resources.Requests["cpu"] != "3" {
		t.Error("the estimate must not change the snapshot")
	}
}

func TestTeamExampleRequests(t *testing.T) {
	a := NewAnalyzer(teamSnapshot(t), Options{})
	for ns, rule := range map[string]string{"checkout": "vpa-not-applied", "reports": "vpa-hpa-conflict", "ml": "vpa-min-allowed"} {
		r, err := a.Namespace(ns)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range r.Findings {
			found = found || (f.Rule == rule && f.Layer == LayerRequests)
		}
		if !found {
			t.Errorf("%s: missing %s", ns, rule)
		}
	}
	c := a.Cluster()
	if v := c.Size.VPA; v.Workloads != 3 || v.WithRecommendation != 3 || v.Oversized != 1 || c.Size.ContainersWithTargets != 6 || c.Size.NodesNeededAtTargets != 2 {
		t.Errorf("size = %+v", c.Size)
	}
}
