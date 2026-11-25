// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"strings"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// checkKarpenterNodePool reads the node's Karpenter NodePool and NodeClaim
// for settings that keep the node: consolidating only empty nodes,
// consolidation turned off, a zero disruption budget, or a wait that has not
// elapsed yet.
func checkKarpenterNodePool(c *nodeContext) []Finding {
	poolName := karpenterPool(c.node)
	if poolName == "" {
		return nil
	}
	np := c.snap.NodePool(poolName)
	if np == nil {
		if len(c.snap.NodePools) == 0 {
			c.note("Karpenter NodePools were not in the snapshot, so consolidation settings and disruption budgets were not checked.")
		} else {
			c.note(fmt.Sprintf("NodePool %s, named by a node's Karpenter label, was not in the snapshot.", poolName))
		}
		return checkNodeClaim(c, nil)
	}
	nref, pref := nodeRef(c.node), snapshot.Ref("NodePool", "", np.Metadata.Name)
	d := np.Spec.Disruption
	var out []Finding
	if strings.EqualFold(d.ConsolidationPolicy, "WhenEmpty") && len(c.movable) > 0 {
		out = append(out, Finding{
			Rule: "karpenter-consolidation-policy", Severity: Blocker, Subject: nref,
			AppliesTo: []string{Karpenter}, Confidence: High,
			Claim: fmt.Sprintf("NodePool %s only consolidates empty nodes (consolidationPolicy WhenEmpty). This node runs %d pod(s) that would need to move, so Karpenter will not remove or replace it for being underused.", np.Metadata.Name, len(c.movable)),
			Evidence: []Evidence{
				{Object: pref, Field: ".spec.disruption.consolidationPolicy", Value: d.ConsolidationPolicy},
				{Object: nref, Field: fmt.Sprintf(".metadata.labels[%q]", labelKarpenterNodePool), Value: poolName},
			},
			Alternatives: []string{"Expiry or drift can still replace the node, depending on the NodePool's other settings."},
		})
	}
	if strings.EqualFold(d.ConsolidateAfter, "Never") {
		out = append(out, Finding{
			Rule: "karpenter-consolidation-disabled", Severity: Blocker, Subject: nref,
			AppliesTo: []string{Karpenter}, Confidence: High,
			Claim:    fmt.Sprintf("NodePool %s sets consolidateAfter: Never, which turns consolidation off: Karpenter never removes or replaces its nodes for being empty or underused.", np.Metadata.Name),
			Evidence: []Evidence{{Object: pref, Field: ".spec.disruption.consolidateAfter", Value: d.ConsolidateAfter}},
		})
	}
	for i, b := range d.Budgets {
		if !blocksConsolidation(b) {
			continue
		}
		field := fmt.Sprintf(".spec.disruption.budgets[%d]", i)
		f := Finding{
			Rule: "karpenter-budget", Subject: nref, AppliesTo: []string{Karpenter},
			Evidence: []Evidence{{Object: pref, Field: field + ".nodes", Value: b.Nodes}},
		}
		if len(b.Reasons) > 0 {
			f.Evidence = append(f.Evidence, Evidence{Object: pref, Field: field + ".reasons", Value: strings.Join(b.Reasons, ", ")})
		}
		if b.Schedule == "" {
			f.Severity, f.Confidence = Blocker, High
			f.Claim = fmt.Sprintf("NodePool %s has a disruption budget of 0 nodes with no schedule, so Karpenter never consolidates its nodes.", np.Metadata.Name)
		} else {
			f.Severity, f.Confidence = PossibleBlocker, Medium
			f.Claim = fmt.Sprintf("NodePool %s blocks consolidation during a scheduled window (schedule %q, duration %s).", np.Metadata.Name, b.Schedule, b.Duration)
			f.Evidence = append(f.Evidence, Evidence{Object: pref, Field: field + ".schedule", Value: b.Schedule})
			f.Unknowns = []string{"Whether the window was active when the snapshot was taken was not evaluated."}
		}
		out = append(out, f)
	}
	return append(out, checkNodeClaim(c, np)...)
}

// blocksConsolidation reports whether a budget allows no disruptions for
// consolidation: 0 nodes for every reason, or for Underutilized or Empty.
func blocksConsolidation(b snapshot.Budget) bool {
	if n := strings.TrimSpace(b.Nodes); n != "0" && n != "0%" {
		return false
	}
	if len(b.Reasons) == 0 {
		return true
	}
	for _, r := range b.Reasons {
		if strings.EqualFold(r, "Underutilized") || strings.EqualFold(r, "Empty") {
			return true
		}
	}
	return false
}

