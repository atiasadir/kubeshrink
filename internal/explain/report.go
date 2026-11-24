// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Package explain turns a snapshot into evidence-backed findings.
//
// A finding is a claim plus the evidence that supports it, a confidence level,
// competing explanations and the unknowns that could change the conclusion.
package explain

type Severity string

const (
	// Blocker: this alone stops the node from being removed.
	Blocker Severity = "blocker"
	// PossibleBlocker: blocks only under configuration the snapshot cannot see.
	PossibleBlocker Severity = "possible-blocker"
	// Warning: a misconfiguration that wastes capacity without blocking removal.
	Warning Severity = "warning"
	// Context: explains why pods exist (e.g. replica floors); not a blocker.
	Context Severity = "context"
)

type Confidence string

const (
	High   Confidence = "high"
	Medium Confidence = "medium"
	Low    Confidence = "low"
)

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

// Evidence points at one field of one object. Every claim must cite at least one.
type Evidence struct {
	Object string `json:"object"`
	Field  string `json:"field,omitempty"`
	Value  string `json:"value,omitempty"`
	Time   string `json:"time,omitempty"`
}

type Finding struct {
	Rule         string     `json:"rule"`
	Severity     Severity   `json:"severity"`
	Subject      string     `json:"subject"`
	Claim        string     `json:"claim"`
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

type NodeReport struct {
	Node        string       `json:"node"`
	ManagedBy   string       `json:"managedBy,omitempty"`
	Verdict     string       `json:"verdict"`
	Summary     string       `json:"summary"`
	PodsToMove  []string     `json:"podsToMove"`
	IgnoredPods []IgnoredPod `json:"ignoredPods,omitempty"`
	Findings    []Finding    `json:"findings"`
	DataNotes   []string     `json:"dataNotes,omitempty"`
	Limitations []string     `json:"limitations"`
}

// Limitations are printed with every report so nobody mistakes a heuristic for a guarantee.
var Limitations = []string{
	"Snapshot heuristic, not a scheduler simulation: pod (anti-)affinity, topology spread, host ports, volume zones, priorities and preemption are not evaluated.",
	"Autoscaler configuration (Cluster Autoscaler flags, Karpenter NodePool budgets and consolidation policy, node group minimum sizes) is not read; it only shows up through events.",
	"One point in time: PDB status, replica counts and events change. Time-window analysis is on the roadmap.",
	"Fit is estimated from resource requests, not real usage.",
	"Read-only: KubeShrink never modifies a cluster.",
}
