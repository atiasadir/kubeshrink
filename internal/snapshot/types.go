// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Package snapshot holds the Kubernetes objects KubeShrink reasons about.
//
// It deliberately models only the fields KubeShrink's rules read, instead of
// importing the full Kubernetes API types. Unknown fields are ignored. The same
// structures are filled from offline files or from a live, read-only cluster.
package snapshot

import "encoding/json"

type ObjectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
	OwnerReferences []OwnerReference  `json:"ownerReferences,omitempty"`
}

type OwnerReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller *bool  `json:"controller,omitempty"`
}

// ControllerRef returns the owner reference marked controller=true, if any.
func (m *ObjectMeta) ControllerRef() *OwnerReference {
	for i := range m.OwnerReferences {
		if c := m.OwnerReferences[i].Controller; c != nil && *c {
			return &m.OwnerReferences[i]
		}
	}
	return nil
}

// Ref renders an object reference such as "Pod/shop/web-1" or "Node/node-a".
func Ref(kind, namespace, name string) string {
	if namespace == "" {
		return kind + "/" + name
	}
	return kind + "/" + namespace + "/" + name
}

// HasValue reports whether a raw JSON field is present and not empty.
func HasValue(r json.RawMessage) bool {
	switch string(r) {
	case "", "null", "{}", "[]":
		return false
	}
	return true
}

type Node struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     NodeSpec   `json:"spec"`
	Status   NodeStatus `json:"status"`
}

type NodeSpec struct {
	Unschedulable bool    `json:"unschedulable,omitempty"`
	Taints        []Taint `json:"taints,omitempty"`
}

type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

type NodeStatus struct {
	Allocatable map[string]string `json:"allocatable,omitempty"`
}

type Pod struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   PodStatus  `json:"status"`
}

type PodSpec struct {
	NodeName                  string            `json:"nodeName,omitempty"`
	NodeSelector              map[string]string `json:"nodeSelector,omitempty"`
	Tolerations               []Toleration      `json:"tolerations,omitempty"`
	Containers                []Container       `json:"containers,omitempty"`
	Volumes                   []Volume          `json:"volumes,omitempty"`
	Affinity                  json.RawMessage   `json:"affinity,omitempty"`
	TopologySpreadConstraints json.RawMessage   `json:"topologySpreadConstraints,omitempty"`
}

type Toleration struct {
	Key      string `json:"key,omitempty"`
	Operator string `json:"operator,omitempty"`
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect,omitempty"`
}

type Container struct {
	Name      string               `json:"name"`
	Resources ResourceRequirements `json:"resources"`
}

type ResourceRequirements struct {
	Requests map[string]string `json:"requests,omitempty"`
}

type Volume struct {
	Name     string          `json:"name"`
	EmptyDir json.RawMessage `json:"emptyDir,omitempty"`
	HostPath *HostPath       `json:"hostPath,omitempty"`
}

type HostPath struct {
	Path string `json:"path"`
}

type PodStatus struct {
	Phase string `json:"phase,omitempty"`
}

type PodDisruptionBudget struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PDBSpec    `json:"spec"`
	Status   PDBStatus  `json:"status"`
}

type PDBSpec struct {
	Selector       *LabelSelector  `json:"selector,omitempty"`
	MinAvailable   json.RawMessage `json:"minAvailable,omitempty"`
	MaxUnavailable json.RawMessage `json:"maxUnavailable,omitempty"`
}

type PDBStatus struct {
	// DisruptionsAllowed is a pointer so a missing status is not mistaken for 0.
	DisruptionsAllowed *int32 `json:"disruptionsAllowed,omitempty"`
	CurrentHealthy     int32  `json:"currentHealthy,omitempty"`
	DesiredHealthy     int32  `json:"desiredHealthy,omitempty"`
	ExpectedPods       int32  `json:"expectedPods,omitempty"`
}

type LabelSelector struct {
	MatchLabels      map[string]string          `json:"matchLabels,omitempty"`
	MatchExpressions []LabelSelectorRequirement `json:"matchExpressions,omitempty"`
}

type LabelSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

// Matches follows policy/v1 semantics: a nil selector matches nothing and an
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

func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

type CrossVersionObjectReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	APIVersion string `json:"apiVersion,omitempty"`
}

type HorizontalPodAutoscaler struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     HPASpec    `json:"spec"`
	Status   HPAStatus  `json:"status"`
}

type HPASpec struct {
	ScaleTargetRef CrossVersionObjectReference `json:"scaleTargetRef"`
	MinReplicas    *int32                      `json:"minReplicas,omitempty"`
	MaxReplicas    int32                       `json:"maxReplicas"`
}

type HPAStatus struct {
	CurrentReplicas int32 `json:"currentReplicas,omitempty"`
	DesiredReplicas int32 `json:"desiredReplicas,omitempty"`
}

// ScaledObject is a KEDA keda.sh/v1alpha1 ScaledObject.
type ScaledObject struct {
	Metadata ObjectMeta       `json:"metadata"`
	Spec     ScaledObjectSpec `json:"spec"`
}

type ScaledObjectSpec struct {
	// ScaleTargetRef.Kind defaults to Deployment when empty.
	ScaleTargetRef   CrossVersionObjectReference `json:"scaleTargetRef"`
	MinReplicaCount  *int32                      `json:"minReplicaCount,omitempty"`
	MaxReplicaCount  *int32                      `json:"maxReplicaCount,omitempty"`
	IdleReplicaCount *int32                      `json:"idleReplicaCount,omitempty"`
}

type VerticalPodAutoscaler struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     VPASpec    `json:"spec"`
}

type VPASpec struct {
	TargetRef    CrossVersionObjectReference `json:"targetRef"`
	UpdatePolicy *VPAUpdatePolicy            `json:"updatePolicy,omitempty"`
}

type VPAUpdatePolicy struct {
	UpdateMode string `json:"updateMode,omitempty"`
}

// Workload covers Deployments, StatefulSets and ReplicaSets.
type Workload struct {
	Kind     string         `json:"kind"`
	Metadata ObjectMeta     `json:"metadata"`
	Spec     WorkloadSpec   `json:"spec"`
	Status   WorkloadStatus `json:"status"`
}

type WorkloadSpec struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

type WorkloadStatus struct {
	Replicas int32 `json:"replicas,omitempty"`
}

// Event is a core/v1 Event.
type Event struct {
	Metadata           ObjectMeta      `json:"metadata"`
	InvolvedObject     ObjectReference `json:"involvedObject"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
	Type               string          `json:"type,omitempty"`
	Count              int32           `json:"count,omitempty"`
	FirstTimestamp     string          `json:"firstTimestamp,omitempty"`
	LastTimestamp      string          `json:"lastTimestamp,omitempty"`
	Source             EventSource     `json:"source"`
	ReportingComponent string          `json:"reportingComponent,omitempty"`
}

type ObjectReference struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type EventSource struct {
	Component string `json:"component,omitempty"`
}
