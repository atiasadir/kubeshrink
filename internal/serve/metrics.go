// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Package serve runs KubeShrink inside a cluster: it scans on an interval
// and exports what it found as low-cardinality Prometheus metrics. No series
// carries a pod, workload or node name.
package serve

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/atiasadir/kubeshrink/internal/explain"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// DefaultPoolLabels are read, in order, for the nodepool label of kubeshrink_nodes.
var DefaultPoolLabels = []string{
	"karpenter.sh/nodepool",
	"kubernetes.azure.com/agentpool",
	"eks.amazonaws.com/nodegroup",
	"cloud.google.com/gke-nodepool",
}

var (
	descNodes = prometheus.NewDesc("kubeshrink_nodes",
		"Nodes per verdict (blocked, possibly-blocked, no-blocker-found) and node pool, from the first configured pool label that is set.",
		[]string{"verdict", "nodepool"}, nil)
	descNodesNeeded = prometheus.NewDesc("kubeshrink_nodes_needed",
		"Request-based estimate of how many nodes the pods would fit on, ignoring placement rules: a lower bound, not a plan. basis is requests, or vpa-target where VPA targets replace requests.",
		[]string{"basis"}, nil)
	descBlocked = prometheus.NewDesc("kubeshrink_blocked_nodes",
		"Nodes with at least one finding of a rule (reason, the catalog rule ID) at a severity (blocker or possible-blocker). A node counts under every reason it has.",
		[]string{"reason", "severity"}, nil)
	descPinned = prometheus.NewDesc("kubeshrink_pinned_workloads",
		"Workloads held at their HPA or KEDA minimum (controller hpa or keda) or at a paused KEDA replica count (keda-paused).",
		[]string{"controller", "namespace"}, nil)
	descOversized = prometheus.NewDesc("kubeshrink_oversized_workloads",
		"Long-running workloads whose requests are above their VPA recommendation.",
		[]string{"namespace"}, nil)
	descWithoutVPA = prometheus.NewDesc("kubeshrink_workloads_without_vpa",
		"Long-running workloads without a VPA recommendation, whose size KubeShrink does not judge.",
		[]string{"namespace"}, nil)
)

type pair [2]string

// observation is everything one scan measured, swapped in as a whole so a
// scrape never sees half of one scan and half of the next.
type observation struct {
	nodes      map[pair]float64
	needed     map[string]float64
	blocked    map[pair]float64
	pinned     map[pair]float64
	oversized  map[string]float64
	withoutVPA map[string]float64
}

// observe turns one analysis into metric values.
func observe(s *snapshot.Snapshot, a *explain.Analyzer, poolLabels []string) *observation {
	o := &observation{
		nodes: map[pair]float64{}, needed: map[string]float64{}, blocked: map[pair]float64{},
		pinned: map[pair]float64{}, oversized: map[string]float64{}, withoutVPA: map[string]float64{},
	}
	for _, r := range a.Nodes() {
		o.nodes[pair{r.Verdict, poolOf(s.Node(r.Node), poolLabels)}]++
		seen := map[pair]bool{}
		for _, f := range r.Findings {
			k := pair{f.Rule, string(f.Severity)}
			if (f.Severity == explain.Blocker || f.Severity == explain.PossibleBlocker) && !seen[k] {
				seen[k] = true
				o.blocked[k]++
			}
		}
	}
	c := a.Cluster()
	o.needed["requests"] = float64(c.NodesNeeded)
	if c.Size.ContainersWithTargets > 0 {
		o.needed["vpa-target"] = float64(c.Size.NodesNeededAtTargets)
	}
	seen := map[string]bool{}
	for _, p := range a.Pinned() {
		if k := p.Controller + "/" + p.Namespace + "/" + p.Workload; !seen[k] {
			seen[k] = true
			o.pinned[pair{p.Controller, p.Namespace}]++
		}
	}
	for _, w := range a.Sizes() {
		o.oversized[w.Namespace] += 0
		o.withoutVPA[w.Namespace] += 0
		if w.Oversized {
			o.oversized[w.Namespace]++
		}
		if !w.HasRecommendation {
			o.withoutVPA[w.Namespace]++
		}
	}
	return o
}

func poolOf(n *snapshot.Node, labels []string) string {
	if n == nil {
		return ""
	}
	for _, l := range labels {
		if v := n.Metadata.Labels[l]; v != "" {
			return v
		}
	}
	return ""
}

// scanCollector exports the latest observation.
type scanCollector struct {
	mu  sync.RWMutex
	obs *observation
}

func (c *scanCollector) set(o *observation) {
	c.mu.Lock()
	c.obs = o
	c.mu.Unlock()
}

func (c *scanCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descNodes, descNodesNeeded, descBlocked, descPinned, descOversized, descWithoutVPA} {
		ch <- d
	}
}

func (c *scanCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	o := c.obs
	c.mu.RUnlock()
	if o == nil {
		return
	}
	pairs := func(d *prometheus.Desc, m map[pair]float64) {
		for k, v := range m {
			ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, k[0], k[1])
		}
	}
	singles := func(d *prometheus.Desc, m map[string]float64) {
		for k, v := range m {
			ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, k)
		}
	}
	pairs(descNodes, o.nodes)
	singles(descNodesNeeded, o.needed)
	pairs(descBlocked, o.blocked)
	pairs(descPinned, o.pinned)
	singles(descOversized, o.oversized)
	singles(descWithoutVPA, o.withoutVPA)
}

// Metrics is every series KubeShrink exports.
type Metrics struct {
	Registry     *prometheus.Registry
	scan         *scanCollector
	scanDuration prometheus.Gauge
	lastSuccess  prometheus.Gauge
	scanErrors   *prometheus.CounterVec
}

// NewMetrics registers KubeShrink's metrics, plus the standard Go and
// process metrics, on a new registry.
func NewMetrics(version, revision string) *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		scan:     &scanCollector{},
		scanDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kubeshrink_scan_duration_seconds", Help: "How long the last scan took: reading the cluster and analyzing it.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kubeshrink_last_successful_scan_timestamp_seconds", Help: "Unix time when the last successful scan finished.",
		}),
		scanErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubeshrink_scan_errors_total", Help: "Lists that failed during scans, by the resource that could not be listed. Kinds the cluster does not serve are not errors.",
		}, []string{"resource"}),
	}
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kubeshrink_build_info", Help: "Build information; the value is always 1."}, []string{"version", "revision"})
	build.WithLabelValues(version, revision).Set(1)
	m.Registry.MustRegister(m.scan, m.scanDuration, m.lastSuccess, m.scanErrors, build,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}
