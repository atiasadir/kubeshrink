// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

type workloadKey struct{ Kind, Namespace, Name string }

func (k workloadKey) ref() string { return snapshot.Ref(k.Kind, k.Namespace, k.Name) }

type workloadRef struct {
	key      workloadKey
	inferred bool // Deployment name guessed from the ReplicaSet name
	evidence []Evidence
}

// workloadOf follows ownerReferences from a pod to the object that sets its
// replica count: Pod -> ReplicaSet -> Deployment, or Pod -> StatefulSet.
func workloadOf(s *snapshot.Snapshot, p *snapshot.Pod) (workloadRef, bool) {
	cr := p.Metadata.ControllerRef()
	if cr == nil {
		return workloadRef{}, false
	}
	ns := p.Metadata.Namespace
	ev := []Evidence{{Object: podRef(p), Field: ".metadata.ownerReferences (controller)", Value: cr.Kind + "/" + cr.Name}}
	if cr.Kind != "ReplicaSet" {
		return workloadRef{key: workloadKey{cr.Kind, ns, cr.Name}, evidence: ev}, true
	}
	if rs := s.Workload("ReplicaSet", ns, cr.Name); rs != nil {
		if d := rs.Metadata.ControllerRef(); d != nil && d.Kind == "Deployment" {
			ev = append(ev, Evidence{Object: snapshot.Ref("ReplicaSet", ns, cr.Name), Field: ".metadata.ownerReferences (controller)", Value: "Deployment/" + d.Name})
			return workloadRef{key: workloadKey{"Deployment", ns, d.Name}, evidence: ev}, true
		}
		return workloadRef{key: workloadKey{"ReplicaSet", ns, cr.Name}, evidence: ev}, true
	}
	if i := strings.LastIndex(cr.Name, "-"); i > 0 {
		return workloadRef{key: workloadKey{"Deployment", ns, cr.Name[:i]}, inferred: true, evidence: ev}, true
	}
	return workloadRef{key: workloadKey{"ReplicaSet", ns, cr.Name}, evidence: ev}, true
}

// workloadPods returns the workload's pods that are not in a terminal phase.
func workloadPods(s *snapshot.Snapshot, k workloadKey) []*snapshot.Pod {
	var out []*snapshot.Pod
	for _, p := range s.PodsInNamespace(k.Namespace) {
		if w, ok := workloadOf(s, p); ok && w.key == k {
			out = append(out, p)
		}
	}
	return out
}

// replicas prefers the workload's .spec.replicas and falls back to counting pods.
func replicas(s *snapshot.Snapshot, w workloadRef) (int32, Evidence) {
	k := w.key
	if wl := s.Workload(k.Kind, k.Namespace, k.Name); wl != nil && wl.Spec.Replicas != nil {
		return *wl.Spec.Replicas, Evidence{Object: k.ref(), Field: ".spec.replicas", Value: itoa(*wl.Spec.Replicas)}
	}
	n := int32(len(workloadPods(s, k)))
	return n, Evidence{Object: k.ref(), Field: "running pods in snapshot (workload object not found)", Value: itoa(n)}
}

// requestsOf sums what the pods request.
func requestsOf(pods []*snapshot.Pod) snapshot.Resources {
	var sum snapshot.Resources
	for _, p := range pods {
		sum = sum.Add(snapshot.PodRequests(p))
	}
	return sum
}

func cpuMem(r snapshot.Resources) string {
	return cpuStr(float64(r.MilliCPU)) + " CPU and " + memStr(float64(r.MemoryBytes)) + " memory"
}

func itoa(n int32) string { return strconv.Itoa(int(n)) }

func isKedaManagedHPA(h *snapshot.HorizontalPodAutoscaler) bool {
	for _, o := range h.Metadata.OwnerReferences {
		if o.Kind == "ScaledObject" {
			return true
		}
	}
	return strings.HasPrefix(h.Metadata.Name, "keda-hpa-")
}

// autoscalersOf returns the workload's KEDA ScaledObjects and the HPAs that
// KEDA did not create.
func autoscalersOf(s *snapshot.Snapshot, k workloadKey) ([]*snapshot.ScaledObject, []*snapshot.HorizontalPodAutoscaler) {
	var hpas []*snapshot.HorizontalPodAutoscaler
	for _, h := range s.HPAsFor(k.Kind, k.Namespace, k.Name) {
		if !isKedaManagedHPA(h) {
			hpas = append(hpas, h)
		}
	}
	return s.ScaledObjectsFor(k.Kind, k.Namespace, k.Name), hpas
}

