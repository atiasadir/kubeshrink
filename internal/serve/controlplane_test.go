// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package serve

import (
	"testing"

	"github.com/atiasadir/kubeshrink/internal/explain"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

func cpTestNode(name string, labels map[string]string) snapshot.Node {
	return snapshot.Node{
		Metadata: snapshot.ObjectMeta{Name: name, Labels: labels},
		Status:   snapshot.NodeStatus{Allocatable: map[string]string{"cpu": "4", "memory": "16Gi", "pods": "110"}},
	}
}

// kubeshrink_nodes and kubeshrink_blocked_nodes leave control-plane nodes out,
// unless every node is one.
func TestObserveLeavesOutControlPlaneNodes(t *testing.T) {
	cp := map[string]string{"node-role.kubernetes.io/control-plane": ""}
	for _, c := range []struct {
		name  string
		nodes []snapshot.Node
		want  float64
	}{
		{"with workers", []snapshot.Node{cpTestNode("cp", cp), cpTestNode("w1", nil), cpTestNode("w2", nil)}, 2},
		{"control plane only", []snapshot.Node{cpTestNode("cp", cp)}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &snapshot.Snapshot{Nodes: c.nodes, Skipped: map[string]int{}}
			o := observe(s, explain.NewAnalyzer(s, explain.Options{}), DefaultPoolLabels)
			total := 0.0
			for _, v := range o.nodes {
				total += v
			}
			if total != c.want {
				t.Errorf("kubeshrink_nodes adds up to %v, want %v: %v", total, c.want, o.nodes)
			}
			for k := range o.blocked {
				if k[0] == "control-plane-node" {
					t.Errorf("kubeshrink_blocked_nodes has %v", k)
				}
			}
		})
	}
}
