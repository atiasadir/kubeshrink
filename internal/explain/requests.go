// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"

	"github.com/atiasadir/kubeshrink/internal/capacity"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// oversizeFactor is how far a request must sit from the VPA target before
// KubeShrink reports it: smaller differences are noise.
const oversizeFactor = 1.25

const vpaLimitationsDoc = "https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler#known-limitations"

var sizedResources = []string{"cpu", "memory"}

// updateMode returns the VPA's update mode; Auto is the default.
func updateMode(v *snapshot.VerticalPodAutoscaler) string {
	if v.Spec.UpdatePolicy != nil && v.Spec.UpdatePolicy.UpdateMode != "" {
		return v.Spec.UpdatePolicy.UpdateMode
	}
	return "Auto"
}

func recommendation(v *snapshot.VerticalPodAutoscaler, container string) *snapshot.ContainerRecommendation {
	if v.Status.Recommendation == nil {
		return nil
	}
	for i := range v.Status.Recommendation.ContainerRecommendations {
		if r := &v.Status.Recommendation.ContainerRecommendations[i]; r.ContainerName == container {
			return r
		}
	}
	return nil
}

// containerPolicy returns the resource policy for a container, else the "*" policy.
func containerPolicy(v *snapshot.VerticalPodAutoscaler, container string) *snapshot.VPAContainerPolicy {
	if v.Spec.ResourcePolicy == nil {
		return nil
	}
	var star *snapshot.VPAContainerPolicy
	for i := range v.Spec.ResourcePolicy.ContainerPolicies {
		cp := &v.Spec.ResourcePolicy.ContainerPolicies[i]
		switch cp.ContainerName {
		case container:
			return cp
		case "*":
			star = cp
		}
	}
	return star
}

// controls reports whether the VPA changes the container's request for the resource.
func controls(v *snapshot.VerticalPodAutoscaler, container, resource string) bool {
	if updateMode(v) == "Off" {
		return false
	}
	cp := containerPolicy(v, container)
	if cp == nil || len(cp.ControlledResources) == 0 {
		return cp == nil || cp.Mode != "Off"
	}
	if cp.Mode == "Off" {
		return false
	}
	for _, r := range cp.ControlledResources {
		if r == resource {
			return true
		}
	}
	return false
}

func quantity(m map[string]string, res string) (float64, bool) {
	v, ok := m[res]
	if !ok {
		return 0, false
	}
	q, err := snapshot.ParseQuantity(v)
	return q, err == nil
}

// checkRequestsNode compares the requests of the node's movable pods with
// their VPA recommendations (see requestFindings).
func checkRequestsNode(c *nodeContext) []Finding { return requestFindings(c.snap, c.movable) }

// checkRequestsNS compares the requests of a namespace's pods with their VPA
// recommendations (see requestFindings).
func checkRequestsNS(c *nsContext) []Finding { return requestFindings(c.snap, c.pods) }

// requestFindings compares each workload's requests with its VPA, using one
// of the given pods as the workload's template.
func requestFindings(s *snapshot.Snapshot, pods []*snapshot.Pod) []Finding {
	var out []Finding
	seen := map[workloadKey]bool{}
	for _, p := range pods {
		w, ok := workloadOf(s, p)
		if !ok || seen[w.key] {
			continue
		}
		seen[w.key] = true
		for _, v := range s.VPAsFor(w.key.Kind, w.key.Namespace, w.key.Name) {
			out = append(out, vpaFindings(s, w, p, v)...)
		}
	}
	return out
}

