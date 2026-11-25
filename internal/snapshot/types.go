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

// String renders the taint the way kubectl does: key=value:effect.
func (t Taint) String() string {
	if t.Value == "" {
		return t.Key + ":" + t.Effect
	}
	return t.Key + "=" + t.Value + ":" + t.Effect
}

// Restricts reports whether the taint keeps pods that do not tolerate it off
// the node. PreferNoSchedule is only a preference.
func (t Taint) Restricts() bool {
	return t.Effect == "NoSchedule" || t.Effect == "NoExecute"
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
	NodeName                  string                     `json:"nodeName,omitempty"`
	NodeSelector              map[string]string          `json:"nodeSelector,omitempty"`
	Tolerations               []Toleration               `json:"tolerations,omitempty"`
	Affinity                  *Affinity                  `json:"affinity,omitempty"`
	TopologySpreadConstraints []TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	InitContainers            []Container                `json:"initContainers,omitempty"`
	Containers                []Container                `json:"containers,omitempty"`
	Overhead                  map[string]string          `json:"overhead,omitempty"`
	Volumes                   []Volume                   `json:"volumes,omitempty"`
}

type Toleration struct {
	Key      string `json:"key,omitempty"`
	Operator string `json:"operator,omitempty"`
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect,omitempty"`
}

// Tolerates reports whether the toleration matches the taint. An empty
// operator means Equal, and an empty effect matches every effect.
func (t Toleration) Tolerates(taint Taint) bool {
	if t.Effect != "" && t.Effect != taint.Effect {
		return false
	}
	if t.Key == "" {
		return t.Operator == "Exists"
	}
	if t.Key != taint.Key {
		return false
	}
	return t.Operator == "Exists" || t.Value == taint.Value
}

// Tolerates reports whether any of the pod's tolerations matches the taint.
func (p *Pod) Tolerates(taint Taint) bool {
	for _, t := range p.Spec.Tolerations {
		if t.Tolerates(taint) {
			return true
		}
	}
	return false
}

type Container struct {
	Name          string               `json:"name"`
	Resources     ResourceRequirements `json:"resources"`
	RestartPolicy string               `json:"restartPolicy,omitempty"`
	Ports         []ContainerPort      `json:"ports,omitempty"`
}

// IsSidecar reports whether an init container keeps running next to the app
// containers (a native sidecar, restartPolicy: Always).
func (c Container) IsSidecar() bool { return c.RestartPolicy == "Always" }

