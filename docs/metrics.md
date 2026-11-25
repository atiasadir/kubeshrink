# Metrics

`kubeshrink serve` scans the cluster every `--interval` (5 minutes by default; `serve.interval` in the chart) and serves what the last successful scan found on `/metrics`. This page lists the metrics, shows how to scrape them, and gives example queries and alerts.

## Endpoints

| Path | Answers |
| --- | --- |
| `/metrics` | The metrics below, plus the Go runtime and process metrics |
| `/healthz` | 200 while the process is up |
| `/readyz` | 200 once a scan has succeeded |

Each scan also writes one JSON log line: `scan complete` with its duration, node and pod counts, nodes needed and blocked nodes, or `scan failed` with the resource that could not be listed.

## The metrics

Labels hold verdicts, node pools, rule IDs, namespaces and controller kinds, never pod or node names. The number of series grows with the number of namespaces and rules, not with the size of the cluster.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `kubeshrink_nodes` | gauge | `verdict`, `nodepool` | Nodes per verdict: `blocked`, `possibly-blocked` or `no-blocker-found`. `nodepool` comes from the first pool label set on the node (`serve.poolLabels` in the chart) and is empty when none is set |
| `kubeshrink_nodes_needed` | gauge | `basis` | How many nodes the pods' requests would fit on, ignoring placement rules: a lower bound, not a plan. `basis="requests"` is always there; `basis="vpa-target"` appears when VPA targets exist and replace requests |
| `kubeshrink_blocked_nodes` | gauge | `reason`, `severity` | Nodes with at least one finding of a rule. `reason` is the rule ID (see `kubeshrink rules`) and `severity` is `blocker` or `possible-blocker`. A node counts under every reason it has, so adding reasons up does not count nodes |
| `kubeshrink_pinned_workloads` | gauge | `controller`, `namespace` | Workloads held at their HPA or KEDA minimum (`hpa`, `keda`) or at a paused KEDA replica count (`keda-paused`) |
| `kubeshrink_oversized_workloads` | gauge | `namespace` | Long-running workloads whose requests are above their VPA recommendation |
| `kubeshrink_workloads_without_vpa` | gauge | `namespace` | Long-running workloads without a VPA recommendation, whose size KubeShrink does not judge |
| `kubeshrink_scan_duration_seconds` | gauge | | How long the last scan took, successful or not |
| `kubeshrink_last_successful_scan_timestamp_seconds` | gauge | | Unix time when the last successful scan finished |
| `kubeshrink_scan_errors_total` | counter | `resource` | Lists that failed, by resource. Kinds the cluster does not serve, such as Karpenter's when Karpenter is not installed, are not errors |
| `kubeshrink_build_info` | gauge | `version`, `revision` | Always 1 |

Control-plane nodes (labelled `node-role.kubernetes.io/control-plane`, or the older `node-role.kubernetes.io/master`) are not in `kubeshrink_nodes`, `kubeshrink_nodes_needed` or `kubeshrink_blocked_nodes`, because no node autoscaler removes them. When every node is a control-plane node, as in a single-node local cluster, they count like any other node.

A successful scan replaces all the gauges at once, so a scrape never mixes two scans. When a scan fails, the resource it could not list is counted in `kubeshrink_scan_errors_total` and the gauges keep the previous scan's values; `kubeshrink_last_successful_scan_timestamp_seconds` tells how old they are. When a list fails but the scan can go on without it, the resource is counted too, and the findings that need it are missing. The values change only once per scan, so scraping every minute or so is plenty.

## Scraping

With the Prometheus Operator, turn on one of the chart's monitors, not both:

```sh
helm upgrade --install kubeshrink charts/kubeshrink -n kubeshrink --create-namespace \
  --set serviceMonitor.enabled=true    # or podMonitor.enabled=true
```

If your Prometheus selects monitors by label, add it with `serviceMonitor.labels` (or `podMonitor.labels`).

Both monitors set `honorLabels: true`. KubeShrink's `namespace` label names the namespace a workload runs in. Without `honorLabels`, Prometheus overwrites it with the scrape target's namespace (where KubeShrink runs) and keeps KubeShrink's value as `exported_namespace`, so every per-namespace series looks as if it belonged to KubeShrink's own namespace. Series without a `namespace` label of their own, such as `kubeshrink_nodes`, still get the target's namespace.

Without the Operator, scrape port 8080 at `/metrics` with `honor_labels: true`. That setup is untested; see [compatibility.md](compatibility.md).

## Querying: count each fact once

Every KubeShrink reports the whole cluster, so two series with the same KubeShrink labels state the same fact twice; they are not parts to add up. That happens when you switch from the ServiceMonitor to the PodMonitor (the old job's series stay visible for up to five minutes) or when two releases watch one cluster. A plain `sum` then doubles: on a local cluster, right after switching monitors, `sum by (namespace) (kubeshrink_workloads_without_vpa)` returned 2, 2 and 4 instead of 1, 1 and 2.

So first collapse duplicates with `max by` the metric's own labels, then aggregate:

```promql
# At most this many nodes could go if pods were packed by requests alone.
# Nodes needed ignores placement rules (a lower bound), so this is an upper bound.
sum(max by (verdict, nodepool) (kubeshrink_nodes)) - max(kubeshrink_nodes_needed{basis="requests"})

# The same if requests matched VPA targets.
sum(max by (verdict, nodepool) (kubeshrink_nodes)) - max(kubeshrink_nodes_needed{basis="vpa-target"})

# Nodes per pool and verdict.
max by (nodepool, verdict) (kubeshrink_nodes)

# The five rules that keep the most nodes.
topk(5, max by (reason, severity) (kubeshrink_blocked_nodes))

# Teams with workloads held at a replica floor.
sum by (namespace) (max by (namespace, controller) (kubeshrink_pinned_workloads)) > 0

# Teams whose requests are above their VPA recommendations.
max by (namespace) (kubeshrink_oversized_workloads) > 0

# Teams with workloads KubeShrink cannot size because they have no VPA.
max by (namespace) (kubeshrink_workloads_without_vpa) > 0

# Seconds since the last successful scan.
time() - max(kubeshrink_last_successful_scan_timestamp_seconds)
```

## Alerts

These go in a Prometheus rule file, or in the `spec.groups` of a PrometheusRule with the Operator:

```yaml
groups:
  - name: kubeshrink
    rules:
      - alert: KubeShrinkScanStale
        expr: (time() - max(kubeshrink_last_successful_scan_timestamp_seconds) > 900) or absent(kubeshrink_last_successful_scan_timestamp_seconds)
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: KubeShrink has not finished a scan for 15 minutes, so its metrics describe an old state.
      - alert: KubeShrinkListErrors
        expr: max by (resource) (increase(kubeshrink_scan_errors_total[30m])) > 0
        labels:
          severity: info
        annotations:
          summary: KubeShrink could not list {{ $labels.resource }} in the last 30 minutes.
```

900 seconds is three scans at the default interval; scale it with `--interval`.

## How this was checked

On Docker Desktop (Kubernetes 1.34.1) with kube-prometheus-stack (Prometheus 3.16.0): `promtool check metrics` passed on `/metrics`, both the ServiceMonitor and the PodMonitor targets were up, and the `exported_namespace` problem above showed up without `honorLabels` and went away with it. The queries and alerts on this page passed `promtool test rules` (promtool 3.16.0) against synthetic series scraped by two jobs, where the plain `sum` versions return double.