func vpaFindings(s *snapshot.Snapshot, w workloadRef, p *snapshot.Pod, v *snapshot.VerticalPodAutoscaler) []Finding {
	wref := w.key.ref()
	vref := snapshot.Ref("VerticalPodAutoscaler", v.Metadata.Namespace, v.Metadata.Name)
	mode := updateMode(v)
	modeEv := Evidence{Object: vref, Field: ".spec.updatePolicy.updateMode", Value: mode}
	var aboveUpper, aboveTarget, differ, floors []Evidence
	for _, c := range p.Spec.Containers {
		rec := recommendation(v, c.Name)
		if rec == nil {
			continue
		}
		for _, res := range sizedResources {
			rq, ok := quantity(c.Resources.Requests, res)
			if !ok {
				continue
			}
			reqEv := Evidence{Object: podRef(p), Field: fmt.Sprintf(".spec.containers[%s].resources.requests.%s", c.Name, res), Value: c.Resources.Requests[res]}
			recEv := func(kind string, m map[string]string) Evidence {
				return Evidence{Object: vref, Field: fmt.Sprintf(".status.recommendation.containerRecommendations[%s].%s.%s", c.Name, kind, res), Value: m[res]}
			}
			tg, hasTarget := quantity(rec.Target, res)
			if ub, ok := quantity(rec.UpperBound, res); ok && rq > ub {
				aboveUpper = append(aboveUpper, reqEv, recEv("upperBound", rec.UpperBound))
			} else if hasTarget && rq > tg*oversizeFactor {
				aboveTarget = append(aboveTarget, reqEv, recEv("target", rec.Target))
			}
			if hasTarget && (rq > tg*oversizeFactor || rq*oversizeFactor < tg) {
				differ = append(differ, reqEv, recEv("target", rec.Target))
			}
			cp := containerPolicy(v, c.Name)
			if cp == nil || !hasTarget {
				continue
			}
			if mn, ok := quantity(cp.MinAllowed, res); ok && mn >= tg {
				if un, ok := quantity(rec.UncappedTarget, res); ok && un < mn {
					floors = append(floors,
						Evidence{Object: vref, Field: fmt.Sprintf(".spec.resourcePolicy.containerPolicies[%s].minAllowed.%s", cp.ContainerName, res), Value: cp.MinAllowed[res]},
						recEv("uncappedTarget", rec.UncappedTarget))
				}
			}
		}
	}
	var out []Finding
	if len(aboveUpper)+len(aboveTarget) > 0 {
		conf := Medium
		if len(aboveUpper) > 0 {
			conf = High
		}
		out = append(out, Finding{
			Rule: "requests-above-vpa", Severity: Warning, Subject: wref, Confidence: conf,
			Claim:    fmt.Sprintf("Pods of this workload request more than VPA %s recommends: %d request(s) above its upper bound and %d more than 25%% above its target. %s", v.Metadata.Name, len(aboveUpper)/2, len(aboveTarget)/2, modeSentence(mode)),
			Meaning:  "Each pod reserves more than it is likely to use, so fewer pods fit on a node and nodes look full on paper.",
			Evidence: with(append(aboveUpper, aboveTarget...), modeEv),
			Unknowns: []string{"VPA recommends from recent usage; a workload with rare peaks may still need its headroom."},
		})
	}
	if (mode == "Off" || mode == "Initial") && len(differ) > 0 {
		f := Finding{
			Rule: "vpa-not-applied", Severity: Warning, Subject: wref, Confidence: High,
			Claim:    fmt.Sprintf("VPA %s recommends different requests, but its update mode is Off, so nothing applies them.", v.Metadata.Name),
			Meaning:  "The right size is known but not applied.",
			Evidence: with([]Evidence{modeEv}, differ...),
		}
		if mode == "Initial" {
			f.Confidence = Medium
			f.Claim = fmt.Sprintf("VPA %s sets requests only when pods are created (mode Initial), and the running pods still differ from its target.", v.Metadata.Name)
			f.Unknowns = []string{"Pod creation times are not in the snapshot; pods created after the recommendation changed would already carry it."}
		}
		out = append(out, f)
	}
	out = append(out, vpaHPAConflict(s, w, p, v, modeEv)...)
	if len(floors) > 0 {
		out = append(out, Finding{
			Rule: "vpa-min-allowed", Severity: Context, Subject: wref, Confidence: High,
			Claim:    fmt.Sprintf("VPA %s's minAllowed keeps its recommendation above what usage suggests: the uncapped target is lower.", v.Metadata.Name),
			Meaning:  "A configured floor, not usage, sets these requests.",
			Evidence: floors,
		})
	}
	return out
}

func modeSentence(mode string) string {
	switch mode {
	case "Off":
		return "The VPA is in mode Off, so it only recommends."
	case "Initial":
		return "The VPA sets requests only when pods are created (mode Initial)."
	}
	return fmt.Sprintf("The VPA is in mode %s and should resize these pods; until it does, they keep their current requests.", mode)
}

