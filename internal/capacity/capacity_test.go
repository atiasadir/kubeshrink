// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package capacity

import (
	"math"
	"testing"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

const gi = float64(1 << 30)

func yes() *bool { b := true; return &b }

func node(name, cpu, mem string) snapshot.Node {
	return snapshot.Node{
		Metadata: snapshot.ObjectMeta{Name: name, Labels: map[string]string{"karpenter.sh/nodepool": "general"}},
		Status:   snapshot.NodeStatus{Allocatable: map[string]string{"cpu": cpu, "memory": mem, "pods": "110"}},
	}
}

func pod(ns, name, nodeName, cpu, mem, ownerKind string) snapshot.Pod {
	p := snapshot.Pod{
		Metadata: snapshot.ObjectMeta{Name: name, Namespace: ns},
		Spec: snapshot.PodSpec{NodeName: nodeName, Containers: []snapshot.Container{
			{Name: "c", Resources: snapshot.ResourceRequirements{Requests: map[string]string{"cpu": cpu, "memory": mem}}},
		}},
		Status: snapshot.PodStatus{Phase: "Running"},
	}
	p.Metadata.OwnerReferences = []snapshot.OwnerReference{{Kind: ownerKind, Name: name + "-owner", Controller: yes()}}
	return p
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAttributeSharesIdleByRequests(t *testing.T) {
	s := &snapshot.Snapshot{
		Nodes: []snapshot.Node{node("n1", "4", "8Gi"), node("n2", "4", "8Gi")},
		Pods: []snapshot.Pod{
			pod("a", "a1", "n1", "1", "1Gi", "ReplicaSet"),
			pod("b", "b1", "n1", "1", "3Gi", "ReplicaSet"),
			pod("a", "a2", "n2", "500m", "1Gi", "ReplicaSet"),
		},
	}
	att := Attribute(s)
	a, b := att.Namespaces["a"], att.Namespaces["b"]
	// n1 has 2 CPU and 4Gi idle, split 1:1 and 1:3 by requests. All of n2's idle
	// capacity (3.5 CPU, 7Gi) is a's.
	if !near(a.Idle.MilliCPU, 4500) || !near(b.Idle.MilliCPU, 1000) {
		t.Errorf("CPU idle shares: a=%v b=%v", a.Idle.MilliCPU, b.Idle.MilliCPU)
	}
	if !near(a.Idle.MemoryBytes, 8*gi) || !near(b.Idle.MemoryBytes, 3*gi) {
		t.Errorf("memory idle shares: a=%vGi b=%vGi", a.Idle.MemoryBytes/gi, b.Idle.MemoryBytes/gi)
	}
	if cpu, mem := a.Packing(); !near(cpu, 0.25) || !near(mem, 0.2) {
		t.Errorf("a packing: cpu=%v memory=%v, want 0.25 and 0.2", cpu, mem)
	}
	if len(a.Shares) != 2 || a.Shares[0].Exclusive || !a.Shares[1].Exclusive {
		t.Errorf("a should share n1 and be alone on n2: %+v", a.Shares)
	}
	if a.Pods != 2 || b.Pods != 1 {
		t.Errorf("pods: a=%d b=%d", a.Pods, b.Pods)
	}
	if order := att.NamespacesByIdle(); order[0].Namespace != "a" {
		t.Errorf("a holds the most idle capacity, got %s first", order[0].Namespace)
	}
}

func TestAttributeKeepsEmptyNodesUnattributed(t *testing.T) {
	s := &snapshot.Snapshot{
		Nodes: []snapshot.Node{node("n1", "4", "8Gi"), node("empty", "2", "4Gi")},
		Pods:  []snapshot.Pod{pod("a", "a1", "n1", "1", "1Gi", "ReplicaSet")},
	}
	att := Attribute(s)
	if !near(att.Unattributed.MilliCPU, 2000) || !near(att.Unattributed.MemoryBytes, 4*gi) {
		t.Errorf("Unattributed = %+v", att.Unattributed)
	}
	pools := att.Pools()
	if len(pools) != 1 || pools[0].Nodes != 2 || pools[0].EmptyNodes != 1 || pools[0].MinNodes != 1 {
		t.Errorf("pools = %+v", pools)
	}
}

func TestMinNodesKeepsNodeBoundPods(t *testing.T) {
	cluster := func(workload ...snapshot.Pod) *snapshot.Snapshot {
		s := &snapshot.Snapshot{}
		for _, n := range []string{"n1", "n2", "n3"} {
			s.Nodes = append(s.Nodes, node(n, "4", "16Gi"))
			s.Pods = append(s.Pods, pod("monitoring", "exporter-"+n, n, "100m", "128Mi", "DaemonSet"))
		}
		s.Pods = append(s.Pods, workload...)
		return s
	}
	// Each node keeps 100m for its DaemonSet pod, leaving 3.9 CPU of room.
	tests := []struct {
		name string
		s    *snapshot.Snapshot
		want int
	}{
		{"fits beside one node's DaemonSet pod", cluster(pod("a", "a1", "n1", "3900m", "1Gi", "ReplicaSet")), 1},
		{"needs a second node", cluster(pod("a", "a1", "n1", "2", "1Gi", "ReplicaSet"), pod("a", "a2", "n2", "2", "1Gi", "ReplicaSet")), 2},
		{"only DaemonSet pods", cluster(), 0},
	}
	for _, tt := range tests {
		if got := Attribute(tt.s).Pools()[0].MinNodes; got != tt.want {
			t.Errorf("%s: MinNodes = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestPoolOf(t *testing.T) {
	n := &snapshot.Node{Metadata: snapshot.ObjectMeta{Labels: map[string]string{"agentpool": "legacy", "karpenter.sh/nodepool": "general"}}}
	if pool, label := PoolOf(n); pool != "general" || label != "karpenter.sh/nodepool" {
		t.Errorf("PoolOf = %s from %s, want the Karpenter NodePool", pool, label)
	}
	if pool, _ := PoolOf(&snapshot.Node{}); pool != NoPool {
		t.Errorf("PoolOf(unlabeled) = %s", pool)
	}
}

// AttributeNodes leaves out the nodes keep rejects, with their pods.
func TestAttributeNodesLeavesOutNodes(t *testing.T) {
	s := &snapshot.Snapshot{
		Nodes: []snapshot.Node{node("a", "4", "16Gi"), node("b", "4", "16Gi")},
		Pods:  []snapshot.Pod{pod("team", "p1", "a", "1", "1Gi", "ReplicaSet"), pod("team", "p2", "b", "2", "2Gi", "ReplicaSet")},
	}
	att := AttributeNodes(s, func(n *snapshot.Node) bool { return n.Metadata.Name != "b" })
	alloc, req := att.Totals()
	if len(att.Nodes) != 1 || !near(alloc.MilliCPU, 4000) || !near(req.MilliCPU, 1000) {
		t.Errorf("nodes %d, allocatable %v, requested %v; want node a only", len(att.Nodes), alloc, req)
	}
	if u := att.Namespaces["team"]; u == nil || u.Pods != 1 || !near(u.Requested.MilliCPU, 1000) {
		t.Errorf("team = %+v, want only the pod on a", u)
	}
	if n := len(Attribute(s).Nodes); n != 2 {
		t.Errorf("Attribute measured %d node(s), want 2", n)
	}
}
