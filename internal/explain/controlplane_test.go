// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"slices"
	"strings"
	"testing"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

const cpLabel = "node-role.kubernetes.io/control-plane"

// controlPlaneSnap has two workers (n1, n2) and a kubeadm-style control-plane
// node (cp): labelled, tainted NoSchedule, and running a static pod and a pod
// that tells Karpenter not to disrupt its node.
func controlPlaneSnap() *snapshot.Snapshot {
	s := base(
		pod("team", "a", "n1", "1", "4Gi"),
		pod("team", "b", "n2", "1", "4Gi"),
		pod("kube-system", "etcd-cp", "cp", "100m", "100Mi", owner("Node", "cp")),
		pod("kube-system", "pinned", "cp", "100m", "100Mi", annotate(annKarpenterDoNotDisrupt, "true")),
	)
	s.Nodes = append(s.Nodes, node("cp", "2", "8Gi", func(n *snapshot.Node) {
		n.Metadata.Labels[cpLabel] = ""
		n.Spec.Taints = []snapshot.Taint{{Key: cpLabel, Effect: "NoSchedule"}}
	}))
	return s
}

// A control-plane node next to workers is not autoscaled: it is managed by
// none and blocked by control-plane-node, its autoscaler-only findings are
// dropped, and its note does not blame --node-autoscaler.
func TestControlPlaneNodeIsNotAutoscaled(t *testing.T) {
	a := NewAnalyzer(controlPlaneSnap(), Options{})
	r, err := a.Node("cp")
	if err != nil {
		t.Fatal(err)
	}
	got := ruleSet(r)
	if r.ManagedBy != AutoscalerNone || r.Verdict != VerdictBlocked || got["control-plane-node"] != Blocker {
		t.Errorf("cp: managed by %q, verdict %q, findings %v; want none, blocked and control-plane-node", r.ManagedBy, r.Verdict, got)
	}
	if _, ok := got["karpenter-do-not-disrupt"]; ok {
		t.Errorf("cp keeps a Karpenter-only finding: %v", got)
	}
	notes := strings.Join(r.DataNotes, "\n")
	if !strings.Contains(notes, "cp is a control-plane node") || strings.Contains(notes, "--node-autoscaler none") {
		t.Errorf("cp notes:\n%s", notes)
	}
	w, err := a.Node("n1")
	if err != nil {
		t.Fatal(err)
	}
	if w.ManagedBy == AutoscalerNone || ruleSet(w)["control-plane-node"] != "" {
		t.Errorf("worker n1: managed by %q, findings %v", w.ManagedBy, ruleSet(w))
	}
}

// The cluster view counts the workers only and names the control-plane node
// it leaves out.
func TestClusterLeavesOutControlPlaneNodes(t *testing.T) {
	c := NewAnalyzer(controlPlaneSnap(), Options{}).Cluster()
	if c.Nodes != 2 || !slices.Equal(c.ControlPlaneNodes, []string{"cp"}) {
		t.Errorf("nodes %d, control plane %v; want 2 and [cp]", c.Nodes, c.ControlPlaneNodes)
	}
	if len(c.Pools) != 1 || c.Pools[0].Nodes != 2 || c.Allocatable.MilliCPU != 12000 {
		t.Errorf("pools %+v, allocatable %v; want the two workers and their 12 CPU", c.Pools, c.Allocatable)
	}
	counted := 0
	for _, n := range c.Verdicts {
		counted += n
	}
	if counted != 2 || c.ManagedBy[AutoscalerNone] != 0 {
		t.Errorf("verdicts %v, managed by %v; want the two workers only", c.Verdicts, c.ManagedBy)
	}
	for _, rs := range c.TopReasons {
		if slices.Contains(rs.Nodes, "cp") {
			t.Errorf("top reason %s counts cp", rs.Rule)
		}
	}
	for _, l := range c.Namespaces {
		if l.Namespace == "kube-system" {
			t.Errorf("kube-system runs only on cp but holds idle capacity: %+v", l)
		}
	}
	var b strings.Builder
	if err := WriteClusterText(&b, c); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 control-plane node(s) are not counted", "Not counted: 1 control-plane node(s), which no node autoscaler removes: cp"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("cluster text has no %q:\n%s", want, b.String())
		}
	}
}

// When every node is a control-plane node, as in a single-node kind or Docker
// Desktop cluster, they run the workloads and count like any other node.
func TestAllControlPlaneNodesCount(t *testing.T) {
	s := base(pod("team", "a", "n1", "1", "4Gi", annotate(annKarpenterDoNotDisrupt, "true")))
	for i := range s.Nodes {
		s.Nodes[i].Metadata.Labels[cpLabel] = ""
	}
	a := NewAnalyzer(s, Options{})
	c := a.Cluster()
	if c.Nodes != 2 || len(c.ControlPlaneNodes) != 0 || len(a.CountedNodes()) != 2 || strings.Contains(c.Summary, "control-plane") {
		t.Errorf("nodes %d, control plane %v, counted %d, summary %q; want every node counted", c.Nodes, c.ControlPlaneNodes, len(a.CountedNodes()), c.Summary)
	}
	r, err := a.Node("n1")
	if err != nil {
		t.Fatal(err)
	}
	if r.ManagedBy == AutoscalerNone || ruleSet(r)["karpenter-do-not-disrupt"] != Blocker {
		t.Errorf("n1: managed by %q, findings %v; want its findings kept", r.ManagedBy, ruleSet(r))
	}
}
