// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package snapshot

import "sort"

// AnnotationMirrorPod marks static pods that the kubelet runs from a manifest.
const AnnotationMirrorPod = "kubernetes.io/config.mirror"

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

// PodsInNamespace returns the namespace's pods that are not in a terminal
// phase, including pending pods that are not on a node yet.
func (s *Snapshot) PodsInNamespace(ns string) []*Pod {
	var out []*Pod
	for i := range s.Pods {
		p := &s.Pods[i]
		if p.Metadata.Namespace == ns && !IsTerminal(p) {
			out = append(out, p)
		}
	}
	return out
}

// Namespaces returns, in name order, every namespace with a pod that is not
// in a terminal phase.
func (s *Snapshot) Namespaces() []string {
	seen := map[string]bool{}
	var out []string
	for i := range s.Pods {
		p := &s.Pods[i]
		if ns := p.Metadata.Namespace; !IsTerminal(p) && !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	return out
}

func IsTerminal(p *Pod) bool {
	return p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed"
}

// IsDaemonSetPod reports whether a DaemonSet controls the pod.
func IsDaemonSetPod(p *Pod) bool {
	cr := p.Metadata.ControllerRef()
	return cr != nil && cr.Kind == "DaemonSet"
}

// IsMirrorPod reports whether the kubelet runs the pod from a static manifest.
func IsMirrorPod(p *Pod) bool {
	if _, ok := p.Metadata.Annotations[AnnotationMirrorPod]; ok {
		return true
	}
	cr := p.Metadata.ControllerRef()
	return cr != nil && cr.Kind == "Node"
}

// IsNodeBound reports whether the pod lives and dies with its node, as
// DaemonSet and static pods do, so it never needs room elsewhere.
func IsNodeBound(p *Pod) bool { return IsDaemonSetPod(p) || IsMirrorPod(p) }

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

// VPAsFor returns the VPAs whose targetRef is the workload.
func (s *Snapshot) VPAsFor(kind, namespace, name string) []*VerticalPodAutoscaler {
	var out []*VerticalPodAutoscaler
	for i := range s.VPAs {
		v := &s.VPAs[i]
		t := v.Spec.TargetRef
		if v.Metadata.Namespace == namespace && t.Kind == kind && t.Name == name {
			out = append(out, v)
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

// LimitRangesIn returns the namespace's LimitRanges.
func (s *Snapshot) LimitRangesIn(ns string) []*LimitRange {
	var out []*LimitRange
	for i := range s.LimitRanges {
		if s.LimitRanges[i].Metadata.Namespace == ns {
			out = append(out, &s.LimitRanges[i])
		}
	}
	return out
}

// NodePool returns the Karpenter NodePool with the given name.
func (s *Snapshot) NodePool(name string) *NodePool {
	for i := range s.NodePools {
		if s.NodePools[i].Metadata.Name == name {
			return &s.NodePools[i]
		}
	}
	return nil
}

// NodeClaimForNode returns the Karpenter NodeClaim that launched the node.
func (s *Snapshot) NodeClaimForNode(node string) *NodeClaim {
	for i := range s.NodeClaims {
		if s.NodeClaims[i].Status.NodeName == node {
			return &s.NodeClaims[i]
		}
	}
	return nil
}
