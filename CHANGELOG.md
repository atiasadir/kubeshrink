# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `control-plane-node` rule: a node labelled `node-role.kubernetes.io/control-plane` (or the older `node-role.kubernetes.io/master`) is reported as managed by `none` with this blocker, and is left out of `explain cluster` (nodes, pools, nodes needed, packing, verdicts, top reasons and namespaces) and of `kubeshrink_nodes`, `kubeshrink_nodes_needed` and `kubeshrink_blocked_nodes`, because no node autoscaler removes it. Clusters whose nodes are all control-plane nodes, such as single-node kind or Docker Desktop clusters, are unchanged.
- `karpenter-needs-new-node` rule: Karpenter's consolidation simulation counts a node it could create as running no pods, so a topology spread on `kubernetes.io/hostname` can leave a pod no existing node even where the scheduler would accept it. Karpenter can then only replace the node, and only with a cheaper one. When this explains a Karpenter event, the note that the placement check disagrees with Karpenter is no longer added.
- `kubeshrink serve`, an in-cluster mode: it scans on an interval (`--interval`, 5m by default) and serves low-cardinality Prometheus metrics on `/metrics`, plus `/healthz`, `/readyz` (ready after the first successful scan) and JSON scan logs. See `docs/metrics.md`.
- Helm chart `charts/kubeshrink`: a read-only ClusterRole (get and list, never Secrets or ConfigMaps), a pod that meets the `restricted` Pod Security Standard, and an optional ServiceMonitor or PodMonitor with `honorLabels`, so namespace labels survive scraping.
- A container image, built locally with `make image` and never pushed: a static binary on Azure Linux 3.0 distroless minimal, built with the Go toolchain image from MCR, both pinned by digest. `make scan` checks it with Trivy and fails on fixable HIGH or CRITICAL findings, `make sbom` writes a CycloneDX SBOM, and `make chart` lints and renders the chart.
- Chart value `image.digest` pins the image by digest (`repository[:tag]@sha256:...`), so an install runs exactly the image that was scanned; a malformed digest fails the render, and `make chart` checks each form of the image reference.
- CI builds and scans the image, keeps its SBOM, pins every action to a commit SHA and also runs weekly; Dependabot proposes Go module, action and base image updates. `SECURITY.md` describes how vulnerabilities are handled.
- Release workflow `.github/workflows/release.yml`, run only for version tags (`v*`): the same scan gate as CI, then `linux/amd64` and `linux/arm64` images pushed to `ghcr.io/atiasadir/kubeshrink` with the workflow's `GITHUB_TOKEN`, signed by digest with cosign keyless signing and given their CycloneDX SBOM as a signed attestation. The README shows how to verify and install them.
- `docs/metrics.md` (metrics, scraping, example queries and alerts) and `docs/compatibility.md` (what has actually been tested).
- `kubeshrink explain cluster`: the nodes that run against the nodes the requests would fit on (per pool, a request-based lower bound), request-based packing, the rules that keep nodes, workloads held at a replica floor, and namespaces ranked by the idle capacity they hold (`--top`).
- `kubeshrink explain team <namespace>` (aliases `namespace`, `ns`): a team's requests, its share of the idle capacity on its nodes, its packing, and the findings behind them.
- Findings carry a layer (`replicas`, `policy`, `nodes`), an ID (`F1`, `F2`, ...) and a plain-language meaning; reports group findings by layer.
- The placement check covers required node affinity, required pod affinity and anti-affinity, enforced topology spread and host ports; pod requests count init containers, native sidecars and pod overhead.
- Karpenter NodePools and NodeClaims: rules `karpenter-consolidation-policy`, `karpenter-consolidation-disabled`, `karpenter-budget` and `karpenter-not-consolidatable-yet`; NodeClaim events are quoted with a plain-language reading, and an autoscaler event saying it keeps a node is a possible blocker.
- `spread-constraint` rule for replicas that must stay apart.
- A `requests` layer (how big each pod is) from VPA recommendations: rules `requests-above-vpa`, `vpa-not-applied`, `vpa-hpa-conflict` and `vpa-min-allowed`; VPA coverage per team and cluster; and in `explain cluster` an estimate of the nodes needed if requests matched VPA targets. The snapshot reads VPA resource policies, uncapped targets and HPA metrics.
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
