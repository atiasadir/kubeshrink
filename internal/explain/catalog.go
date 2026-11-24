// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

// Rule describes one deterministic check. Some checks emit several rule IDs;
// those extra IDs are listed with a nil check so `kubeshrink rules` documents them.
type Rule struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
	check       func(*nodeContext) []Finding
}

// Catalog is the ordered list of rules run for every node.
var Catalog = []Rule{
	{
		ID:          "karpenter-do-not-disrupt",
		Title:       "Karpenter do-not-disrupt annotation",
		Explanation: "karpenter.sh/do-not-disrupt=true on a pod or node tells Karpenter never to voluntarily remove that node (consolidation, drift, expiry). Remove the annotation, or wait for the pod to finish.",
		check:       checkKarpenterDoNotDisrupt,
	},
	{
		ID:          "ca-scale-down-disabled",
		Title:       "Cluster Autoscaler scale-down disabled on the node",
		Explanation: "cluster-autoscaler.kubernetes.io/scale-down-disabled=true on a node excludes it from Cluster Autoscaler scale-down.",
		check:       checkCAScaleDownDisabled,
	},
	{
		ID:          "ca-safe-to-evict-false",
		Title:       "Pod marked not safe to evict",
		Explanation: "cluster-autoscaler.kubernetes.io/safe-to-evict=false on a pod stops Cluster Autoscaler from removing the node it runs on.",
		check:       checkSafeToEvictFalse,
	},
	{
		ID:          "pdb-zero-disruptions",
		Title:       "PodDisruptionBudget allows zero disruptions",
		Explanation: "Draining uses the eviction API, which refuses to evict a pod whose PDB has status.disruptionsAllowed=0. Structural when minAvailable equals the replica count (or maxUnavailable is 0); transient when some pods are unhealthy.",
		check:       checkPDBs,
	},
	{
		ID:          "multiple-pdbs",
		Title:       "Pod selected by more than one PDB",
		Explanation: "The eviction API refuses to evict a pod covered by several PDBs, even when each has room. Emitted by the PDB check.",
	},
	{
		ID:          "pod-without-controller",
		Title:       "Pod without a controller",
		Explanation: "A bare pod (no ownerReference with controller=true) would not be recreated elsewhere, so Cluster Autoscaler keeps its node unless the pod is annotated safe-to-evict=true.",
		check:       checkNoController,
	},
	{
		ID:          "local-storage",
		Title:       "Pod uses emptyDir or hostPath storage",
		Explanation: "With --skip-nodes-with-local-storage enabled, Cluster Autoscaler will not remove a node whose pods use local volumes, unless they are listed in safe-to-evict-local-volumes.",
		check:       checkLocalStorage,
	},
	{
		ID:          "kube-system-pod",
		Title:       "kube-system pod without a PDB",
		Explanation: "With --skip-nodes-with-system-pods enabled, Cluster Autoscaler will not remove a node running a non-DaemonSet kube-system pod that has no PDB.",
		check:       checkKubeSystem,
	},
	{
		ID:          "utilization-above-threshold",
		Title:       "Node too busy to be a scale-down candidate",
		Explanation: "Cluster Autoscaler only considers nodes whose requested CPU or memory is below --scale-down-utilization-threshold (0.5 by default).",
		check:       checkUtilization,
	},
	{
		ID:          "no-room-elsewhere",
		Title:       "No other node can take a pod",
		Explanation: "A first-fit estimate: each pod that must move is placed on another schedulable node that matches its nodeSelector, tolerates its taints and has free requests. A pod that fits nowhere keeps the node alive.",
		check:       checkRoomElsewhere,
	},
	{
		ID:          "replica-floor",
		Title:       "Workload held at its autoscaler floor",
		Explanation: "HPA minReplicas or KEDA minReplicaCount sets a lower bound. At the floor, the pods exist because of configuration, not load. Context, not a blocker.",
		check:       checkReplicaFloors,
	},
	{
		ID:          "keda-paused",
		Title:       "KEDA scaling paused",
		Explanation: "autoscaling.keda.sh/paused-replicas on a ScaledObject pins the workload at that count. Emitted by the replica-floor check.",
	},
	{
		ID:          "keda-hpa-conflict",
		Title:       "KEDA ScaledObject and a separate HPA on one workload",
		Explanation: "KEDA creates and owns an HPA for each ScaledObject. A second, user-made HPA on the same target fights it. Emitted by the replica-floor check.",
	},
	{
		ID:          "downscaler-excluded",
		Title:       "Workload excluded from kube-downscaler",
		Explanation: "downscaler/exclude=true makes scheduled off-hours downscaling skip the workload. Emitted by the replica-floor check.",
	},
	{
		ID:          "downscaler-vs-hpa-floor",
		Title:       "kube-downscaler target below the HPA floor",
		Explanation: "kube-downscaler lowers replicas off-hours, but an HPA with a higher minReplicas scales them straight back up. Emitted by the replica-floor check.",
	},
	{
		ID:          "autoscaler-event",
		Title:       "What the autoscaler itself reported",
		Explanation: "Events from Cluster Autoscaler or Karpenter (e.g. DisruptionBlocked, Unconsolidatable, ScaleDownFailed) on the node or its pods, quoted as evidence.",
		check:       checkAutoscalerEvents,
	},
}
