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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/atiasadir/kubeshrink/internal/explain"
	"github.com/atiasadir/kubeshrink/internal/live"
	"github.com/atiasadir/kubeshrink/internal/serve"
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
  kubeshrink explain cluster           SOURCE [--top N] [-o text|json]
  kubeshrink explain namespace <name>  SOURCE [-o text|json]   (aliases: ns, team)
  kubeshrink explain node <name>       SOURCE [-o text|json]
  kubeshrink explain nodes             SOURCE [-o text|json]
  kubeshrink snapshot --context <name> --out <dir>
  kubeshrink serve [--context <name>] [--listen :8080] [--interval 5m] [--pool-labels a,b]
                                       in-cluster mode: Prometheus /metrics, /healthz, /readyz;
                                       uses the pod's service account unless --context is given
  kubeshrink rules [-o text|json]
  kubeshrink version

SOURCE is one of:
  --snapshot, -f <path>  YAML/JSON exported with kubectl (a file or a directory)
  --context <name>       a live cluster, read with list calls only; must be named explicitly
    --kubeconfig <path>  kubeconfig file (default: $KUBECONFIG or ~/.kube/config)
    --timeout <d>        timeout for all live reads (default 60s)

Options:
  --top <n>               namespaces in the text cluster view (default 10; 0 lists all)
  --node-autoscaler <a>   auto (default), cluster-autoscaler or none: what manages
                          nodes that carry no Karpenter label

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
		return runRules(args[1:], stdout, stderr)
	case "explain":
		return runExplain(args[1:], stdout, stderr, loadLive)
	case "snapshot":
		return runSnapshot(args[1:], stdout, stderr, loadLive)
	case "serve":
		return runServe(args[1:], stderr, loadLive)
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return 2
}

