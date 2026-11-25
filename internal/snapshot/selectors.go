// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package snapshot

import "strconv"

type LabelSelector struct {
	MatchLabels      map[string]string          `json:"matchLabels,omitempty"`
	MatchExpressions []LabelSelectorRequirement `json:"matchExpressions,omitempty"`
}

type LabelSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

// Matches follows metav1 semantics: a nil selector matches nothing and an
// empty selector matches everything.
func (s *LabelSelector) Matches(labels map[string]string) bool {
	if s == nil {
		return false
	}
	for k, v := range s.MatchLabels {
		if lv, ok := labels[k]; !ok || lv != v {
			return false
		}
	}
	for _, e := range s.MatchExpressions {
		lv, ok := labels[e.Key]
		switch e.Operator {
		case "In":
			if !ok || !contains(e.Values, lv) {
				return false
			}
		case "NotIn":
			if ok && contains(e.Values, lv) {
				return false
			}
		case "Exists":
			if !ok {
				return false
			}
		case "DoesNotExist":
			if ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Matches reports whether the node satisfies the selector. Terms are ORed,
// requirements inside a term are ANDed, and an empty term matches nothing.
func (s *NodeSelector) Matches(n *Node) bool {
	for _, t := range s.NodeSelectorTerms {
		if t.Matches(n) {
			return true
		}
	}
	return false
}

// Matches reports whether the node satisfies every requirement of the term.
func (t NodeSelectorTerm) Matches(n *Node) bool {
	if len(t.MatchExpressions) == 0 && len(t.MatchFields) == 0 {
		return false
	}
	for _, r := range t.MatchExpressions {
		if !r.Matches(n.Metadata.Labels) {
			return false
		}
	}
	fields := map[string]string{"metadata.name": n.Metadata.Name}
	for _, r := range t.MatchFields {
		if !r.Matches(fields) {
			return false
		}
	}
	return true
}

// Matches evaluates one requirement. NotIn and DoesNotExist match a missing
// label; Gt and Lt compare integers.
func (r NodeSelectorRequirement) Matches(labels map[string]string) bool {
	v, ok := labels[r.Key]
	switch r.Operator {
	case "In":
		return ok && contains(r.Values, v)
	case "NotIn":
		return !ok || !contains(r.Values, v)
	case "Exists":
		return ok
	case "DoesNotExist":
		return !ok
	case "Gt", "Lt":
		if !ok || len(r.Values) != 1 {
			return false
		}
		have, err1 := strconv.ParseInt(v, 10, 64)
		want, err2 := strconv.ParseInt(r.Values[0], 10, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		if r.Operator == "Gt" {
			return have > want
		}
		return have < want
	}
	return false
}

// RequiredNodeAffinity returns the pod's required node affinity, if any.
func RequiredNodeAffinity(p *Pod) *NodeSelector {
	if a := p.Spec.Affinity; a != nil && a.NodeAffinity != nil {
		return a.NodeAffinity.Required
	}
	return nil
}

// RequiredPodAffinity returns the pod's hard rules for running near other pods.
func RequiredPodAffinity(p *Pod) []PodAffinityTerm {
	if a := p.Spec.Affinity; a != nil && a.PodAffinity != nil {
		return a.PodAffinity.Required
	}
	return nil
}

// RequiredPodAntiAffinity returns the pod's hard rules for staying away from other pods.
func RequiredPodAntiAffinity(p *Pod) []PodAffinityTerm {
	if a := p.Spec.Affinity; a != nil && a.PodAntiAffinity != nil {
		return a.PodAntiAffinity.Required
	}
	return nil
}

// HasPreferredScheduling reports whether the pod declares soft (preferred)
// affinity rules, which KubeShrink does not evaluate.
func HasPreferredScheduling(p *Pod) bool {
	a := p.Spec.Affinity
	if a == nil {
		return false
	}
	return (a.NodeAffinity != nil && HasValue(a.NodeAffinity.Preferred)) ||
		(a.PodAffinity != nil && HasValue(a.PodAffinity.Preferred)) ||
		(a.PodAntiAffinity != nil && HasValue(a.PodAntiAffinity.Preferred))
}

// MatchesNodeSelection reports whether the node passes the pod's nodeSelector
// and required node affinity.
func MatchesNodeSelection(p *Pod, n *Node) bool {
	for k, v := range p.Spec.NodeSelector {
		if lv, ok := n.Metadata.Labels[k]; !ok || lv != v {
			return false
		}
	}
	if s := RequiredNodeAffinity(p); s != nil && !s.Matches(n) {
		return false
	}
	return true
}

// SelectsPod reports whether an inter-pod term declared by owner selects
// other. A namespaceSelector is treated as selecting every namespace, because
// namespace labels are not part of the snapshot.
func (t PodAffinityTerm) SelectsPod(owner, other *Pod) bool {
	if !t.LabelSelector.Matches(other.Metadata.Labels) {
		return false
	}
	switch {
	case t.NamespaceSelector != nil:
		return true
	case len(t.Namespaces) == 0:
		return other.Metadata.Namespace == owner.Metadata.Namespace
	default:
		return contains(t.Namespaces, other.Metadata.Namespace)
	}
}

func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
