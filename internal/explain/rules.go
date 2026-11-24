// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

const (
	annKarpenterDoNotDisrupt      = "karpenter.sh/do-not-disrupt"
	annCASafeToEvict              = "cluster-autoscaler.kubernetes.io/safe-to-evict"
	annCASafeToEvictLocalVolumes  = "cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"
	annCAScaleDownDisabled        = "cluster-autoscaler.kubernetes.io/scale-down-disabled"
	annKedaPausedReplicas         = "autoscaling.keda.sh/paused-replicas"
	annDownscalerExclude          = "downscaler/exclude"
	annDownscalerDowntimeReplicas = "downscaler/downtime-replicas"
	annMirrorPod                  = "kubernetes.io/config.mirror"

	caDefaultUtilizationThreshold = 0.5
)

func annField(key string) string { return fmt.Sprintf(".metadata.annotations[%q]", key) }

func podRef(p *snapshot.Pod) string {
	return snapshot.Ref("Pod", p.Metadata.Namespace, p.Metadata.Name)
}

func nodeRef(n *snapshot.Node) string { return snapshot.Ref("Node", "", n.Metadata.Name) }

func fmtLabels(m map[string]string) string {
	if len(m) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// with copies base before appending, so findings never share evidence slices.
func with(base []Evidence, extra ...Evidence) []Evidence {
	out := make([]Evidence, 0, len(base)+len(extra))
	return append(append(out, base...), extra...)
}

func safeToEvict(p *snapshot.Pod) bool { return p.Metadata.Annotations[annCASafeToEvict] == "true" }

func checkKarpenterDoNotDisrupt(c *nodeContext) []Finding {
	var out []Finding
	notKarpenter := []string{"If this node is not managed by Karpenter, the annotation has no effect."}
	if c.node.Metadata.Annotations[annKarpenterDoNotDisrupt] == "true" {
		ref := nodeRef(c.node)
		out = append(out, Finding{
			Rule: "karpenter-do-not-disrupt", Severity: Blocker, Subject: ref,
			AppliesTo: []string{Karpenter}, Confidence: High,
			Claim:        "The node is annotated karpenter.sh/do-not-disrupt=true, so Karpenter will not voluntarily disrupt it (no consolidation, drift or expiry replacement).",
			Evidence:     []Evidence{{Object: ref, Field: annField(annKarpenterDoNotDisrupt), Value: "true"}},
			Alternatives: notKarpenter,
		})
	}
	for _, p := range c.movable {
		if p.Metadata.Annotations[annKarpenterDoNotDisrupt] != "true" {
			continue
		}
		ref := podRef(p)
		out = append(out, Finding{
			Rule: "karpenter-do-not-disrupt", Severity: Blocker, Subject: ref,
			AppliesTo: []string{Karpenter}, Confidence: High,
			Claim:        "The pod is annotated karpenter.sh/do-not-disrupt=true. Karpenter will not voluntarily disrupt a node running it, so the node stays until the pod finishes or the annotation is removed.",
			Evidence:     []Evidence{{Object: ref, Field: annField(annKarpenterDoNotDisrupt), Value: "true"}},
			Alternatives: notKarpenter,
		})
	}
	return out
}

func checkCAScaleDownDisabled(c *nodeContext) []Finding {
	if c.node.Metadata.Annotations[annCAScaleDownDisabled] != "true" {
		return nil
	}
	ref := nodeRef(c.node)
	return []Finding{{
		Rule: "ca-scale-down-disabled", Severity: Blocker, Subject: ref,
		AppliesTo: []string{CA}, Confidence: High,
		Claim:        "The node is annotated cluster-autoscaler.kubernetes.io/scale-down-disabled=true, so Cluster Autoscaler never scales it down.",
		Evidence:     []Evidence{{Object: ref, Field: annField(annCAScaleDownDisabled), Value: "true"}},
		Alternatives: []string{"If the node is managed by Karpenter rather than Cluster Autoscaler, this annotation has no effect."},
	}}
}

func checkSafeToEvictFalse(c *nodeContext) []Finding {
	var out []Finding
	for _, p := range c.movable {
		if p.Metadata.Annotations[annCASafeToEvict] != "false" {
			continue
		}
		ref := podRef(p)
		out = append(out, Finding{
			Rule: "ca-safe-to-evict-false", Severity: Blocker, Subject: ref,
			AppliesTo: []string{CA}, Confidence: High,
			Claim:    "The pod is annotated cluster-autoscaler.kubernetes.io/safe-to-evict=false, so Cluster Autoscaler will not remove the node it runs on.",
			Evidence: []Evidence{{Object: ref, Field: annField(annCASafeToEvict), Value: "false"}},
		})
	}
	return out
}

func checkPDBs(c *nodeContext) []Finding {
	var out []Finding
	for _, p := range c.movable {
		pdbs := c.snap.PDBsFor(p)
		ref := podRef(p)
		labels := Evidence{Object: ref, Field: ".metadata.labels", Value: fmtLabels(p.Metadata.Labels)}
		switch {
		case len(pdbs) == 0:
			continue
		case len(pdbs) > 1:
			ev := []Evidence{labels}
			for _, b := range pdbs {
				ev = append(ev, Evidence{Object: pdbRef(b), Field: ".spec.selector", Value: "matches this pod"})
			}
			out = append(out, Finding{
				Rule: "multiple-pdbs", Severity: Blocker, Subject: ref,
				AppliesTo: bothAutoscalers, Confidence: High,
				Claim:    fmt.Sprintf("%d PodDisruptionBudgets select this pod. The eviction API refuses to evict a pod covered by more than one PDB, so the node cannot be drained.", len(pdbs)),
				Evidence: ev,
				Unknowns: []string{"Autoscalers may emit no event for this; recent Kubernetes versions record a MultiplePodDisruptionBudgets event on the pod."},
			})
			continue
		}
		b := pdbs[0]
		bref := pdbRef(b)
		if b.Status.DisruptionsAllowed == nil {
			out = append(out, Finding{
				Rule: "pdb-zero-disruptions", Severity: PossibleBlocker, Subject: ref,
				AppliesTo: bothAutoscalers, Confidence: Low,
				Claim:    fmt.Sprintf("PodDisruptionBudget %s selects this pod, but the snapshot has no .status.disruptionsAllowed, so whether it blocks eviction is unknown.", b.Metadata.Name),
				Evidence: []Evidence{labels, {Object: bref, Field: ".status.disruptionsAllowed", Value: "(missing)"}},
				Unknowns: []string{"Export the PDB with its status (kubectl get pdb -o yaml) to resolve this."},
			})
			continue
		}
		if *b.Status.DisruptionsAllowed > 0 {
			continue
		}
		ev := []Evidence{
			{Object: bref, Field: ".status.disruptionsAllowed", Value: "0"},
			{Object: bref, Field: ".status.currentHealthy / .status.desiredHealthy", Value: fmt.Sprintf("%d / %d", b.Status.CurrentHealthy, b.Status.DesiredHealthy)},
		}
		if snapshot.HasValue(b.Spec.MinAvailable) {
			ev = append(ev, Evidence{Object: bref, Field: ".spec.minAvailable", Value: strings.Trim(string(b.Spec.MinAvailable), `"`)})
		}
		if snapshot.HasValue(b.Spec.MaxUnavailable) {
			ev = append(ev, Evidence{Object: bref, Field: ".spec.maxUnavailable", Value: strings.Trim(string(b.Spec.MaxUnavailable), `"`)})
		}
		ev = append(ev, labels)
		var alt string
		if b.Status.CurrentHealthy < b.Status.DesiredHealthy {
			alt = "Transient: fewer pods are healthy than the budget requires; the budget may open up once they become ready."
		} else {
			alt = "Structural: every healthy pod is needed to satisfy the budget (e.g. minAvailable equals the replica count, or maxUnavailable is 0). It stays at zero until the budget or the replica count changes."
		}
		out = append(out, Finding{
			Rule: "pdb-zero-disruptions", Severity: Blocker, Subject: ref,
			AppliesTo: bothAutoscalers, Confidence: High,
			Claim:        fmt.Sprintf("PodDisruptionBudget %s currently allows 0 disruptions, so this pod cannot be evicted and the node cannot be drained.", b.Metadata.Name),
			Evidence:     ev,
			Alternatives: []string{alt},
			Unknowns:     []string{"The snapshot is one moment; disruptionsAllowed changes as pods become ready or unready."},
		})
	}
	return out
}

func pdbRef(b *snapshot.PodDisruptionBudget) string {
	return snapshot.Ref("PodDisruptionBudget", b.Metadata.Namespace, b.Metadata.Name)
}

func checkNoController(c *nodeContext) []Finding {
	var out []Finding
	for _, p := range c.movable {
		if p.Metadata.ControllerRef() != nil || safeToEvict(p) {
			continue
		}
		ref := podRef(p)
		out = append(out, Finding{
			Rule: "pod-without-controller", Severity: Blocker, Subject: ref,
			AppliesTo: []string{CA}, Confidence: High,
			Claim:    "The pod has no controller, so nothing would recreate it elsewhere. Cluster Autoscaler keeps the node unless the pod is annotated safe-to-evict=true.",
			Evidence: []Evidence{{Object: ref, Field: ".metadata.ownerReferences", Value: "no entry with controller=true"}},
			Unknowns: []string{"Karpenter's handling of controller-less pods differs from Cluster Autoscaler's and has changed across versions; check its DisruptionBlocked events."},
		})
	}
	return out
}

func checkLocalStorage(c *nodeContext) []Finding {
	var out []Finding
	for _, p := range c.movable {
		if safeToEvict(p) {
			continue
		}
		allowed := map[string]bool{}
		for _, v := range strings.Split(p.Metadata.Annotations[annCASafeToEvictLocalVolumes], ",") {
			if v = strings.TrimSpace(v); v != "" {
				allowed[v] = true
			}
		}
		ref := podRef(p)
		var ev []Evidence
		for _, v := range p.Spec.Volumes {
			if allowed[v.Name] {
				continue
			}
			switch {
			case snapshot.HasValue(v.EmptyDir) || string(v.EmptyDir) == "{}":
				ev = append(ev, Evidence{Object: ref, Field: fmt.Sprintf(".spec.volumes[%s].emptyDir", v.Name), Value: "set"})
			case v.HostPath != nil:
				ev = append(ev, Evidence{Object: ref, Field: fmt.Sprintf(".spec.volumes[%s].hostPath.path", v.Name), Value: v.HostPath.Path})
			}
		}
		if len(ev) == 0 {
			continue
		}
		out = append(out, Finding{
			Rule: "local-storage", Severity: PossibleBlocker, Subject: ref,
			AppliesTo: []string{CA}, Confidence: Medium,
			Claim:    "The pod uses local storage (emptyDir or hostPath). Cluster Autoscaler skips nodes with such pods when --skip-nodes-with-local-storage is enabled.",
			Evidence: ev,
			Unknowns: []string{"The Cluster Autoscaler flag value is not in the snapshot."},
		})
	}
	return out
}

func checkKubeSystem(c *nodeContext) []Finding {
	var out []Finding
	for _, p := range c.movable {
		if p.Metadata.Namespace != "kube-system" || safeToEvict(p) || len(c.snap.PDBsFor(p)) > 0 {
			continue
		}
		ref := podRef(p)
		out = append(out, Finding{
			Rule: "kube-system-pod", Severity: PossibleBlocker, Subject: ref,
			AppliesTo: []string{CA}, Confidence: Medium,
			Claim:    "The pod runs in kube-system without a PodDisruptionBudget. Cluster Autoscaler skips such nodes when --skip-nodes-with-system-pods is enabled.",
			Evidence: []Evidence{{Object: ref, Field: ".metadata.namespace", Value: "kube-system"}, {Object: ref, Field: "matching PodDisruptionBudgets", Value: "none"}},
			Unknowns: []string{"The Cluster Autoscaler flag value is not in the snapshot; some managed offerings change the default."},
		})
	}
	return out
}

func checkUtilization(c *nodeContext) []Finding {
	alloc := snapshot.NodeAllocatable(c.node)
	if alloc.MilliCPU == 0 || alloc.MemoryBytes == 0 {
		return nil
	}
	var used snapshot.Resources
	for _, p := range c.snap.PodsOnNode(c.node.Metadata.Name) {
		used = used.Add(snapshot.PodRequests(p))
	}
	cpu := float64(used.MilliCPU) / float64(alloc.MilliCPU)
	mem := float64(used.MemoryBytes) / float64(alloc.MemoryBytes)
	if max(cpu, mem) < caDefaultUtilizationThreshold {
		return nil
	}
	ref := nodeRef(c.node)
	return []Finding{{
		Rule: "utilization-above-threshold", Severity: PossibleBlocker, Subject: ref,
		AppliesTo: []string{CA}, Confidence: Medium,
		Claim: fmt.Sprintf("Pods on this node request %.0f%% of its CPU and %.0f%% of its memory. Cluster Autoscaler only considers nodes below --scale-down-utilization-threshold (0.5 by default), so it may never evaluate this node for removal.", cpu*100, mem*100),
		Evidence: []Evidence{
			{Object: ref, Field: ".status.allocatable", Value: alloc.String()},
			{Object: ref, Field: "sum of pod requests (including DaemonSets)", Value: used.String()},
		},
		Alternatives: []string{"Karpenter consolidation does not use this threshold; it compares the cost of deleting or replacing the node."},
		Unknowns:     []string{"The configured threshold and --ignore-daemonsets-utilization are not in the snapshot."},
	}}
}

var disruptionReasons = map[string]bool{"DisruptionBlocked": true, "Unconsolidatable": true, "ScaleDownFailed": true, "ScaleDown": true}

func checkAutoscalerEvents(c *nodeContext) []Finding {
	targets := map[string]bool{nodeRef(c.node): true}
	for _, p := range c.movable {
		targets[podRef(p)] = true
	}
	var out []Finding
	for i := range c.snap.Events {
		e := &c.snap.Events[i]
		obj := snapshot.Ref(e.InvolvedObject.Kind, e.InvolvedObject.Namespace, e.InvolvedObject.Name)
		if !targets[obj] {
			continue
		}
		who := autoscalerOf(e)
		if who == "" && !disruptionReasons[e.Reason] {
			continue
		}
		ts := e.LastTimestamp
		if ts == "" {
			ts = e.FirstTimestamp
		}
		reporter, applies := "A component", []string(nil)
		if who != "" {
			reporter, applies = who, []string{who}
		}
		out = append(out, Finding{
			Rule: "autoscaler-event", Severity: Context, Subject: obj,
			AppliesTo: applies, Confidence: High,
			Claim:    fmt.Sprintf("%s reported %s: %s", reporter, e.Reason, e.Message),
			Evidence: []Evidence{{Object: snapshot.Ref("Event", e.Metadata.Namespace, e.Metadata.Name), Field: ".reason, .message", Value: e.Reason + ": " + e.Message, Time: ts}},
			Unknowns: []string{"Events expire (one hour by default) and may describe an earlier state; compare the timestamp with when the snapshot was taken."},
		})
	}
	return out
}

func autoscalerOf(e *snapshot.Event) string {
	comp := strings.ToLower(e.Source.Component + " " + e.ReportingComponent)
	switch {
	case strings.Contains(comp, "karpenter"):
		return Karpenter
	case strings.Contains(comp, "cluster-autoscaler"):
		return CA
	}
	return ""
}