type options struct {
	snapshot, kubeconfig, context, output, out, nodeAutoscaler string
	timeout                                                    time.Duration
	top                                                        int
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
	fs.IntVar(&o.top, "top", 10, "namespaces in the text cluster view (0 lists all)")
	fs.StringVar(&o.nodeAutoscaler, "node-autoscaler", explain.AutoscalerAuto, "auto, cluster-autoscaler or none")
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

const explainUsage = "usage: kubeshrink explain cluster | namespace <name> | node <name> | nodes\n"

func validOutput(o *options, stderr io.Writer) bool {
	if o.output != "text" && o.output != "json" {
		fmt.Fprintf(stderr, "error: --output must be text or json, got %q\n", o.output)
		return false
	}
	return true
}

func runRules(args []string, stdout, stderr io.Writer) int {
	var o options
	pos, err := parseInterspersed(newFlags("rules", stderr, &o), args)
	if err != nil || len(pos) != 0 || !validOutput(&o, stderr) {
		return 2
	}
	if o.output == "json" {
		err = explain.WriteJSON(stdout, explain.Catalog)
	} else {
		err = explain.WriteCatalog(stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// explainTarget reads what to explain from the positional arguments.
func explainTarget(pos []string) (target, name string, ok bool) {
	switch {
	case len(pos) == 1 && (pos[0] == "cluster" || pos[0] == "nodes"):
		return pos[0], "", true
	case len(pos) == 2 && pos[0] == "node":
		return "node", pos[1], true
	case len(pos) == 2 && (pos[0] == "namespace" || pos[0] == "ns" || pos[0] == "team"):
		return "namespace", pos[1], true
	}
	return "", "", false
}

func runExplain(args []string, stdout, stderr io.Writer, loadLive liveLoader) int {
	var o options
	pos, err := parseInterspersed(newFlags("explain", stderr, &o), args)
	if err != nil || !validOutput(&o, stderr) {
		return 2
	}
	switch o.nodeAutoscaler {
	case explain.AutoscalerAuto, explain.CA, explain.AutoscalerNone:
	default:
		fmt.Fprintf(stderr, "error: --node-autoscaler must be auto, cluster-autoscaler or none, got %q\n", o.nodeAutoscaler)
		return 2
	}
	target, name, ok := explainTarget(pos)
	if !ok {
		fmt.Fprint(stderr, explainUsage)
		return 2
	}
	s, err := loadSource(&o, stderr, loadLive)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	a := explain.NewAnalyzer(s, explain.Options{NodeAutoscaler: o.nodeAutoscaler})
	var report any
	var text func(io.Writer) error
	switch target {
	case "cluster":
		r := a.Cluster()
		report = r
		text = func(w io.Writer) error {
			r.Truncate(o.top)
			return explain.WriteClusterText(w, r)
		}
	case "nodes":
		rs := a.Nodes()
		report, text = rs, func(w io.Writer) error { return explain.WriteSummaryTable(w, rs) }
	case "node":
		r, rerr := a.Node(name)
		if rerr != nil {
			fmt.Fprintf(stderr, "error: %v\n", rerr)
			return 1
		}
		report, text = r, func(w io.Writer) error { return explain.WriteNodeText(w, r) }
	case "namespace":
		r, rerr := a.Namespace(name)
		if rerr != nil {
			fmt.Fprintf(stderr, "error: %v\n", rerr)
			return 1
		}
		report, text = r, func(w io.Writer) error { return explain.WriteNamespaceText(w, r) }
	}
	if o.output == "json" {
		err = explain.WriteJSON(stdout, report)
	} else {
		err = text(stdout)
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
	fmt.Fprintf(stdout, "Saved %d nodes, %d pods, %d PDBs, %d HPAs, %d ScaledObjects, %d workloads, %d NodePools, %d NodeClaims, %d events to %s\n",
		len(s.Nodes), len(s.Pods), len(s.PDBs), len(s.HPAs), len(s.ScaledObjects), len(s.Workloads), len(s.NodePools), len(s.NodeClaims), len(s.Events), o.out)
	for _, n := range s.Notes {
		fmt.Fprintf(stdout, "note: %s\n", n)
	}
	return 0
}

func runServe(args []string, stderr io.Writer, loadLive liveLoader) int {
	var o options
	fs := newFlags("serve", stderr, &o)
	listen := fs.String("listen", ":8080", "address for /metrics, /healthz and /readyz")
	interval := fs.Duration("interval", 5*time.Minute, "time between scans (at least 10s)")
	pools := fs.String("pool-labels", strings.Join(serve.DefaultPoolLabels, ","), "node labels to read the nodepool label from, in order")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	switch {
	case len(pos) != 0 || o.snapshot != "":
		fmt.Fprint(stderr, "usage: kubeshrink serve [--context <name>] [--listen :8080] [--interval 5m] [--pool-labels a,b]\n")
		return 2
	case *interval < 10*time.Second:
		fmt.Fprintf(stderr, "error: --interval must be at least 10s, got %s\n", *interval)
		return 2
	case o.nodeAutoscaler != explain.AutoscalerAuto && o.nodeAutoscaler != explain.CA && o.nodeAutoscaler != explain.AutoscalerNone:
		fmt.Fprintf(stderr, "error: --node-autoscaler must be auto, cluster-autoscaler or none, got %q\n", o.nodeAutoscaler)
		return 2
	}
	var labels []string
	for _, l := range strings.Split(*pools, ",") {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	if o.context == "" && os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		fmt.Fprintln(stderr, "error: serve needs --context outside a cluster; in a pod it uses its service account")
		return 2
	}
	lopts := live.Options{Kubeconfig: o.kubeconfig, Context: o.context, InCluster: o.context == ""}
	v, c, _ := buildInfo()
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := serve.New(serve.Config{
		Listen: *listen, Interval: *interval, Timeout: o.timeout, PoolLabels: labels,
		Options: explain.Options{NodeAutoscaler: o.nodeAutoscaler},
		Load:    func(ctx context.Context) (*snapshot.Snapshot, error) { return loadLive(ctx, lopts) },
		Logger:  logger, Version: v, Revision: c,
	})
	if err := srv.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("stopped", "error", err.Error())
		return 1
	}
	return 0
}

func versionString() string {
	v, c, d := buildInfo()
	return fmt.Sprintf("kubeshrink %s (commit %s, built %s)", v, c, d)
}

func buildInfo() (v, c, d string) {
	v, c, d = version, commit, date
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
	return v, c, d
}
