// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteText renders one report for humans: verdict first, then each finding
// with its claim, evidence, other explanations and unknowns.
func WriteText(w io.Writer, r *NodeReport) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "Node %s: %s\n%s\n", r.Node, strings.ToUpper(r.Verdict), r.Summary)
	if len(r.PodsToMove) > 0 {
		fmt.Fprintf(b, "\nPods that would need to move (%d):\n", len(r.PodsToMove))
		for _, p := range r.PodsToMove {
			fmt.Fprintf(b, "  - %s\n", p)
		}
	}
	if len(r.IgnoredPods) > 0 {
		fmt.Fprintf(b, "\nIgnored pods:\n")
		for _, p := range r.IgnoredPods {
			fmt.Fprintf(b, "  - %s (%s)\n", p.Pod, p.Reason)
		}
	}
	for i, f := range r.Findings {
		fmt.Fprintf(b, "\n[%d] %s  %s  %s\n", i+1, strings.ToUpper(string(f.Severity)), f.Rule, f.Subject)
		fmt.Fprintf(b, "    %s\n", f.Claim)
		if len(f.AppliesTo) > 0 {
			fmt.Fprintf(b, "    Applies to: %s\n", strings.Join(f.AppliesTo, ", "))
		}
		fmt.Fprintf(b, "    Confidence: %s\n    Evidence:\n", f.Confidence)
		for _, e := range f.Evidence {
			fmt.Fprintf(b, "      - %s", e.Object)
			if e.Field != "" {
				fmt.Fprintf(b, " %s", e.Field)
			}
			if e.Value != "" {
				fmt.Fprintf(b, " = %s", e.Value)
			}
			if e.Time != "" {
				fmt.Fprintf(b, " (at %s)", e.Time)
			}
			b.WriteString("\n")
		}
		writeList(b, "    Other explanations:", f.Alternatives)
		writeList(b, "    Unknowns:", f.Unknowns)
	}
	if len(r.DataNotes) > 0 {
		b.WriteString("\n")
		writeList(b, "Data notes:", r.DataNotes)
	}
	b.WriteString("\n")
	writeList(b, "Limitations:", r.Limitations)
	_, err := io.WriteString(w, b.String())
	return err
}

func writeList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	indent := strings.Repeat(" ", len(title)-len(strings.TrimLeft(title, " "))+2)
	fmt.Fprintf(b, "%s\n", title)
	for _, it := range items {
		fmt.Fprintf(b, "%s- %s\n", indent, it)
	}
}

// WriteSummaryTable renders one line per node, for `explain nodes`.
func WriteSummaryTable(w io.Writer, reports []*NodeReport) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%-28s %-17s %8s %9s %8s %8s %5s\n", "NODE", "VERDICT", "BLOCKERS", "POSSIBLE", "WARNINGS", "CONTEXT", "PODS")
	for _, r := range reports {
		c := r.Counts()
		fmt.Fprintf(b, "%-28s %-17s %8d %9d %8d %8d %5d\n", r.Node, r.Verdict, c[Blocker], c[PossibleBlocker], c[Warning], c[Context], len(r.PodsToMove))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteCatalog lists every rule in plain language, for `kubeshrink rules`.
func WriteCatalog(w io.Writer) error {
	b := &strings.Builder{}
	for _, r := range Catalog {
		fmt.Fprintf(b, "%s\n  %s\n  %s\n\n", r.ID, r.Title, r.Explanation)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