// floor is an autoscaler minimum that a workload sits at.
type floor struct {
	controller string // KEDA or HPA
	param      string // minReplicaCount or minReplicas
	object     string
	min        int32
	value      string
}

// floorsAt returns the minimums the workload sits at: replicas at or below a
// ScaledObject's minReplicaCount or an HPA's minReplicas. A paused
// ScaledObject pins the count instead and is reported on its own.
func floorsAt(sos []*snapshot.ScaledObject, hpas []*snapshot.HorizontalPodAutoscaler, replicas int32) []floor {
	var out []floor
	for _, so := range sos {
		if _, paused := so.Metadata.Annotations[annKedaPausedReplicas]; paused || so.Spec.MinReplicaCount == nil {
			continue
		}
		if m := *so.Spec.MinReplicaCount; m > 0 && replicas <= m {
			out = append(out, floor{"KEDA", "minReplicaCount", snapshot.Ref("ScaledObject", so.Metadata.Namespace, so.Metadata.Name), m, itoa(m)})
		}
	}
	for _, h := range hpas {
		m, val := int32(1), "1 (default)"
		if h.Spec.MinReplicas != nil {
			m, val = *h.Spec.MinReplicas, itoa(*h.Spec.MinReplicas)
		}
		if replicas <= m {
			out = append(out, floor{"HPA", "minReplicas", hpaRef(h), m, val})
		}
	}
	return out
}

// checkReplicaFloorsNode reports the workloads of the node's movable pods that
// are held at a replica floor: an autoscaler minimum, a paused KEDA
// ScaledObject or a kube-downscaler setting (see replicaFindings).
func checkReplicaFloorsNode(c *nodeContext) []Finding { return replicaFindings(c.snap, c.movable) }

// checkReplicaFloorsNS does the same as checkReplicaFloorsNode for every pod
// of a namespace.
func checkReplicaFloorsNS(c *nsContext) []Finding { return replicaFindings(c.snap, c.pods) }

// replicaFindings explains why the pods exist at all: which autoscaler floor
// or schedule keeps each of their workloads from scaling lower.
func replicaFindings(s *snapshot.Snapshot, pods []*snapshot.Pod) []Finding {
	var out []Finding
	seen := map[workloadKey]bool{}
	for _, p := range pods {
		w, ok := workloadOf(s, p)
		if !ok || seen[w.key] {
			continue
		}
		seen[w.key] = true
		out = append(out, workloadFindings(s, w)...)
	}
	return out
}

