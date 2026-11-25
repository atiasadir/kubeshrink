# Compatibility

What KubeShrink has actually run against. Anything not listed here is untested, even where the APIs suggest it should work.

The chart declares `kubeVersion: ">=1.23.0-0"` because 1.23 is the oldest Kubernetes that serves `autoscaling/v2` and `policy/v1`. That is an API minimum, not a tested range.

## Kubernetes

| Version | Cluster | What ran |
| --- | --- | --- |
| 1.34.1 | Docker Desktop, one node | In-cluster mode: the Helm chart (install, upgrade, uninstall), `/metrics`, `/healthz` and `/readyz`, scraping through a ServiceMonitor and a PodMonitor, and a server-side dry run of the `restricted` Pod Security Standard |
| 1.35.8 | kind 0.33.0, one node, created and deleted by `make e2e` | The CLI on `examples/local-demo/workloads.yaml` (a live scan and a replay of its snapshot) and the chart: install, `/metrics`, JSON logs, the service account's permissions and uninstall |
| 1.35.9 | A managed cluster whose nodes Karpenter provisions | The live read-only CLI: `snapshot` and `explain` |

The Kubernetes client libraries (`client-go`, `apimachinery`) moved from 0.34.2 to 0.37.2 on 2026-11-25. The Docker Desktop checks were repeated with 0.37.2: install, readiness, two scans, `/metrics` through `promtool check metrics`, the service account's permissions and uninstall. The managed-cluster run used 0.34.2 and has not been repeated.

Offline mode and the unit tests read synthetic snapshots (see `examples/`), so they do not depend on a cluster.

## Autoscaling and policy APIs

| Kind | API version | Tested against |
| --- | --- | --- |
| HorizontalPodAutoscaler | `autoscaling/v2` | The live cluster |
| PodDisruptionBudget | `policy/v1` | The live cluster |
| Karpenter NodePool and NodeClaim | `karpenter.sh/v1`, falling back to `v1beta1` | `v1` on the live cluster; `v1beta1` only in unit tests with a fake client |
| KEDA ScaledObject | `keda.sh/v1alpha1` | The live cluster |
| VerticalPodAutoscaler | `autoscaling.k8s.io/v1` | The live cluster |
| Cluster Autoscaler | None: detected from its pods and events | Synthetic snapshots only |

## Tools

| Tool | Version | Used for |
| --- | --- | --- |
| Go | 1.26.11 | Building and testing. The image is built with the Go toolchain image from MCR (`1.26.11-azurelinux3.0`) with system crypto switched off |
| Helm | 4.0.4 | Installing the chart locally. CI lints and renders it with the Helm on GitHub's `ubuntu-latest` runner |
| kube-prometheus-stack | Chart 93.10.0: Prometheus Operator 0.95.2, Prometheus 3.16.0 | Scraping through a ServiceMonitor and a PodMonitor |
| promtool | 3.16.0 | `promtool check metrics` on `/metrics` |
| Docker | 29.1.2 with the containerd image store | Building the image |
| Trivy | 0.76.2 | Scanning the image (`make scan`) |
| kind | 0.33.0, through `go run` | The throwaway cluster of `make e2e` |

## Image

| Platform | Status |
| --- | --- |
| `linux/arm64` | Built locally and run in the Docker Desktop cluster and in kind (`make e2e`) |
| `linux/amd64` | Built and scanned in CI, and run in a kind cluster by the CI `e2e` job |

The released image `ghcr.io/atiasadir/kubeshrink:0.1.0` (`sha256:2d1bfe618bedebf93aa2b0fa4ffedd8b140e5688e085a3d8ecaae5a02ae3c4ff`) ran with the chart's default values on a one-node kind 0.33.0 cluster with Kubernetes 1.35.8 on `linux/arm64`: the node pulled it without registry credentials, the pod became Ready after its first scan, and `kubeshrink_build_info` reported `version="v0.1.0"`. Its `linux/amd64` image has only been pulled and run with `docker run` (`kubeshrink version`), not in a cluster.

## Not tested yet

- Other Kubernetes versions and distributions, including OpenShift.
- In-cluster mode on a cluster with Karpenter, KEDA or VPA installed.
- More than one replica; the chart runs one.
- Prometheus without the Operator, such as a plain scrape config.
- Cluster Autoscaler on a real cluster.
- The released `linux/amd64` image in a cluster.
