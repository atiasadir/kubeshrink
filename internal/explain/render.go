// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/atiasadir/kubeshrink/internal/capacity"
)

func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteNodeText renders one node report for humans: the verdict first, then
// the findings by layer, each with its claim, evidence, other explanations
// and unknowns.
func WriteNodeText(w io.Writer, r *NodeReport) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "Node %s: %s\n", r.Node, strings.ToUpper(r.Verdict))
	if r.Pool != "" {
		fmt.Fprintf(b, "Pool: %s\n", r.Pool)
	}
	if r.ManagedBy != "" {
		fmt.Fprintf(b, "Managed by: %s\n", r.ManagedBy)
	}
	fmt.Fprintf(b, "Requested: %s of %s allocatable\n", shortAmount(amountOf(r.Requested)), shortAmount(amountOf(r.Allocatable)))
	fmt.Fprintf(b, "%s\n", r.Summary)
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
	writeFindings(b, r.Findings)
	writeTail(b, r.DataNotes, r.Limitations)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeFindings prints the findings one layer at a time, each layer under
// the question it answers.
func writeFindings(b *strings.Builder, fs []Finding) {
	known := map[Layer]bool{}
	for _, l := range Layers {
		known[l.Layer] = true
		header := false
		for _, f := range fs {
			if f.Layer != l.Layer {
				continue
			}
			if !header {
				fmt.Fprintf(b, "\n== %s: %s\n", strings.ToUpper(string(l.Layer)), l.Question)
				header = true
			}
			writeFinding(b, f)
		}
	}
	for _, f := range fs {
		if !known[f.Layer] {
			writeFinding(b, f)
		}
	}
}