type ContainerPort struct {
	ContainerPort int32  `json:"containerPort"`
	HostPort      int32  `json:"hostPort,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
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

// Affinity holds the scheduling rules KubeShrink evaluates. Preferred (soft)
// rules are kept as raw JSON, only to report that they exist.
type Affinity struct {
	NodeAffinity    *NodeAffinity `json:"nodeAffinity,omitempty"`
	PodAffinity     *PodAffinity  `json:"podAffinity,omitempty"`
	PodAntiAffinity *PodAffinity  `json:"podAntiAffinity,omitempty"`
}

type NodeAffinity struct {
	Required  *NodeSelector   `json:"requiredDuringSchedulingIgnoredDuringExecution,omitempty"`
	Preferred json.RawMessage `json:"preferredDuringSchedulingIgnoredDuringExecution,omitempty"`
}

type NodeSelector struct {
	NodeSelectorTerms []NodeSelectorTerm `json:"nodeSelectorTerms"`
}

type NodeSelectorTerm struct {
	MatchExpressions []NodeSelectorRequirement `json:"matchExpressions,omitempty"`
	MatchFields      []NodeSelectorRequirement `json:"matchFields,omitempty"`
}

type NodeSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

// PodAffinity is used for both podAffinity and podAntiAffinity.
type PodAffinity struct {
	Required  []PodAffinityTerm `json:"requiredDuringSchedulingIgnoredDuringExecution,omitempty"`
	Preferred json.RawMessage   `json:"preferredDuringSchedulingIgnoredDuringExecution,omitempty"`
}

type PodAffinityTerm struct {
	LabelSelector     *LabelSelector `json:"labelSelector,omitempty"`
	Namespaces        []string       `json:"namespaces,omitempty"`
	NamespaceSelector *LabelSelector `json:"namespaceSelector,omitempty"`
	TopologyKey       string         `json:"topologyKey"`
}

type TopologySpreadConstraint struct {
	MaxSkew           int32          `json:"maxSkew"`
	TopologyKey       string         `json:"topologyKey"`
	WhenUnsatisfiable string         `json:"whenUnsatisfiable"`
	LabelSelector     *LabelSelector `json:"labelSelector,omitempty"`
	MinDomains        *int32         `json:"minDomains,omitempty"`
}

// Enforced reports whether the scheduler refuses placements that break the
// constraint (whenUnsatisfiable: DoNotSchedule).
func (t TopologySpreadConstraint) Enforced() bool { return t.WhenUnsatisfiable != "ScheduleAnyway" }

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
	Metrics        []HPAMetric                 `json:"metrics,omitempty"`
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

// VerticalPodAutoscaler is an autoscaling.k8s.io/v1 VPA. Its recommendations
// are evidence of over-requesting even when updateMode is Off.
type VerticalPodAutoscaler struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     VPASpec    `json:"spec"`
	Status   VPAStatus  `json:"status"`
}

type VPASpec struct {
	TargetRef      CrossVersionObjectReference `json:"targetRef"`
	UpdatePolicy   *VPAUpdatePolicy            `json:"updatePolicy,omitempty"`
	ResourcePolicy *VPAResourcePolicy          `json:"resourcePolicy,omitempty"`
}

type VPAUpdatePolicy struct {
	UpdateMode string `json:"updateMode,omitempty"`
}

type VPAStatus struct {
	Recommendation *VPARecommendation `json:"recommendation,omitempty"`
}

type VPARecommendation struct {
	ContainerRecommendations []ContainerRecommendation `json:"containerRecommendations,omitempty"`
}

type ContainerRecommendation struct {
	ContainerName string            `json:"containerName"`
	Target        map[string]string `json:"target,omitempty"`
	LowerBound    map[string]string `json:"lowerBound,omitempty"`
	UpperBound    map[string]string `json:"upperBound,omitempty"`
	// UncappedTarget is the recommendation before minAllowed and maxAllowed apply.
	UncappedTarget map[string]string `json:"uncappedTarget,omitempty"`
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

// LimitRange is a core/v1 LimitRange.
type LimitRange struct {
	Metadata ObjectMeta     `json:"metadata"`
	Spec     LimitRangeSpec `json:"spec"`
}

type LimitRangeSpec struct {
	Limits []LimitRangeItem `json:"limits"`
}

type LimitRangeItem struct {
	Type           string            `json:"type"`
	Default        map[string]string `json:"default,omitempty"`
	DefaultRequest map[string]string `json:"defaultRequest,omitempty"`
}

// DefaultRequests returns the requests the LimitRange gives a container that
// sets none: defaultRequest where present, otherwise the default limit, which
// is what Kubernetes does.
func (i LimitRangeItem) DefaultRequests() map[string]string {
	out := map[string]string{}
	for k, v := range i.Default {
		out[k] = v
	}
	for k, v := range i.DefaultRequest {
		out[k] = v
	}
	return out
}

// NodePool is a Karpenter NodePool (karpenter.sh/v1; v1beta1 reads the same way).
type NodePool struct {
	Metadata ObjectMeta   `json:"metadata"`
	Spec     NodePoolSpec `json:"spec"`
}

type NodePoolSpec struct {
	Disruption NodePoolDisruption `json:"disruption"`
	Limits     map[string]string  `json:"limits,omitempty"`
}

type NodePoolDisruption struct {
	ConsolidationPolicy string   `json:"consolidationPolicy,omitempty"`
	ConsolidateAfter    string   `json:"consolidateAfter,omitempty"`
	Budgets             []Budget `json:"budgets,omitempty"`
}

// Budget limits how many of a NodePool's nodes Karpenter may disrupt at once.
type Budget struct {
	Nodes    string   `json:"nodes"`
	Schedule string   `json:"schedule,omitempty"`
	Duration string   `json:"duration,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
}

// NodeClaim is a Karpenter NodeClaim: the request that launched one node.
type NodeClaim struct {
	Metadata ObjectMeta      `json:"metadata"`
	Status   NodeClaimStatus `json:"status"`
}

type NodeClaimStatus struct {
	NodeName   string      `json:"nodeName,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
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
	EventTime          string          `json:"eventTime,omitempty"`
	Source             EventSource     `json:"source"`
	ReportingComponent string          `json:"reportingComponent,omitempty"`
}

// Time returns the most recent timestamp the event carries.
func (e *Event) Time() string {
	for _, t := range []string{e.LastTimestamp, e.EventTime, e.FirstTimestamp} {
		if t != "" {
			return t
		}
	}
	return ""
}

type ObjectReference struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type EventSource struct {
	Component string `json:"component,omitempty"`
}

// VPAResourcePolicy limits what a VPA recommends, per container.
type VPAResourcePolicy struct {
	ContainerPolicies []VPAContainerPolicy `json:"containerPolicies,omitempty"`
}

// VPAContainerPolicy applies to one container, or to all with name "*".
type VPAContainerPolicy struct {
	ContainerName       string            `json:"containerName"`
	Mode                string            `json:"mode,omitempty"`
	MinAllowed          map[string]string `json:"minAllowed,omitempty"`
	MaxAllowed          map[string]string `json:"maxAllowed,omitempty"`
	ControlledResources []string          `json:"controlledResources,omitempty"`
}

// HPAMetric is one autoscaling/v2 metric. Only the resource kinds matter to
// KubeShrink: they scale on usage divided by requests.
type HPAMetric struct {
	Type              string                      `json:"type"`
	Resource          *HPAResourceSource          `json:"resource,omitempty"`
	ContainerResource *HPAContainerResourceSource `json:"containerResource,omitempty"`
}

type HPAResourceSource struct {
	Name string `json:"name"`
}

type HPAContainerResourceSource struct {
	Name      string `json:"name"`
	Container string `json:"container"`
}
