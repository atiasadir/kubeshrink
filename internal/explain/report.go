// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Package explain turns a snapshot into evidence-backed findings and reports.
//
// A finding is a claim plus the evidence that supports it, a confidence level,
// competing explanations and the unknowns that could change the conclusion.
// Each finding belongs to a layer, the place where its cause sits:
//
//   - replicas: why the pods exist (HPA and KEDA minimums, scheduled downscalers)
//   - policy: where pods may run and whether they may move
//   - nodes: whether the node autoscaler removes or replaces nodes
package explain

import (
	"fmt"
	"sort"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

type Severity string

const (
	// Blocker: this alone stops the node from being removed.
	Blocker Severity = "blocker"
	// PossibleBlocker: blocks only under configuration the snapshot cannot see.
	PossibleBlocker Severity = "possible-blocker"
	// Warning: a misconfiguration that wastes capacity without blocking removal.
	Warning Severity = "warning"
	// Context: explains why pods exist or must stay apart; not a blocker.
	Context Severity = "context"
)

type Confidence string

const (
	High   Confidence = "high"
	Medium Confidence = "medium"
	Low    Confidence = "low"
)

// Layer is where a finding's cause sits.
type Layer string

const (
	LayerReplicas Layer = "replicas"
	LayerPolicy   Layer = "policy"
	LayerNodes    Layer = "nodes"
)

// Layers lists the layers in the order reports present them, with the
// question each one answers.
var Layers = []struct {
	Layer    Layer
	Question string
}{
	{LayerReplicas, "Why do these pods exist?"},
	{LayerPolicy, "Where may they run, and may they move?"},
	{LayerNodes, "Will the node autoscaler remove or replace the node?"},
}

const (
	CA        = "cluster-autoscaler"
	Karpenter = "karpenter"
)

var bothAutoscalers = []string{CA, Karpenter}

const (
	VerdictBlocked         = "blocked"
	VerdictPossiblyBlocked = "possibly-blocked"
	VerdictNoBlockerFound  = "no-blocker-found"
)

// Evidence points at one field of one object. Every claim cites at least one.
type Evidence struct {
	Object string `json:"object"`
	Field  string `json:"field,omitempty"`
	Value  string `json:"value,omitempty"`
	Time   string `json:"time,omitempty"`
}

type Finding struct {
	ID           string     `json:"id"`
	Rule         string     `json:"rule"`
	Layer        Layer      `json:"layer"`
	Severity     Severity   `json:"severity"`
	Subject      string     `json:"subject"`
	Claim        string     `json:"claim"`
	Meaning      string     `json:"meaning,omitempty"`
	AppliesTo    []string   `json:"appliesTo,omitempty"`
	Confidence   Confidence `json:"confidence"`
	Evidence     []Evidence `json:"evidence"`
	Alternatives []string   `json:"alternatives,omitempty"`
	Unknowns     []string   `json:"unknowns,omitempty"`
}

type IgnoredPod struct {
	Pod    string `json:"pod"`
	Reason string `json:"reason"`
}

// NodeReport answers "why can't this node be removed?".
type NodeReport struct {
	Node        string             `json:"node"`
	Pool        string             `json:"pool"`
	ManagedBy   string             `json:"managedBy,omitempty"`
	Verdict     string             `json:"verdict"`
	Summary     string             `json:"summary"`
	Allocatable snapshot.Resources `json:"allocatable"`
	Requested   snapshot.Resources `json:"requested"`
	PodsToMove  []string           `json:"podsToMove"`
	IgnoredPods []IgnoredPod       `json:"ignoredPods,omitempty"`
	Findings    []Finding          `json:"findings"`
	DataNotes   []string           `json:"dataNotes,omitempty"`
	Limitations []string           `json:"limitations"`
}

// Limitations are printed with every report so nobody mistakes a heuristic for a guarantee.
var Limitations = []string{
	"Snapshot heuristic, not a scheduler simulation: placement checks cover node selectors, required node affinity, taints, required pod (anti-)affinity, topology spread and host ports, but not volume zones, priorities, preemption or preferred rules.",
	"Autoscaler settings are read where the cluster exposes them (Karpenter NodePools and NodeClaims), not Cluster Autoscaler flags or node group minimums.",
	"One point in time: PDB status, replica counts and events change.",
	"Packing is measured with resource requests, not real usage.",
	"Read-only: KubeShrink never modifies a cluster.",
}

var (
	severityOrder = map[Severity]int{Blocker: 0, PossibleBlocker: 1, Warning: 2, Context: 3}
	layerOrder    = map[Layer]int{LayerReplicas: 0, LayerPolicy: 1, LayerNodes: 2}
)

// finalize fills in each finding's layer from the catalog, sorts the findings
// and numbers them F1, F2, ... in that order. byLayer sorts by layer first,
// for reports that print one section per layer.
func finalize(fs []Finding, byLayer bool) []Finding {
	for i := range fs {
		if fs[i].Layer == "" {
			fs[i].Layer = ruleLayer(fs[i].Rule)
		}
	}
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if byLayer && a.Layer != b.Layer {
			return layerOrder[a.Layer] < layerOrder[b.Layer]
		}
		if a.Severity != b.Severity {
			return severityOrder[a.Severity] < severityOrder[b.Severity]
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Rule < b.Rule
	})
	for i := range fs {
		fs[i].ID = fmt.Sprintf("F%d", i+1)
	}
	return fs
}

func countBySeverity(fs []Finding) map[Severity]int {
	counts := map[Severity]int{}
	for _, f := range fs {
		counts[f.Severity]++
	}
	return counts
}

// Counts returns the number of findings per severity.
func (r *NodeReport) Counts() map[Severity]int { return countBySeverity(r.Findings) }

func addNote(notes *[]string, note string) {
	for _, n := range *notes {
		if n == note {
			return
		}
	}
	*notes = append(*notes, note)
}