// checkNodeClaim reports when Karpenter has not marked the node consolidatable
// yet: the NodeClaim's Consolidatable condition is False, or it is missing
// while other NodeClaims carry it (older Karpenter versions have none at all).
func checkNodeClaim(c *nodeContext, np *snapshot.NodePool) []Finding {
	nc := c.snap.NodeClaimForNode(c.node.Metadata.Name)
	if nc == nil {
		return nil
	}
	ref := snapshot.Ref("NodeClaim", "", nc.Metadata.Name)
	cond := consolidatable(nc)
	var ev []Evidence
	conf := Medium
	switch {
	case cond != nil && cond.Status == "False":
		ev = []Evidence{{Object: ref, Field: ".status.conditions[Consolidatable].status", Value: "False", Time: cond.LastTransitionTime}}
	case cond == nil && othersCarryConsolidatable(c.snap, nc):
		ev = []Evidence{{Object: ref, Field: ".status.conditions[Consolidatable]", Value: "absent, while other NodeClaims carry it"}}
		conf = Low
	default:
		return nil
	}
	claim := "Karpenter has not marked this node consolidatable yet, so it does not consider removing or replacing it."
	if np != nil && np.Spec.Disruption.ConsolidateAfter != "" {
		claim += fmt.Sprintf(" NodePool %s waits consolidateAfter (%s) after pods were last added to or removed from a node.", np.Metadata.Name, np.Spec.Disruption.ConsolidateAfter)
		ev = append(ev, Evidence{Object: snapshot.Ref("NodePool", "", np.Metadata.Name), Field: ".spec.disruption.consolidateAfter", Value: np.Spec.Disruption.ConsolidateAfter})
	}
	return []Finding{{
		Rule: "karpenter-not-consolidatable-yet", Severity: Context, Subject: nodeRef(c.node),
		AppliesTo: []string{Karpenter}, Confidence: conf,
		Claim: claim, Evidence: ev,
		Unknowns: []string{"Frequent pod churn on the node keeps restarting this wait."},
	}}
}

func consolidatable(nc *snapshot.NodeClaim) *snapshot.Condition {
	for i := range nc.Status.Conditions {
		if nc.Status.Conditions[i].Type == "Consolidatable" {
			return &nc.Status.Conditions[i]
		}
	}
	return nil
}

func othersCarryConsolidatable(s *snapshot.Snapshot, self *snapshot.NodeClaim) bool {
	for i := range s.NodeClaims {
		if nc := &s.NodeClaims[i]; nc.Metadata.Name != self.Metadata.Name && consolidatable(nc) != nil {
			return true
		}
	}
	return false
}

// eventMeanings explains common autoscaler messages in plain language.
var eventMeanings = []struct{ match, meaning string }{
	{"can't replace with a cheaper node", "Karpenter considered this node and found no cheaper replacement. Karpenter tries deleting a node before replacing it, so this usually also means its own simulation could not fit all of the node's pods onto the other nodes."},
	{"prevents pod evictions", "A PodDisruptionBudget has no room, so Karpenter cannot evict one of the node's pods."},
	{"do-not-disrupt", "A karpenter.sh/do-not-disrupt annotation blocks voluntary disruption."},
	{"blocking budget", "A NodePool disruption budget allows no more disruptions right now."},
}

func eventMeaning(message string) string {
	m := strings.ToLower(message)
	for _, e := range eventMeanings {
		if strings.Contains(m, e.match) {
			return e.meaning
		}
	}
	return ""
}

// annotateKarpenterEvents adds what KubeShrink's own placement check found to
// Karpenter's "cannot consolidate" events, so a disagreement is visible.
func annotateKarpenterEvents(r *NodeReport, movable int) {
	if movable == 0 {
		return
	}
	for _, f := range r.Findings {
		if f.Rule == "no-room-elsewhere" {
			return
		}
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.Rule == "autoscaler-event" && f.Severity == PossibleBlocker {
			f.Unknowns = append(f.Unknowns, "KubeShrink's placement check found room on other nodes for every pod here, so Karpenter is likely weighing something KubeShrink does not model, such as volume zones, pod priority or constraints this snapshot does not carry.")
		}
	}
}

// poolSettings summarizes a NodePool's disruption settings on one line.
func poolSettings(np *snapshot.NodePool) string {
	d := np.Spec.Disruption
	var parts []string
	if d.ConsolidationPolicy != "" {
		parts = append(parts, "consolidationPolicy="+d.ConsolidationPolicy)
	}
	if d.ConsolidateAfter != "" {
		parts = append(parts, "consolidateAfter="+d.ConsolidateAfter)
	}
	var budgets []string
	for _, b := range d.Budgets {
		s := b.Nodes
		if len(b.Reasons) > 0 {
			s += " for " + strings.Join(b.Reasons, "/")
		}
		if b.Schedule != "" {
			s += " during " + b.Schedule
		}
		budgets = append(budgets, s)
	}
	if len(budgets) > 0 {
		parts = append(parts, "budgets="+strings.Join(budgets, "; "))
	}
	if len(np.Spec.Limits) > 0 {
		parts = append(parts, "limits="+snapshot.FormatResourceList(np.Spec.Limits))
	}
	return strings.Join(parts, " ")
}
