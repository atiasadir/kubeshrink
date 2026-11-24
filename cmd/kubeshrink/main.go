// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Command kubeshrink explains why Kubernetes capacity did not go down.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"

	"github.com/atiasadir/kubeshrink/internal/explain"
	"github.com/atiasadir/kubeshrink/internal/live"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

const usage = `kubeshrink - find out why your cluster won't shrink (read-only)

Usage:
  kubeshrink explain node <name>  (--snapshot <path> | --context <name>) [-o text|json]
  kubeshrink explain nodes        (--snapshot <path> | --context <name>) [-o text|json]
  kubeshrink snapshot --context <name> --out <dir>
  kubeshrink rules
  kubeshrink version

Sources:
  --snapshot, -f <path>  YAML/JSON exported with kubectl (a file or a directory)
  --context <name>       a live cluster, read with list calls only; must be named explicitly
  --kubeconfig <path>    kubeconfig file (default: $KUBECONFIG or ~/.kube/config)
  --timeout <duration>   timeout for live reads (default 60s)

KubeShrink never modifies a cluster and never reads Secrets or ConfigMaps.
Installed on PATH as "kubectl-shrink", it also runs as "kubectl shrink".
`

type liveLoader func(ctx context.Context, opts live.Options) (*snapshot.Snapshot, error)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, live.Load))
}

func run(args []string, stdout, stderr io.Writer, loadLive liveLoader) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version":
		fmt.Fprintln(stdout, versionString())
		return 0
	case "rules":
		if err := explain.WriteCatalog(stdout); err != nil {
			return 1
		}
		return 0
	case "explain":
		return runExplain(args[1:], stdout, stderr, loadLive)
	case "snapshot":
		return runSnapshot(args[1:], stdout, stderr, loadLive)
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return 2
}

type options struct {
	snapshot, kubeconfig, context, output, out string
	timeout                                    time.Duration
}

func newFlags(name string, stderr io.Writer, o *options) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.snapshot, "snapshot", "", "snapshot file or directory")
	fs.StringVar(&o.snapshot, "f", "", "shorthand for --snapshot")
	fs.StringVar(&o.context, "context", "", "kubeconfig context to read (live mode)")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	fs.StringVar(&o.output, "output", "text", "output format: text or json")
	fs.StringVar(&o.output, "o", "text", "shorthand for --output")
	fs.StringVar(&o.out, "out", "", "directory for the exported snapshot (snapshot command)")
	fs.DurationVar(&o.timeout, "timeout", 60*time.Second, "timeout for live reads")
	return fs
}

// parseInterspersed lets flags appear before or after positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func loadSource(o *options, stderr io.Writer, loadLive liveLoader) (*snapshot.Snapshot, error) {
	switch {
	case o.snapshot != "" && o.context != "":
		return nil, errors.New("use either --snapshot or --context, not both")
	case o.snapshot != "":
		return snapshot.Load(o.snapshot)
	case o.context != "":
		fmt.Fprintf(stderr, "kubeshrink: reading context %q read-only (list calls only; Secrets and ConfigMaps are never read)\n", o.context)
		ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
		defer cancel()
		return loadLive(ctx, live.Options{Kubeconfig: o.kubeconfig, Context: o.context, SaveDir: o.out})
	}
	return nil, errors.New("a source is required: --snapshot <path> or --context <name> (the current context is never used implicitly)")
}

func runExplain(args []string, stdout, stderr io.Writer, loadLive liveLoader) int {
	var o options
	pos, err := parseInterspersed(newFlags("explain", stderr, &o), args)
	if err != nil {
		return 2
	}
	if o.output != "text" && o.output != "json" {
		fmt.Fprintf(stderr, "error: --output must be text or json, got %q\n", o.output)
		return 2
	}
	oneNode := len(pos) == 2 && pos[0] == "node"
	allNodes := len(pos) == 1 && pos[0] == "nodes"
	if !oneNode && !allNodes {
		fmt.Fprint(stderr, "usage: kubeshrink explain node <name> | kubeshrink explain nodes\n")
		return 2
	}
	s, err := loadSource(&o, stderr, loadLive)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if pos[0] == "nodes" {
		reports := explain.ExplainAllNodes(s)
		if o.output == "json" {
			err = explain.WriteJSON(stdout, reports)
		} else {
			err = explain.WriteSummaryTable(stdout, reports)
		}
	} else {
		r, rerr := explain.ExplainNode(s, pos[1])
		if rerr != nil {
			fmt.Fprintf(stderr, "error: %v\n", rerr)
			return 1
		}
		if o.output == "json" {
			err = explain.WriteJSON(stdout, r)
		} else {
			err = explain.WriteText(stdout, r)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func runSnapshot(args []string, stdout, stderr io.Writer, loadLive liveLoader) int {
	var o options
	pos, err := parseInterspersed(newFlags("snapshot", stderr, &o), args)
	if err != nil {
		return 2
	}
	if len(pos) != 0 || o.context == "" || o.out == "" || o.snapshot != "" {
		fmt.Fprint(stderr, "usage: kubeshrink snapshot --context <name> --out <dir>\n")
		return 2
	}
	s, err := loadSource(&o, stderr, loadLive)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Saved %d nodes, %d pods, %d PDBs, %d HPAs, %d ScaledObjects, %d workloads, %d events to %s\n",
		len(s.Nodes), len(s.Pods), len(s.PDBs), len(s.HPAs), len(s.ScaledObjects), len(s.Workloads), len(s.Events), o.out)
	for _, n := range s.Notes {
		fmt.Fprintf(stdout, "note: %s\n", n)
	}
	return 0
}

func versionString() string {
	v, c, d := version, commit, date
	if info, ok := debug.ReadBuildInfo(); ok && c == "" {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				c = s.Value
			case "vcs.time":
				d = s.Value
			}
		}
	}
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return fmt.Sprintf("kubeshrink %s (commit %s, built %s)", v, c, d)
}
