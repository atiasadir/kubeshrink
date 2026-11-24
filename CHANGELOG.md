# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `kubeshrink explain node <name>` and `explain nodes` over offline snapshots (multi-document YAML, kubectl `List`s, JSON).
- Live read-only mode (`--context`, list calls only, never Secrets or ConfigMaps) and `kubeshrink snapshot --out` for offline replay.
- Rules: `karpenter-do-not-disrupt`, `ca-scale-down-disabled`, `ca-safe-to-evict-false`, `pdb-zero-disruptions`, `multiple-pdbs`, `pod-without-controller`, `local-storage`, `kube-system-pod`, `utilization-above-threshold`, `no-room-elsewhere`, `replica-floor`, `keda-paused`, `keda-hpa-conflict`, `downscaler-excluded`, `downscaler-vs-hpa-floor`, `autoscaler-event`.
- Text and JSON output; `kubeshrink rules`; version info via ldflags; `kubectl shrink` plugin naming.
- Synthetic `examples/weekend-scale-down` scenario, `examples/local-demo` workloads for a throwaway local cluster, design document and read-only RBAC example.
