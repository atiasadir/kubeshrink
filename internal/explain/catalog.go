// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

// Rule describes one deterministic check. Some checks emit several rule IDs;
// those extra IDs are listed without a check so `kubeshrink rules` documents them.
type Rule struct {
	ID          string `json:"id"`
	Layer       Layer  `json:"layer"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
	nodeCheck   func(*nodeContext) []Finding
	nsCheck     func(*nsContext) []Finding
}

// Catalog is every rule, grouped by layer.
var Catalog = []Rule{
	{
		ID: "replica-floor", Layer: LayerReplicas,
		Title:       "Workload held at its autoscaler minimum",
		Explanation: "HPA minReplicas or KEDA minReplicaCount sets a lower bound. At the minimum, the pods exist because of configuration, not load.",
		nodeCheck:   checkReplicaFloorsNode, nsCheck: checkReplicaFloorsNS,
	},
	{
		ID: "keda-paused", Layer: LayerReplicas,
		Title:       "KEDA scaling paused",
		Explanation: "autoscaling.keda.sh/paused-replicas on a ScaledObject pins the workload at that count. Emitted by the replica-floor check.",
	},
	{
		ID: "keda-hpa-conflict", Layer: LayerReplicas,
		Title:       "KEDA ScaledObject and a separate HPA on one workload",
		Explanation: "KEDA creates and owns an HPA for each ScaledObject. A second, user-made HPA on the same target fights it. Emitted by the replica-floor check.",
	},
	{
		ID: "downscaler-excluded", Layer: LayerReplicas,
		Title:       "Workload excluded from kube-downscaler",
		Explanation: "downscaler/exclude=true makes scheduled off-hours downscaling skip the workload. Emitted by the replica-floor check.",
	},
	{
		ID: "downscaler-vs-hpa-floor", Layer: LayerReplicas,
		Title:       "kube-downscaler target below the HPA minimum",
		Explanation: "kube-downscaler lowers replicas off-hours, but an HPA with a higher minReplicas scales them straight back up. Emitted by the replica-floor check.",
	},
	{
		ID: "spread-constraint", Layer: LayerPolicy,
		Title:       "Replicas forced onto separate nodes or zones",
		Explanation: "Required pod anti-affinity or an enforced topology spread keeps a workload's replicas apart, which limits how tightly they can be packed.",
		nsCheck:     checkSpreadConstraints,
	},
	{
		ID: "no-room-elsewhere", Layer: LayerPolicy,
		Title:       "No other node can take a pod",
		Explanation: "Each pod that must move is placed, largest first, on another schedulable node that matches its node selector and required node affinity, tolerates its taints, respects required pod (anti-)affinity, topology spread and host ports, and has free requests. A pod that fits nowhere keeps the node.",
		nodeCheck:   checkRoomElsewhere,
	},
	{
		ID: "pdb-zero-disruptions", Layer: LayerPolicy,
		Title:       "PodDisruptionBudget allows zero disruptions",
		Explanation: "Draining uses the eviction API, which refuses to evict a pod whose PDB has status.disruptionsAllowed=0. Structural when minAvailable equals the replica count (or maxUnavailable is 0); transient when some pods are unhealthy.",
		nodeCheck:   checkPDBs,
	},
	{
		ID: "multiple-pdbs", Layer: LayerPolicy,
		Title:       "Pod selected by more than one PDB",
		Explanation: "The eviction API refuses to evict a pod covered by several PDBs, even when each has room. Emitted by the PDB check.",
	},
	{
		ID: "karpenter-do-not-disrupt", Layer: LayerPolicy,
		Title:       "Karpenter do-not-disrupt annotation",
		Explanation: "karpenter.sh/do-not-disrupt=true on a pod or node tells Karpenter never to voluntarily remove that node (consolidation, drift, expiry).",
		nodeCheck:   checkKarpenterDoNotDisrupt,
	},
	{
		ID: "ca-safe-to-evict-false", Layer: LayerPolicy,
		Title:       "Pod marked not safe to evict",
		Explanation: "cluster-autoscaler.kubernetes.io/safe-to-evict=false on a pod stops Cluster Autoscaler from removing the node it runs on.",
		nodeCheck:   checkSafeToEvictFalse,
	},
	{
		ID: "pod-without-controller", Layer: LayerPolicy,
		Title:       "Pod without a controller",
		Explanation: "A bare pod (no ownerReference with controller=true) would not be recreated elsewhere, so Cluster Autoscaler keeps its node unless the pod is annotated safe-to-evict=true.",
		nodeCheck:   checkNoController,
	},
	{
		ID: "local-storage", Layer: LayerPolicy,
		Title:       "Pod uses emptyDir or hostPath storage",
		Explanation: "With --skip-nodes-with-local-storage enabled, Cluster Autoscaler will not remove a node whose pods use local volumes, unless they are listed in safe-to-evict-local-volumes.",
		nodeCheck:   checkLocalStorage,
	},
	{
		ID: "kube-system-pod", Layer: LayerPolicy,
		Title:       "kube-system pod without a PDB",
		Explanation: "With --skip-nodes-with-system-pods enabled, Cluster Autoscaler will not remove a node running a non-DaemonSet kube-system pod that has no PDB.",
		nodeCheck:   checkKubeSystem,
	},
	{
		ID: "ca-scale-down-disabled", Layer: LayerNodes,
		Title:       "Cluster Autoscaler scale-down disabled on the node",
		Explanation: "cluster-autoscaler.kubernetes.io/scale-down-disabled=true on a node excludes it from Cluster Autoscaler scale-down.",
		nodeCheck:   checkCAScaleDownDisabled,
	},
	{
		ID: "utilization-above-threshold", Layer: LayerNodes,
		Title:       "Node too busy to be a scale-down candidate",
		Explanation: "Cluster Autoscaler only considers nodes whose requested CPU or memory is below --scale-down-utilization-threshold (0.5 by default).",
		nodeCheck:   checkUtilization,
	},
	{
		ID: "karpenter-consolidation-policy", Layer: LayerNodes,
		Title:       "NodePool only consolidates empty nodes",
		Explanation: "consolidationPolicy WhenEmpty tells Karpenter to remove only nodes without workload pods; underused nodes stay.",
		nodeCheck:   checkKarpenterNodePool,
	},
	{
		ID: "karpenter-consolidation-disabled", Layer: LayerNodes,
		Title:       "NodePool consolidation turned off",
		Explanation: "consolidateAfter: Never turns consolidation off for the NodePool. Emitted by the NodePool check.",
	},
	{
		ID: "karpenter-budget", Layer: LayerNodes,
		Title:       "NodePool disruption budget allows no disruptions",
		Explanation: "A budget of 0 nodes, always or during a scheduled window, stops Karpenter from consolidating. Emitted by the NodePool check.",
	},
	{
		ID: "karpenter-not-consolidatable-yet", Layer: LayerNodes,
		Title:       "Karpenter still waiting before considering the node",
		Explanation: "Karpenter considers a node for consolidation only after consolidateAfter has passed since pods were last added or removed. Emitted by the NodePool check.",
	},
	{
		ID: "autoscaler-event", Layer: LayerNodes,
		Title:       "What the autoscaler itself reported",
		Explanation: "Events from Cluster Autoscaler or Karpenter (e.g. DisruptionBlocked, Unconsolidatable, ScaleDownFailed) on the node, its NodeClaim or its pods, quoted as evidence.",
		nodeCheck:   checkAutoscalerEvents,
	},
}

func ruleLayer(id string) Layer {
	for _, r := range Catalog {
		if r.ID == id {
			return r.Layer
		}
	}
	return ""
}

func ruleTitle(id string) string {
	for _, r := range Catalog {
		if r.ID == id {
			return r.Title
		}
	}
	return id
}
