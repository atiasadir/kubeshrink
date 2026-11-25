// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Package snapshot holds the Kubernetes objects KubeShrink reasons about.
//
// It deliberately models only the fields KubeShrink's rules read, instead of
// importing the full Kubernetes API types. Unknown fields are ignored. The same
// structures are filled from offline files or from a live, read-only cluster.
package snapshot

import "encoding/json"

// ObjectMeta is the part of an object's metadata that KubeShrink reads.
type ObjectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
	OwnerReferences []OwnerReference  `json:"ownerReferences,omitempty"`
}

// OwnerReference names an owner of an object; Controller marks the one that
// manages it.
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

// Node is a core/v1 Node, with only the fields KubeShrink reads.
type Node struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     NodeSpec   `json:"spec"`
	Status   NodeStatus `json:"status"`
}

// NodeSpec holds whether the node is cordoned, and its taints.
type NodeSpec struct {
	Unschedulable bool    `json:"unschedulable,omitempty"`
	Taints        []Taint `json:"taints,omitempty"`
}

// Taint keeps pods that do not tolerate it off the node.
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

// NodeStatus holds what the node can offer to pods.
type NodeStatus struct {
	Allocatable map[string]string `json:"allocatable,omitempty"`
}

// Pod is a core/v1 Pod, with only the fields KubeShrink reads.
type Pod struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   PodStatus  `json:"status"`
}

// PodSpec holds where a pod may run, what it requests and the volumes it uses.
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

// Toleration lets a pod run on nodes with a matching taint.
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

// Container holds a container's requests, its restart policy (Always marks a
// native sidecar) and its ports.
type Container struct {
	Name          string               `json:"name"`
	Resources     ResourceRequirements `json:"resources"`
	RestartPolicy string               `json:"restartPolicy,omitempty"`
	Ports         []ContainerPort      `json:"ports,omitempty"`
}

// IsSidecar reports whether an init container keeps running next to the app
// containers (a native sidecar, restartPolicy: Always).
func (c Container) IsSidecar() bool { return c.RestartPolicy == "Always" }