func writeFinding(b *strings.Builder, f Finding) {
	fmt.Fprintf(b, "\n[%s] %s  %s  %s\n", f.ID, strings.ToUpper(string(f.Severity)), f.Rule, f.Subject)
	fmt.Fprintf(b, "    %s\n", f.Claim)
	if f.Meaning != "" {
		fmt.Fprintf(b, "    What it means: %s\n", f.Meaning)
	}
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

func writeTail(b *strings.Builder, notes, limitations []string) {
	if len(notes) > 0 {
		b.WriteString("\n")
		writeList(b, "Data notes:", notes)
	}
	b.WriteString("\n")
	writeList(b, "Limitations:", limitations)
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

// cores renders millicores as cores with one decimal, for columns that mix sizes.
func cores(m float64) string { return fmt.Sprintf("%.1f", m/1000) }

func shortAmount(a capacity.Amount) string {
	return cpuStr(a.MilliCPU) + " CPU, " + memStr(a.MemoryBytes)
}

func table(b *strings.Builder) *tabwriter.Writer { return tabwriter.NewWriter(b, 0, 0, 2, ' ', 0) }

// WriteSummaryTable renders one line per node, for `explain nodes`.
func WriteSummaryTable(w io.Writer, reports []*NodeReport) error {
	b := &strings.Builder{}
	tw := table(b)
	fmt.Fprintln(tw, "NODE\tPOOL\tMANAGED BY\tVERDICT\tBLOCKERS\tPOSSIBLE\tWARNINGS\tCONTEXT\tPODS")
	for _, r := range reports {
		c := r.Counts()
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n", r.Node, r.Pool, r.ManagedBy, r.Verdict, c[Blocker], c[PossibleBlocker], c[Warning], c[Context], len(r.PodsToMove))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteNamespaceText renders a team view: what the namespace requests and
// holds, the nodes it uses, and the findings by layer.
func WriteNamespaceText(w io.Writer, r *NamespaceReport) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "Namespace %s\n%s\n", r.Namespace, r.Summary)
	if len(r.Nodes) > 0 {
		b.WriteString("\nNodes it uses (idle share: its part of each node's unrequested capacity, split by requests)\n")
		tw := table(b)
		fmt.Fprintln(tw, "  NODE\tPOOL\tREQUESTED\tIDLE SHARE\tNODE PACKING CPU/MEM\tALONE\tVERDICT")
		for _, n := range r.Nodes {
			alone := "no"
			if n.Alone {
				alone = "yes"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s / %s\t%s\t%s\n", n.Node, n.Pool, shortAmount(n.Requested), shortAmount(n.IdleShare), pct(n.NodePackingCPU), pct(n.NodePackingMemory), alone, n.Verdict)
		}
		_ = tw.Flush()
	}
	writeFindings(b, r.Findings)
	if r.OtherTenantFindings > 0 {
		fmt.Fprintf(b, "\n%d more finding(s) on these nodes are about other namespaces' pods; run explain node for the full picture.\n", r.OtherTenantFindings)
	}
	writeTail(b, r.DataNotes, r.Limitations)
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteClusterText renders the cluster view.
func WriteClusterText(w io.Writer, r *ClusterReport) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "Cluster\n%s\n", r.Summary)
	if r.TakenAt != "" || r.ServerVersion != "" {
		fmt.Fprintf(b, "Snapshot: taken %s, Kubernetes %s\n", orUnknown(r.TakenAt), orUnknown(r.ServerVersion))
	}
	b.WriteString("\nPools (needed: nodes the requests would fit on if placement rules did not exist; a lower bound, not a plan)\n")
	tw := table(b)
	fmt.Fprintln(tw, "  POOL\tNODES\tNEEDED\tEMPTY\tCPU REQUESTED/ALLOCATABLE\tMEMORY REQUESTED/ALLOCATABLE")
	for _, p := range r.Pools {
		cpu, mem := capacity.Ratio(p.Requested, p.Allocatable)
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%s/%s (%s)\t%s/%s (%s)\n", p.Pool, p.Nodes, p.MinNodes, p.EmptyNodes,
			cores(p.Requested.MilliCPU), cores(p.Allocatable.MilliCPU), pct(cpu), memStr(p.Requested.MemoryBytes), memStr(p.Allocatable.MemoryBytes), pct(mem))
	}
	_ = tw.Flush()
	managers := make([]string, 0, len(r.ManagedBy))
	for m := range r.ManagedBy {
		managers = append(managers, m)
	}
	sort.Strings(managers)
	for i, m := range managers {
		managers[i] = fmt.Sprintf("%s on %d node(s)", m, r.ManagedBy[m])
	}
	fmt.Fprintf(b, "\nNode autoscaler: %s\n", strings.Join(managers, ", "))
	for _, np := range r.NodePools {
		fmt.Fprintf(b, "  NodePool %s (%d node(s)): %s\n", np.Name, np.Nodes, np.Settings)
	}
	b.WriteString("\nWhat keeps nodes (rules that block or may block removal, most nodes first)\n")
	if len(r.TopReasons) == 0 {
		b.WriteString("  No rule found a reason any node must stay.\n")
	}
	for _, rs := range r.TopReasons {
		fmt.Fprintf(b, "  %s [%s, %s] on %d node(s): %s\n      %s\n", rs.Rule, rs.Layer, rs.Severity, len(rs.Nodes), joinMax(rs.Nodes, 6), rs.Example)
	}
	b.WriteString("\nWhy pods exist: workloads held at a floor\n")
	if len(r.ReplicaFloors) == 0 {
		b.WriteString("  No workload sits at an HPA or KEDA minimum.\n")
	}
	for _, g := range r.ReplicaFloors {
		fmt.Fprintf(b, "  %d workload(s) %s: %d pod(s) requesting %s\n", g.Workloads, floorPhrase(g), g.Pods, shortAmount(amountOf(g.Requested)))
	}
	if len(r.Namespaces) > 0 {
		b.WriteString("\nNamespaces by the idle capacity they hold\n")
		tw = table(b)
		fmt.Fprintln(tw, "  NAMESPACE\tPODS\tNODES\tREQUESTED\tIDLE SHARE\tPACKING CPU/MEM\tPINNED")
		for _, n := range r.Namespaces {
			fmt.Fprintf(tw, "  %s\t%d\t%d\t%s\t%s\t%s / %s\t%d\n", n.Namespace, n.Pods, n.Nodes, shortAmount(n.Requested), shortAmount(n.IdleShare), pct(n.PackingCPU), pct(n.PackingMemory), n.Pinned)
		}
		_ = tw.Flush()
		if r.OmittedNamespaces > 0 {
			fmt.Fprintf(b, "  ... and %d more (use --top 0 to list all)\n", r.OmittedNamespaces)
		}
	}
	writeTail(b, r.DataNotes, r.Limitations)
	_, err := io.WriteString(w, b.String())
	return err
}

func floorPhrase(g FloorGroup) string {
	switch g.Controller {
	case "keda":
		return fmt.Sprintf("at KEDA minReplicaCount %d", g.Min)
	case "hpa":
		return fmt.Sprintf("at HPA minReplicas %d", g.Min)
	case "keda-paused":
		return fmt.Sprintf("paused by KEDA at %d replica(s)", g.Min)
	}
	return fmt.Sprintf("held by %s at %d", g.Controller, g.Min)
}

func joinMax(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(" and %d more", len(items)-n)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// WriteCatalog lists every rule in plain language, by layer, for `kubeshrink rules`.
func WriteCatalog(w io.Writer) error {
	b := &strings.Builder{}
	for _, l := range Layers {
		fmt.Fprintf(b, "== %s: %s\n\n", strings.ToUpper(string(l.Layer)), l.Question)
		for _, r := range Catalog {
			if r.Layer == l.Layer {
				fmt.Fprintf(b, "%s\n  %s\n  %s\n\n", r.ID, r.Title, r.Explanation)
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