// vpaHPAConflict finds HPAs that scale the workload on CPU or memory
// utilization while the VPA changes the same requests.
func vpaHPAConflict(s *snapshot.Snapshot, w workloadRef, p *snapshot.Pod, v *snapshot.VerticalPodAutoscaler, modeEv Evidence) []Finding {
	if updateMode(v) == "Off" {
		return nil
	}
	var out []Finding
	for _, h := range s.HPAsFor(w.key.Kind, w.key.Namespace, w.key.Name) {
		for i, m := range h.Spec.Metrics {
			var res, container, field string
			switch {
			case m.Type == "Resource" && m.Resource != nil:
				res, field = m.Resource.Name, fmt.Sprintf(".spec.metrics[%d].resource.name", i)
			case m.Type == "ContainerResource" && m.ContainerResource != nil:
				res, container, field = m.ContainerResource.Name, m.ContainerResource.Container, fmt.Sprintf(".spec.metrics[%d].containerResource.name", i)
			default:
				continue
			}
			controlled := false
			for _, c := range p.Spec.Containers {
				if (container == "" || c.Name == container) && controls(v, c.Name, res) {
					controlled = true
				}
			}
			if !controlled {
				continue
			}
			out = append(out, Finding{
				Rule: "vpa-hpa-conflict", Severity: Warning, Subject: w.key.ref(), Confidence: High,
				Claim:        fmt.Sprintf("HPA %s scales this workload on %s utilization while VPA %s (mode %s) changes the %s requests. Utilization is usage divided by requests, so every resize moves the HPA's signal and the two can work against each other.", h.Metadata.Name, res, v.Metadata.Name, updateMode(v), res),
				Meaning:      "The VPA project lists this combination as a known limitation: " + vpaLimitationsDoc,
				Evidence:     []Evidence{{Object: hpaRef(h), Field: field, Value: res}, modeEv},
				Alternatives: []string{"Scaling the HPA on custom or external metrics, with the VPA on CPU and memory, is the supported combination."},
			})
			break
		}
	}
	return out
}

// WorkloadSize is one workload's requests measured against its VPA.
type WorkloadSize struct {
	Workload          string `json:"workload"`
	Namespace         string `json:"namespace"`
	HasVPA            bool   `json:"hasVPA"`
	HasRecommendation bool   `json:"hasRecommendation"`
	Oversized         bool   `json:"oversized"`
}

// Sizes measures every long-running workload (not Jobs, DaemonSets or static
// pods) against its VPA. Workloads without a recommendation are unknowns.
func (a *Analyzer) Sizes() []WorkloadSize {
	s := a.snap
	out := []WorkloadSize{}
	for _, w := range a.workloads() {
		if w.key.Kind == "Job" {
			continue
		}
		ws := WorkloadSize{Workload: w.key.ref(), Namespace: w.key.Namespace}
		pods := workloadPods(s, w.key)
		for _, v := range s.VPAsFor(w.key.Kind, w.key.Namespace, w.key.Name) {
			ws.HasVPA = true
			if v.Status.Recommendation != nil && len(v.Status.Recommendation.ContainerRecommendations) > 0 {
				ws.HasRecommendation = true
			}
			if len(pods) == 0 {
				continue
			}
			for _, f := range vpaFindings(s, w, pods[0], v) {
				if f.Rule == "requests-above-vpa" {
					ws.Oversized = true
				}
			}
		}
		out = append(out, ws)
	}
	return out
}

// atVPATargets returns a copy of the snapshot in which every container with
// a VPA target requests that target (never the lower bound), how many
// containers changed, and how much the pods requested above their targets.
func atVPATargets(s *snapshot.Snapshot) (*snapshot.Snapshot, int, capacity.Amount) {
	c := *s
	c.Pods = append([]snapshot.Pod(nil), s.Pods...)
	changed := 0
	var above capacity.Amount
	for i := range c.Pods {
		p := &c.Pods[i]
		if snapshot.IsTerminal(p) {
			continue
		}
		w, ok := workloadOf(s, &s.Pods[i])
		if !ok {
			continue
		}
		vpas := s.VPAsFor(w.key.Kind, w.key.Namespace, w.key.Name)
		if len(vpas) == 0 {
			continue
		}
		ctrs := append([]snapshot.Container(nil), p.Spec.Containers...)
		for j := range ctrs {
			rec := recommendation(vpas[0], ctrs[j].Name)
			if rec == nil || len(rec.Target) == 0 {
				continue
			}
			req := map[string]string{}
			for k, v := range ctrs[j].Resources.Requests {
				req[k] = v
			}
			for _, res := range sizedResources {
				tq, ok := quantity(rec.Target, res)
				if !ok {
					continue
				}
				if rq, ok := quantity(req, res); ok && rq > tq {
					if res == "cpu" {
						above.MilliCPU += (rq - tq) * 1000
					} else {
						above.MemoryBytes += rq - tq
					}
				}
				req[res] = rec.Target[res]
			}
			ctrs[j].Resources.Requests = req
			changed++
		}
		p.Spec.Containers = ctrs
	}
	return &c, changed, above
}
