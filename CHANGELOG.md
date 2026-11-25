# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `kubeshrink explain cluster`: the nodes that run against the nodes the requests would fit on (per pool, a request-based lower bound), request-based packing, the rules that keep nodes, workloads held at a replica floor, and namespaces ranked by the idle capacity they hold (`--top`).
- `kubeshrink explain team <namespace>` (aliases `namespace`, `ns`): a team's requests, its share of the idle capacity on its nodes, its packing, and the findings behind them.
- Findings carry a layer (`replicas`, `policy`, `nodes`), an ID (`F1`, `F2`, ...) and a plain-language meaning; reports group findings by layer.
- The placement check covers required node affinity, required pod affinity and anti-affinity, enforced topology spread and host ports; pod requests count init containers, native sidecars and pod overhead.
- Karpenter NodePools and NodeClaims: rules `karpenter-consolidation-policy`, `karpenter-consolidation-disabled`, `karpenter-budget` and `karpenter-not-consolidatable-yet`; NodeClaim events are quoted with a plain-language reading, and an autoscaler event saying it keeps a node is a possible blocker.
- `spread-constraint` rule for replicas that must stay apart.
- Node autoscaler detection (Karpenter label, Cluster Autoscaler pods or events, otherwise unknown) and `--node-autoscaler auto|cluster-autoscaler|none`.
- Live mode reads Karpenter NodePools and NodeClaims (`karpenter.sh/v1`, falling back to `v1beta1`), records the snapshot time and Kubernetes version in `snapshot-info.json`, and bounds each request at 30 seconds.
- `kubeshrink rules -o json`.
- Synthetic `examples/team-bin-packing` scenario.
- README and design doc state that this version uses rules only and that AI is the next layer of the loop.
- Project logo (`docs/images/kubeshrink-logo.png`) shown in the README, and a square icon (`docs/images/kubeshrink-icon.png`) for avatars and favicons.
- `kubeshrink explain node <name>` and `explain nodes` over offline snapshots (multi-document YAML, kubectl `List`s, JSON).
- Live read-only mode (`--context`, list calls only, never Secrets or ConfigMaps) and `kubeshrink snapshot --out` for offline replay.
- Rules: `karpenter-do-not-disrupt`, `ca-scale-down-disabled`, `ca-safe-to-evict-false`, `pdb-zero-disruptions`, `multiple-pdbs`, `pod-without-controller`, `local-storage`, `kube-system-pod`, `utilization-above-threshold`, `no-room-elsewhere`, `replica-floor`, `keda-paused`, `keda-hpa-conflict`, `downscaler-excluded`, `downscaler-vs-hpa-floor`, `autoscaler-event`.
- Karpenter-managed nodes (label `karpenter.sh/nodepool`) are detected; Cluster Autoscaler-only findings are omitted for them, with a note.
- A cluster with no other schedulable node gets one node-level `no-room-elsewhere` finding instead of one per pod.
- Text and JSON output; `kubeshrink rules`; version info via ldflags; `kubectl shrink` plugin naming.
- Synthetic `examples/weekend-scale-down` scenario, `examples/local-demo` workloads for a throwaway local cluster, design document and read-only RBAC example.
