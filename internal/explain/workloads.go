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

type workloadRef struct {
	key      workloadKey
	inferred bool // Deployment name guessed from the ReplicaSet name
	evidence []Evidence
}

// workloadOf follows ownerReferences from a pod to the object that sets its
// replica count: Pod -> ReplicaSet -> Deployment, or Pod -> StatefulSet.
func (c *nodeContext) workloadOf(p *snapshot.Pod) (workloadRef, bool) {
	cr := p.Metadata.ControllerRef()
	if cr == nil {
		return workloadRef{}, false
	}
	ns := p.Metadata.Namespace
	ev := []Evidence{{Object: podRef(p), Field: ".metadata.ownerReferences (controller)", Value: cr.Kind + "/" + cr.Name}}
	if cr.Kind != "ReplicaSet" {
		return workloadRef{key: workloadKey{cr.Kind, ns, cr.Name}, evidence: ev}, true
	}
	if rs := c.snap.Workload("ReplicaSet", ns, cr.Name); rs != nil {
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

// replicas prefers the workload's .spec.replicas and falls back to counting pods.
func (c *nodeContext) replicas(w workloadRef) (int32, Evidence) {
	k := w.key
	ref := snapshot.Ref(k.Kind, k.Namespace, k.Name)
	if wl := c.snap.Workload(k.Kind, k.Namespace, k.Name); wl != nil && wl.Spec.Replicas != nil {
		return *wl.Spec.Replicas, Evidence{Object: ref, Field: ".spec.replicas", Value: itoa(*wl.Spec.Replicas)}
	}
	var n int32
	for i := range c.snap.Pods {
		p := &c.snap.Pods[i]
		if snapshot.IsTerminal(p) {
			continue
		}
		if pw, ok := c.workloadOf(p); ok && pw.key == k {
			n++
		}
	}
	return n, Evidence{Object: ref, Field: "running pods in snapshot (workload object not found)", Value: itoa(n)}
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

// checkReplicaFloors explains why the pods on this node exist at all: which
// autoscaler floor or schedule keeps their workload from scaling lower.
func checkReplicaFloors(c *nodeContext) []Finding {
	var out []Finding
	seen := map[workloadKey]bool{}
	for _, p := range c.movable {
		w, ok := c.workloadOf(p)
		if !ok || seen[w.key] {
			continue
		}
		seen[w.key] = true
		k := w.key
		wref := snapshot.Ref(k.Kind, k.Namespace, k.Name)
		replicas, repEv := c.replicas(w)
		base := with(w.evidence, repEv)
		var unknowns []string
		if w.inferred {
			unknowns = append(unknowns, "The Deployment name was inferred from the ReplicaSet name because the ReplicaSet is not in the snapshot.")
		}

		var userHPAs []*snapshot.HorizontalPodAutoscaler
		for _, h := range c.snap.HPAsFor(k.Kind, k.Namespace, k.Name) {
			if !isKedaManagedHPA(h) {
				userHPAs = append(userHPAs, h)
			}
		}
		sos := c.snap.ScaledObjectsFor(k.Kind, k.Namespace, k.Name)

		for _, so := range sos {
			soRef := snapshot.Ref("ScaledObject", so.Metadata.Namespace, so.Metadata.Name)
			if v, ok := so.Metadata.Annotations[annKedaPausedReplicas]; ok {
				out = append(out, Finding{
					Rule: "keda-paused", Severity: Context, Subject: wref, Confidence: High,
					Claim:    fmt.Sprintf("KEDA scaling is paused: ScaledObject %s pins this workload at %s replica(s) regardless of load or schedule.", so.Metadata.Name, v),
					Evidence: with(base, Evidence{Object: soRef, Field: annField(annKedaPausedReplicas), Value: v}),
					Unknowns: copyStrings(unknowns),
				})
				continue
			}
			var floor int32
			if so.Spec.MinReplicaCount != nil {
				floor = *so.Spec.MinReplicaCount
			}
			if floor > 0 && replicas <= floor {
				out = append(out, Finding{
					Rule: "replica-floor", Severity: Context, Subject: wref, Confidence: High,
					Claim:    fmt.Sprintf("The workload sits at its KEDA floor (minReplicaCount=%d, replicas=%d). KEDA will not scale it lower, so these pods keep needing capacity even when idle.", floor, replicas),
					Evidence: with(base, Evidence{Object: soRef, Field: ".spec.minReplicaCount", Value: itoa(floor)}),
					Unknowns: copyStrings(unknowns),
				})
			}
		}

		for _, h := range userHPAs {
			floor, val := int32(1), "1 (default)"
			if h.Spec.MinReplicas != nil {
				floor, val = *h.Spec.MinReplicas, itoa(*h.Spec.MinReplicas)
			}
			if replicas > floor {
				continue
			}
			out = append(out, Finding{
				Rule: "replica-floor", Severity: Context, Subject: wref, Confidence: High,
				Claim:    fmt.Sprintf("The workload sits at its HPA floor (minReplicas=%d, replicas=%d). The HPA will not scale it lower, so these pods keep needing capacity even when idle.", floor, replicas),
				Evidence: with(base, Evidence{Object: hpaRef(h), Field: ".spec.minReplicas", Value: val}),
				Unknowns: copyStrings(unknowns),
			})
		}

		if len(sos) > 0 && len(userHPAs) > 0 {
			ev := with(base)
			for _, so := range sos {
				ev = append(ev, Evidence{Object: snapshot.Ref("ScaledObject", so.Metadata.Namespace, so.Metadata.Name), Field: ".spec.scaleTargetRef", Value: wref})
			}
			for _, h := range userHPAs {
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

		out = append(out, c.downscalerFindings(w, wref, base, userHPAs, unknowns)...)
	}
	return out
}

// downscalerFindings covers kube-downscaler style annotations on the workload.
func (c *nodeContext) downscalerFindings(w workloadRef, wref string, base []Evidence, hpas []*snapshot.HorizontalPodAutoscaler, unknowns []string) []Finding {
	wl := c.snap.Workload(w.key.Kind, w.key.Namespace, w.key.Name)
	if wl == nil {
		return nil
	}
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
