// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// labelHostname is the well-known node label that holds the node's name.
const labelHostname = "kubernetes.io/hostname"

// hostnameRejection is an existing node that Karpenter's simulation rules out
// for a pod because of a topology spread on the node hostname.
type hostnameRejection struct {
	node     *snapshot.Node
	index    int // the constraint's index in the pod spec
	tsc      snapshot.TopologySpreadConstraint
	matching int // pods on the node that the constraint selects
}

// checkKarpenterNewNode finds pods that Karpenter's consolidation simulation
// can only place on a new node, although the placement check found room for
// them on existing nodes.
//
// The difference is a topology spread on kubernetes.io/hostname. The
// scheduler measures skew against the existing node with the fewest matching
// pods. Karpenter can create a node, so it takes that minimum as 0 and
// accepts an existing node only while its matching pods, plus the pod itself,
// stay within maxSkew. It treats ScheduleAnyway spreads the same way, because
// it relaxes preferences only when even a new node would not do. A pod with
// no existing node left needs a new one, so Karpenter can only replace the
// node, and it replaces a node only with a cheaper one.
//
// Each pod is checked on its own against the cluster as it is.
func checkKarpenterNewNode(c *nodeContext) []Finding {
	if karpenterPool(c.node) == "" || len(c.movable) == 0 {
		return nil
	}
	sim := newSimulation(c.snap, c.node.Metadata.Name)
	var out []Finding
	for _, p := range c.movable {
		if len(hostnameSpreads(p)) == 0 {
			continue
		}
		if rejected, ok := karpenterRejectsAll(sim, p); ok {
			out = append(out, newNodeFinding(p, rejected))
		}
	}
	return out
}

// karpenterRejectsAll returns the nodes the placement check found room on
// when Karpenter's hostname rule rules out every one of them. It reports
// false when there is no such node, which no-room-elsewhere covers, or when
// Karpenter accepts one.
func karpenterRejectsAll(sim *simulation, p *snapshot.Pod) ([]hostnameRejection, bool) {
	req := snapshot.PodRequests(p)
	var rejected []hostnameRejection
	for _, cand := range sim.nodes {
		if cand.node.Spec.Unschedulable {
			continue
		}
		if kind, _ := sim.whyNotFit(p, req, cand); kind != "" {
			continue
		}
		r, ok := karpenterHostnameRejects(p, cand)
		if !ok {
			return nil, false
		}
		rejected = append(rejected, r)
	}
	return rejected, len(rejected) > 0
}

// hostnameSpreads returns the indexes of the pod's topology spread
// constraints on the node hostname, enforced or not.
func hostnameSpreads(p *snapshot.Pod) []int {
	var out []int
	for i, tsc := range p.Spec.TopologySpreadConstraints {
		if tsc.TopologyKey == labelHostname {
			out = append(out, i)
		}
	}
	return out
}

// karpenterHostnameRejects reports the first hostname spread under which
// Karpenter's simulation rules out the node: the pods there that the
// constraint selects, plus the pod itself when it matches, exceed maxSkew.
func karpenterHostnameRejects(p *snapshot.Pod, c *candidate) (hostnameRejection, bool) {
	for _, i := range hostnameSpreads(p) {
		tsc := p.Spec.TopologySpreadConstraints[i]
		matching := 0
		for _, o := range c.pods {
			if o.Metadata.Namespace == p.Metadata.Namespace && tsc.LabelSelector.Matches(o.Metadata.Labels) {
				matching++
			}
		}
		self := 0
		if tsc.LabelSelector.Matches(p.Metadata.Labels) {
			self = 1
		}
		if matching+self > int(tsc.MaxSkew) {
			return hostnameRejection{node: c.node, index: i, tsc: tsc, matching: matching}, true
		}
	}
	return hostnameRejection{}, false
}

// newNodeFinding reports a pod that Karpenter can only move to a new node.
func newNodeFinding(p *snapshot.Pod, rejected []hostnameRejection) Finding {
	ref, first := podRef(p), rejected[0]
	least := first.matching
	for _, r := range rejected {
		least = min(least, r.matching)
	}
	ev := []Evidence{{
		Object: ref,
		Field:  fmt.Sprintf(".spec.topologySpreadConstraints[%d]", first.index),
		Value:  fmt.Sprintf("topologyKey %s, maxSkew %d, whenUnsatisfiable %s", first.tsc.TopologyKey, first.tsc.MaxSkew, first.tsc.WhenUnsatisfiable),
	}}
	for i, r := range rejected {
		if i == maxRejectionsShown {
			ev = append(ev, Evidence{Object: "cluster", Field: "more nodes like these", Value: fmt.Sprint(len(rejected) - i)})
			break
		}
		ev = append(ev, Evidence{Object: nodeRef(r.node), Field: "pods matching the spread", Value: fmt.Sprint(r.matching)})
	}
	return Finding{
		Rule: "karpenter-needs-new-node", Severity: PossibleBlocker, Subject: ref,
		AppliesTo: []string{Karpenter}, Confidence: Medium,
		Claim: fmt.Sprintf("Karpenter would need a new node for %s. Each of the %d other node(s) the placement check found room on already runs at least %d pod(s) matching its topology spread on %s (maxSkew %d). Karpenter counts a node it could create as running none, so in its simulation one more pod on any of them breaks the spread. It can then only replace this node, and only with a cheaper one.",
			ref, len(rejected), least, labelHostname, first.tsc.MaxSkew),
		Meaning:      "The scheduler would accept the move, but Karpenter's own simulation does not, so the node stays unless a cheaper replacement exists.",
		Evidence:     ev,
		Alternatives: []string{"Karpenter replaces the node when a NodePool allows a cheaper instance type that fits the pods that need the new node."},
		Unknowns:     []string{"KubeShrink does not see instance prices, so it cannot tell whether a cheaper replacement exists."},
	}
}
