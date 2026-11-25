// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"sort"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// controlPlaneApart returns the control-plane nodes, which the analyzer sets
// apart from the others. No node autoscaler removes them: Cluster Autoscaler
// only scales the node groups it is given, and Karpenter only the nodes it
// launched. So they are reported as not autoscaled, and the cluster's counts
// and metrics leave them out. When every node is a control-plane node, as in a
// single-node kind, minikube or Docker Desktop cluster, they run the workloads
// and count like any other node, so it returns none.
func controlPlaneApart(s *snapshot.Snapshot) map[string]bool {
	cp := map[string]bool{}
	for i := range s.Nodes {
		if snapshot.IsControlPlane(&s.Nodes[i]) {
			cp[s.Nodes[i].Metadata.Name] = true
		}
	}
	if len(cp) == len(s.Nodes) {
		return map[string]bool{}
	}
	return cp
}

// counted reports whether the cluster's counts include the node.
func (a *Analyzer) counted(n *snapshot.Node) bool { return !a.controlPlane[n.Metadata.Name] }

// CountedNodes explains, in name order, the nodes that the cluster's counts
// and metrics include: every node but the control-plane nodes set apart.
func (a *Analyzer) CountedNodes() []*NodeReport {
	all := a.Nodes()
	out := make([]*NodeReport, 0, len(all))
	for _, r := range all {
		if !a.controlPlane[r.Node] {
			out = append(out, r)
		}
	}
	return out
}

// ControlPlaneNodes lists, in name order, the control-plane nodes that the
// cluster's counts and metrics leave out.
func (a *Analyzer) ControlPlaneNodes() []string {
	out := make([]string, 0, len(a.controlPlane))
	for n := range a.controlPlane {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// checkControlPlane reports a control-plane node that the analyzer sets apart.
func checkControlPlane(c *nodeContext) []Finding {
	if !c.controlPlane {
		return nil
	}
	ref := nodeRef(c.node)
	label := snapshot.ControlPlaneLabel(c.node)
	return []Finding{{
		Rule: "control-plane-node", Severity: Blocker, Subject: ref, Confidence: High,
		Claim:    "The node is labelled " + label + ": it runs the control plane, which no node autoscaler removes.",
		Meaning:  "Cluster Autoscaler only scales the node groups it is given and Karpenter only the nodes it launched, so this node stays whatever runs on it. The cluster's counts and metrics leave it out.",
		Evidence: []Evidence{{Object: ref, Field: labelField(label), Value: c.node.Metadata.Labels[label]}},
	}}
}
