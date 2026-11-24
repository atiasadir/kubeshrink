# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `kubeshrink explain node <name>` and `explain nodes` over offline snapshots (multi-document YAML, kubectl `List`s, JSON).
- Live read-only mode (`--context`, list calls only, never Secrets or ConfigMaps) and `kubeshrink snapshot --out` for offline replay.
- Rules: `karpenter-do-not-disrupt`, `ca-scale-down-disabled`, `ca-safe-to-evict-false`, `pdb-zero-disruptions`, `multiple-pdbs`, `pod-without-controller`, `local-storage`, `kube-system-pod`, `utilization-above-threshold`, `no-room-elsewhere`, `replica-floor`, `keda-paused`, `keda-hpa-conflict`, `downscaler-excluded`, `downscaler-vs-hpa-floor`, `autoscaler-event`.
- Karpenter-managed nodes (label `karpenter.sh/nodepool`) are detected; Cluster Autoscaler-only findings are omitted for them, with a note.
- A cluster with no other schedulable node gets one node-level `no-room-elsewhere` finding instead of one per pod.
- Text and JSON output; `kubeshrink rules`; version info via ldflags; `kubectl shrink` plugin naming.
- Synthetic `examples/weekend-scale-down` scenario, `examples/local-demo` workloads for a throwaway local cluster, design document and read-only RBAC example.
