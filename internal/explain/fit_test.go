// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"strings"
	"testing"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

func hostnames(s *snapshot.Snapshot) {
	for i := range s.Nodes {
		s.Nodes[i].Metadata.Labels["kubernetes.io/hostname"] = s.Nodes[i].Metadata.Name
	}
}

func cordoned(name string) snapshot.Node {
	n := node(name, "8", "32Gi")
	n.Spec.Unschedulable = true
	return n
}

func term(key, value, topology string) snapshot.PodAffinityTerm {
	return snapshot.PodAffinityTerm{LabelSelector: &snapshot.LabelSelector{MatchLabels: map[string]string{key: value}}, TopologyKey: topology}
}

// TestFitChecks drains n1, whose pod p must find a place on n2.
func TestFitChecks(t *testing.T) {
	cases := []struct {
		name string
		snap func() *snapshot.Snapshot
		want string // part of the no-room explanation; "" means p fits on n2
	}{
		{"required node affinity", func() *snapshot.Snapshot {
			s := base(pod("ns", "p", "n1", "100m", "128Mi", func(p *snapshot.Pod) {
				req := []snapshot.NodeSelectorRequirement{{Key: "disk", Operator: "In", Values: []string{"ssd"}}}
				p.Spec.Affinity = &snapshot.Affinity{NodeAffinity: &snapshot.NodeAffinity{Required: &snapshot.NodeSelector{NodeSelectorTerms: []snapshot.NodeSelectorTerm{{MatchExpressions: req}}}}}
			}))
			s.Nodes[0].Metadata.Labels["disk"] = "ssd"
			return s
		}, "node affinity on 1 node(s)"},
		{"host port in use", func() *snapshot.Snapshot {
			port := func(p *snapshot.Pod) {
				p.Spec.Containers[0].Ports = []snapshot.ContainerPort{{ContainerPort: 80, HostPort: 8080}}
			}
			return base(pod("ns", "p", "n1", "100m", "128Mi", port), pod("other", "q", "n2", "100m", "128Mi", port))
		}, "host port on 1 node(s)"},
		{"enforced topology spread", func() *snapshot.Snapshot {
			spread := func(p *snapshot.Pod) {
				p.Spec.TopologySpreadConstraints = []snapshot.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: "zone", WhenUnsatisfiable: "DoNotSchedule", LabelSelector: &snapshot.LabelSelector{MatchLabels: map[string]string{"app": "app"}}}}
			}
			s := base(pod("ns", "p", "n1", "100m", "128Mi", spread), pod("ns", "q", "n2", "100m", "128Mi", spread))
			s.Nodes = append(s.Nodes, cordoned("n3")) // an empty zone still counts
			for i, z := range []string{"a", "b", "c"} {
				s.Nodes[i].Metadata.Labels["zone"] = z
			}
			return s
		}, "topology spread on 1 node(s)"},
		{"required pod affinity", func() *snapshot.Snapshot {
			s := base(pod("ns", "p", "n1", "100m", "128Mi", func(p *snapshot.Pod) {
				p.Spec.Affinity = &snapshot.Affinity{PodAffinity: &snapshot.PodAffinity{Required: []snapshot.PodAffinityTerm{term("role", "cache", "kubernetes.io/hostname")}}}
			}), pod("ns", "cache", "n3", "100m", "128Mi", func(p *snapshot.Pod) {
				p.Metadata.Labels = map[string]string{"role": "cache"}
			}))
			s.Nodes = append(s.Nodes, cordoned("n3"))
			hostnames(s)
			return s
		}, "pod affinity on 1 node(s)"},
		{"anti-affinity of a pod already there", func() *snapshot.Snapshot {
			s := base(pod("ns", "p", "n1", "100m", "128Mi"), pod("ns", "q", "n2", "100m", "128Mi", func(q *snapshot.Pod) {
				q.Metadata.Labels = map[string]string{"app": "other"}
				q.Spec.Affinity = &snapshot.Affinity{PodAntiAffinity: &snapshot.PodAffinity{Required: []snapshot.PodAffinityTerm{term("app", "app", "kubernetes.io/hostname")}}}
			}))
			hostnames(s)
			return s
		}, "pod anti-affinity on 1 node(s)"},
		{"nothing in the way", func() *snapshot.Snapshot {
			s := base(pod("ns", "p", "n1", "100m", "128Mi"))
			hostnames(s)
			return s
		}, ""},
	}
	for _, c := range cases {
		r, err := ExplainNode(c.snap(), "n1")
		if err != nil {
			t.Fatal(err)
		}
		meaning, found := "", false
		for _, f := range r.Findings {
			if f.Rule == "no-room-elsewhere" {
				meaning, found = f.Meaning, true
			}
		}
		switch {
		case c.want == "" && found:
			t.Errorf("%s: p should fit on n2, got %q", c.name, meaning)
		case c.want != "" && !strings.Contains(meaning, c.want):
			t.Errorf("%s: no-room explanation %q, want it to mention %q", c.name, meaning, c.want)
		}
	}
}