func workloadFindings(s *snapshot.Snapshot, w workloadRef) []Finding {
	k := w.key
	wref := k.ref()
	n, repEv := replicas(s, w)
	base := with(w.evidence, repEv)
	var unknowns []string
	if w.inferred {
		unknowns = append(unknowns, "The Deployment name was inferred from the ReplicaSet name because the ReplicaSet is not in the snapshot.")
	}
	sos, hpas := autoscalersOf(s, k)
	var out []Finding
	for _, so := range sos {
		v, ok := so.Metadata.Annotations[annKedaPausedReplicas]
		if !ok {
			continue
		}
		out = append(out, Finding{
			Rule: "keda-paused", Severity: Context, Subject: wref, Confidence: High,
			Claim:    fmt.Sprintf("KEDA scaling is paused: ScaledObject %s pins this workload at %s replica(s) regardless of load or schedule.", so.Metadata.Name, v),
			Evidence: with(base, Evidence{Object: snapshot.Ref("ScaledObject", so.Metadata.Namespace, so.Metadata.Name), Field: annField(annKedaPausedReplicas), Value: v}),
			Unknowns: copyStrings(unknowns),
		})
	}
	if floors := floorsAt(sos, hpas, n); len(floors) > 0 {
		pods := workloadPods(s, k)
		held := requestsOf(pods)
		for _, f := range floors {
			out = append(out, Finding{
				Rule: "replica-floor", Severity: Context, Subject: wref, Confidence: High,
				Claim:    fmt.Sprintf("The workload sits at its %s floor (%s=%d, replicas=%d). %s will not scale it lower, so its %d pod(s) keep holding %s even when idle.", f.controller, f.param, f.min, n, f.controller, len(pods), cpuMem(held)),
				Meaning:  "These pods exist because of configuration, not load. Lowering the minimum (or scaling to zero off-hours) is what frees this capacity.",
				Evidence: with(base, Evidence{Object: f.object, Field: ".spec." + f.param, Value: f.value}),
				Unknowns: copyStrings(unknowns),
			})
		}
	}
	if len(sos) > 0 && len(hpas) > 0 {
		ev := with(base)
		for _, so := range sos {
			ev = append(ev, Evidence{Object: snapshot.Ref("ScaledObject", so.Metadata.Namespace, so.Metadata.Name), Field: ".spec.scaleTargetRef", Value: wref})
		}
		for _, h := range hpas {
			ev = append(ev, Evidence{Object: hpaRef(h), Field: ".spec.scaleTargetRef", Value: wref})
		}
		out = append(out, Finding{
			Rule: "keda-hpa-conflict", Severity: Warning, Subject: wref, Confidence: High,
			Claim:        "The workload is targeted by a KEDA ScaledObject and by a separate HPA. KEDA manages its own HPA, so two HPAs act on the same replica count and can hold it above what either intends.",
			Evidence:     ev,
			Alternatives: []string{"If the extra HPA was created by KEDA under a non-standard name, this is a false positive; check its ownerReferences."},
			Unknowns:     copyStrings(unknowns),
		})
	}
	return append(out, downscalerFindings(s, w, base, hpas, unknowns)...)
}

// downscalerFindings covers kube-downscaler style annotations on the workload.
func downscalerFindings(s *snapshot.Snapshot, w workloadRef, base []Evidence, hpas []*snapshot.HorizontalPodAutoscaler, unknowns []string) []Finding {
	wl := s.Workload(w.key.Kind, w.key.Namespace, w.key.Name)
	if wl == nil {
		return nil
	}
	wref := w.key.ref()
	ann := wl.Metadata.Annotations
	var out []Finding
	if ann[annDownscalerExclude] == "true" {
		out = append(out, Finding{
			Rule: "downscaler-excluded", Severity: Context, Subject: wref, Confidence: High,
			Claim:    "The workload is excluded from kube-downscaler (downscaler/exclude=true), so scheduled off-hours downscaling skips it.",
			Evidence: with(base, Evidence{Object: wref, Field: annField(annDownscalerExclude), Value: "true"}),
			Unknowns: copyStrings(unknowns),
		})
	}
	v, ok := ann[annDownscalerDowntimeReplicas]
	if !ok {
		return out
	}
	downtime, err := strconv.Atoi(v)
	if err != nil {
		return out
	}
	for _, h := range hpas {
		if _, own := h.Metadata.Annotations[annDownscalerDowntimeReplicas]; own {
			continue
		}
		floor := int32(1)
		if h.Spec.MinReplicas != nil {
			floor = *h.Spec.MinReplicas
		}
		if int(floor) <= downtime {
			continue
		}
		out = append(out, Finding{
			Rule: "downscaler-vs-hpa-floor", Severity: Warning, Subject: wref, Confidence: Medium,
			Claim: fmt.Sprintf("kube-downscaler sets this workload to %d replica(s) during downtime, but HPA %s has minReplicas=%d and scales it back up. Off-hours savings for this workload are capped by the HPA floor.", downtime, h.Metadata.Name, floor),
			Evidence: with(base,
				Evidence{Object: wref, Field: annField(annDownscalerDowntimeReplicas), Value: v},
				Evidence{Object: hpaRef(h), Field: ".spec.minReplicas", Value: itoa(floor)}),
			Unknowns: append(copyStrings(unknowns),
				"kube-downscaler can also lower an HPA's minReplicas when the HPA itself carries downscaler annotations; none were found on this HPA.",
				"Whether the snapshot was taken inside the downtime window is unknown."),
		})
	}
	return out
}

func hpaRef(h *snapshot.HorizontalPodAutoscaler) string {
	return snapshot.Ref("HorizontalPodAutoscaler", h.Metadata.Namespace, h.Metadata.Name)
}

func copyStrings(s []string) []string { return append([]string(nil), s...) }
