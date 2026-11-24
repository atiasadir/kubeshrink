// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package snapshot

// Lookups are linear scans: snapshots are small and clarity matters more than speed here.

func (s *Snapshot) Node(name string) *Node {
	for i := range s.Nodes {
		if s.Nodes[i].Metadata.Name == name {
			return &s.Nodes[i]
		}
	}
	return nil
}

// PodsOnNode returns pods bound to the node that are not in a terminal phase.
func (s *Snapshot) PodsOnNode(name string) []*Pod {
	var out []*Pod
	for i := range s.Pods {
		p := &s.Pods[i]
		if p.Spec.NodeName == name && !IsTerminal(p) {
			out = append(out, p)
		}
	}
	return out
}

func IsTerminal(p *Pod) bool {
	return p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed"
}

func (s *Snapshot) PDBsFor(p *Pod) []*PodDisruptionBudget {
	var out []*PodDisruptionBudget
	for i := range s.PDBs {
		b := &s.PDBs[i]
		if b.Metadata.Namespace == p.Metadata.Namespace && b.Spec.Selector.Matches(p.Metadata.Labels) {
			out = append(out, b)
		}
	}
	return out
}

func (s *Snapshot) HPAsFor(kind, namespace, name string) []*HorizontalPodAutoscaler {
	var out []*HorizontalPodAutoscaler
	for i := range s.HPAs {
		h := &s.HPAs[i]
		t := h.Spec.ScaleTargetRef
		if h.Metadata.Namespace == namespace && t.Kind == kind && t.Name == name {
			out = append(out, h)
		}
	}
	return out
}

func (s *Snapshot) ScaledObjectsFor(kind, namespace, name string) []*ScaledObject {
	var out []*ScaledObject
	for i := range s.ScaledObjects {
		so := &s.ScaledObjects[i]
		t := so.Spec.ScaleTargetRef
		k := t.Kind
		if k == "" {
			k = "Deployment"
		}
		if so.Metadata.Namespace == namespace && k == kind && t.Name == name {
			out = append(out, so)
		}
	}
	return out
}

func (s *Snapshot) Workload(kind, namespace, name string) *Workload {
	for i := range s.Workloads {
		w := &s.Workloads[i]
		if w.Kind == kind && w.Metadata.Namespace == namespace && w.Metadata.Name == name {
			return w
		}
	}
	return nil
}