// ContainerPort is a port a container listens on. A host port allows one such
// pod per node.
type ContainerPort struct {
	ContainerPort int32  `json:"containerPort"`
	HostPort      int32  `json:"hostPort,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

// ResourceRequirements holds a container's requests. Limits do not affect
// scheduling, so they are not read.
type ResourceRequirements struct {
	Requests map[string]string `json:"requests,omitempty"`
}

// Volume is a pod volume. Only emptyDir and hostPath matter here, because they
// keep data on the node.
type Volume struct {
	Name     string          `json:"name"`
	EmptyDir json.RawMessage `json:"emptyDir,omitempty"`
	HostPath *HostPath       `json:"hostPath,omitempty"`
}

// HostPath is a hostPath volume source.
type HostPath struct {
	Path string `json:"path"`
}

// PodStatus holds the pod's phase.
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

// NodeAffinity holds a pod's node affinity. Only the required part limits
// where the pod can go; the preferred part is kept as raw JSON.
type NodeAffinity struct {
	Required  *NodeSelector   `json:"requiredDuringSchedulingIgnoredDuringExecution,omitempty"`
	Preferred json.RawMessage `json:"preferredDuringSchedulingIgnoredDuringExecution,omitempty"`
}

// NodeSelector is a list of terms, any one of which a node must match.
type NodeSelector struct {
	NodeSelectorTerms []NodeSelectorTerm `json:"nodeSelectorTerms"`
}

// NodeSelectorTerm is a set of requirements that a node must all match.
type NodeSelectorTerm struct {
	MatchExpressions []NodeSelectorRequirement `json:"matchExpressions,omitempty"`
	MatchFields      []NodeSelectorRequirement `json:"matchFields,omitempty"`
}

// NodeSelectorRequirement is one expression on a node's labels or fields, such
// as key In (a, b).
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

// PodAffinityTerm selects the pods that a pod must, or must not, share a
// topology domain with.
type PodAffinityTerm struct {
	LabelSelector     *LabelSelector `json:"labelSelector,omitempty"`
	Namespaces        []string       `json:"namespaces,omitempty"`
	NamespaceSelector *LabelSelector `json:"namespaceSelector,omitempty"`
	TopologyKey       string         `json:"topologyKey"`
}

// TopologySpreadConstraint keeps matching pods balanced across topology
// domains, within maxSkew.
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

// PodDisruptionBudget is a policy/v1 PodDisruptionBudget.
type PodDisruptionBudget struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PDBSpec    `json:"spec"`
	Status   PDBStatus  `json:"status"`
}

// PDBSpec selects the pods a budget protects and says how many must stay
// available.
type PDBSpec struct {
	Selector       *LabelSelector  `json:"selector,omitempty"`
	MinAvailable   json.RawMessage `json:"minAvailable,omitempty"`
	MaxUnavailable json.RawMessage `json:"maxUnavailable,omitempty"`
}

// PDBStatus is what the disruption controller last computed for a budget.
type PDBStatus struct {
	// DisruptionsAllowed is a pointer so a missing status is not mistaken for 0.
	DisruptionsAllowed *int32 `json:"disruptionsAllowed,omitempty"`
	CurrentHealthy     int32  `json:"currentHealthy,omitempty"`
	DesiredHealthy     int32  `json:"desiredHealthy,omitempty"`
	ExpectedPods       int32  `json:"expectedPods,omitempty"`
}

// CrossVersionObjectReference names the workload an autoscaler or a VPA
// targets.
type CrossVersionObjectReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	APIVersion string `json:"apiVersion,omitempty"`
}

// HorizontalPodAutoscaler is an autoscaling/v2 HorizontalPodAutoscaler.
type HorizontalPodAutoscaler struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     HPASpec    `json:"spec"`
	Status   HPAStatus  `json:"status"`
}

// HPASpec holds the workload an HPA scales, its replica range and its metrics.
type HPASpec struct {
	ScaleTargetRef CrossVersionObjectReference `json:"scaleTargetRef"`
	MinReplicas    *int32                      `json:"minReplicas,omitempty"`
	MaxReplicas    int32                       `json:"maxReplicas"`
	Metrics        []HPAMetric                 `json:"metrics,omitempty"`
}

// HPAStatus holds an HPA's current and desired replica counts.
type HPAStatus struct {
	CurrentReplicas int32 `json:"currentReplicas,omitempty"`
	DesiredReplicas int32 `json:"desiredReplicas,omitempty"`
}

// ScaledObject is a KEDA keda.sh/v1alpha1 ScaledObject.
type ScaledObject struct {
	Metadata ObjectMeta       `json:"metadata"`
	Spec     ScaledObjectSpec `json:"spec"`
}

// ScaledObjectSpec holds the workload a KEDA ScaledObject scales and its
// replica counts.
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

// VPASpec holds the workload a VPA targets, how it applies its recommendation
// and its resource policy.
type VPASpec struct {
	TargetRef      CrossVersionObjectReference `json:"targetRef"`
	UpdatePolicy   *VPAUpdatePolicy            `json:"updatePolicy,omitempty"`
	ResourcePolicy *VPAResourcePolicy          `json:"resourcePolicy,omitempty"`
}

// VPAUpdatePolicy holds a VPA's update mode, such as Off, Initial or Auto.
type VPAUpdatePolicy struct {
	UpdateMode string `json:"updateMode,omitempty"`
}

// VPAStatus holds a VPA's latest recommendation.
type VPAStatus struct {
	Recommendation *VPARecommendation `json:"recommendation,omitempty"`
}

// VPARecommendation holds one recommendation per container.
type VPARecommendation struct {
	ContainerRecommendations []ContainerRecommendation `json:"containerRecommendations,omitempty"`
}

// ContainerRecommendation is a VPA's recommended size for one container, with
// its bounds.
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

// WorkloadSpec holds a workload's desired replica count.
type WorkloadSpec struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

// WorkloadStatus holds a workload's current replica count.
type WorkloadStatus struct {
	Replicas int32 `json:"replicas,omitempty"`
}

// LimitRange is a core/v1 LimitRange.
type LimitRange struct {
	Metadata ObjectMeta     `json:"metadata"`
	Spec     LimitRangeSpec `json:"spec"`
}

// LimitRangeSpec lists the limits of a LimitRange.
type LimitRangeSpec struct {
	Limits []LimitRangeItem `json:"limits"`
}

// LimitRangeItem holds the default requests and limits a LimitRange gives
// containers that set none.
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

// NodePoolSpec holds a Karpenter NodePool's disruption settings and resource
// limits.
type NodePoolSpec struct {
	Disruption NodePoolDisruption `json:"disruption"`
	Limits     map[string]string  `json:"limits,omitempty"`
}

// NodePoolDisruption says when Karpenter may consolidate a NodePool's nodes,
// and how many at once.
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

// NodeClaimStatus holds the node a NodeClaim launched, and its conditions.
type NodeClaimStatus struct {
	NodeName   string      `json:"nodeName,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

// Condition is a status condition, such as a NodeClaim's Consolidatable.
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

// ObjectReference names the object an event is about.
type ObjectReference struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// EventSource names the component that reported an event.
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

// HPAResourceSource is a CPU or memory metric of an HPA, averaged over the
// pods.
type HPAResourceSource struct {
	Name string `json:"name"`
}

// HPAContainerResourceSource is a CPU or memory metric of an HPA for one
// container.
type HPAContainerResourceSource struct {
	Name      string `json:"name"`
	Container string `json:"container"`
}
