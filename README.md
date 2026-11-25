<p align="center">
  <img src="docs/images/kubeshrink-logo.png" alt="KubeShrink" width="600">
</p>

# KubeShrink

**Find out why your cluster won't shrink, and why your teams' bin-packing is low.**

Your pods request a third of what your nodes offer, yet the nodes stay. KubeShrink reads your Kubernetes objects, read-only, and explains what keeps each node, which team holds how much idle capacity, and which settings keep the pods around. Every finding cites its evidence, states competing explanations and lists what it could not see.

> Status: early proof of concept (v0.1, unreleased). Expect breaking changes.
>
> Deterministic rules only: no model calls, and no data leaves your machine. The AI layer is on hold until the proof of concept shows the rules are worth building on; see [the roadmap](docs/DESIGN.md#9-roadmap).

## How it reads a cluster

**Levels** are what you ask about. **Layers** are where a cause sits.

| Level | Command | Question |
|---|---|---|
| Cluster | `explain cluster` | How many nodes would the requests fit on, how many run, and what keeps the rest? |
| Team | `explain team <namespace>` | How much idle capacity does this namespace hold, and why? |
| Node | `explain node <name>`, `explain nodes` | Why can't this node be removed? |

| Layer | Question it answers | What KubeShrink checks |
|---|---|---|
| replicas | Why do these pods exist? | HPA `minReplicas`, KEDA `minReplicaCount` and paused scaling, a ScaledObject fighting a second HPA, kube-downscaler settings |
| requests | How big is each pod? | Requests against VPA recommendations: above the upper bound, recommendations not applied, VPA and HPA on the same resource, `minAllowed` floors. Workloads without a recommendation are not judged. |
| policy | Where may they run, and may they move? | Node selectors, required node and pod (anti-)affinity, taints, topology spread, host ports, PDBs with no room, `do-not-disrupt` and `safe-to-evict` annotations, bare pods, local storage |
| nodes | Will the node autoscaler remove or replace the node? | Karpenter NodePool `consolidationPolicy`, `consolidateAfter` and disruption budgets, NodeClaim state and events, pods Karpenter could only move to a new node; Cluster Autoscaler annotations and thresholds |

Packing is measured with resource requests. A namespace's **idle share** is its part of the unrequested capacity on each node it uses, split between the namespaces on that node in proportion to their requests. Its **packing** is its requests divided by requests plus idle share.

## Quick start

Requires Go 1.26+.

```sh
make build
./bin/kubeshrink explain cluster -f examples/team-bin-packing
./bin/kubeshrink explain team checkout -f examples/team-bin-packing
./bin/kubeshrink explain node general-a -f examples/team-bin-packing
./bin/kubeshrink rules            # every rule, by layer, in plain language
```

An excerpt of `explain cluster` on the synthetic example:

```text
4 node(s) run; by requests alone their pods would fit on 2. Pods request 31% of allocatable CPU
and 19% of memory. Verdicts: 4 blocked, 0 possibly blocked, 0 with no blocker found.

What keeps nodes (rules that block or may block removal, most nodes first)
  no-room-elsewhere [policy, blocker] on 4 node(s): general-a, general-b, general-c, ml-a
      The other nodes say no because of: pod anti-affinity on 2 node(s), taint on 1 node(s).
  karpenter-consolidation-policy [nodes, blocker] on 1 node(s): ml-a
      NodePool ml only consolidates empty nodes (consolidationPolicy WhenEmpty). ...

Why pods exist: workloads held at a floor
  1 workload(s) at HPA minReplicas 3: 3 pod(s) requesting 1.5 CPU, 3.0Gi
  1 workload(s) at KEDA minReplicaCount 2: 2 pod(s) requesting 2.0 CPU, 4.0Gi

Namespaces by the idle capacity they hold
  NAMESPACE   PODS  NODES  REQUESTED        IDLE SHARE       PACKING CPU/MEM  PINNED
  ml          1     1      2.0 CPU, 8.0Gi   5.6 CPU, 23.5Gi  26% / 25%        0
  checkout    3     3      1.5 CPU, 3.0Gi   3.4 CPU, 21.5Gi  31% / 12%        1
```

`explain cluster` also estimates how many nodes the pods would fit on if their requests matched the VPA targets, where a target exists. `explain team checkout` then shows the findings behind that team's number: its HPA floor of 3, requests above its VPA's upper bound that the VPA (mode Off) never applies, the required anti-affinity that gives each replica its own node, and the placement check that finds no other node for any of them. Add `-o json` for machine-readable output.

### Your own data, offline

Export with kubectl and point KubeShrink at the file or directory:

```sh
mkdir -p .kubeshrink   # git-ignored
kubectl get nodes,pods,pdb,hpa,deploy,sts,rs,events -A -o yaml > .kubeshrink/cluster.yaml
kubeshrink explain cluster -f .kubeshrink/cluster.yaml
```

Add `scaledobjects.keda.sh`, `vpa`, `nodepools.karpenter.sh` and `nodeclaims.karpenter.sh` to the list if you use them.

### Live, read-only

```sh
kubeshrink explain cluster --context my-dev-cluster
kubeshrink explain team <namespace> --context my-dev-cluster -o json
kubeshrink snapshot --context my-dev-cluster --out .kubeshrink/dev   # save for offline replay
```

- `--context` is required; the current context is never used implicitly.
- Nodes with a `karpenter.sh/nodepool` label are treated as Karpenter-managed. Others are treated as Cluster Autoscaler-managed when its pods or events are visible; otherwise findings for both are kept, because managed offerings run Cluster Autoscaler outside the cluster. Override with `--node-autoscaler cluster-autoscaler|none`.
- KubeShrink only issues `list` calls, on a fixed set of resources, and never reads Secrets or ConfigMaps.
- For API-server-enforced read-only access, bind a dedicated identity to [docs/rbac.yaml](docs/rbac.yaml) (get/list only).

### As a kubectl plugin

```sh
make plugin && cp bin/kubectl-shrink /usr/local/bin/
kubectl shrink explain nodes --context my-dev-cluster
```

### In a cluster (proof of concept)

`kubeshrink serve` scans the cluster on an interval and exports what it finds as low-cardinality Prometheus metrics. By default the chart installs the released image its `appVersion` names, from GitHub Container Registry. For anything beyond a quick try, verify that image and pin it by digest ([see below](#from-github-container-registry)).

```sh
helm install kubeshrink charts/kubeshrink -n kubeshrink --create-namespace
kubectl -n kubeshrink port-forward svc/kubeshrink 8080 &
curl -s localhost:8080/metrics | grep '^kubeshrink_'
```

To try unreleased changes, build the image and point the chart at it. Docker Desktop shares local images with its cluster; with kind, run `kind load docker-image kubeshrink:dev` first.

```sh
make image    # kubeshrink:dev, a static binary on Azure Linux distroless; nothing is pushed
helm install kubeshrink charts/kubeshrink -n kubeshrink --create-namespace \
  --set image.repository=kubeshrink --set image.tag=dev
```

- The ClusterRole grants get and list only, never on Secrets or ConfigMaps. The pod runs as a non-root user with a read-only root filesystem and meets the `restricted` Pod Security Standard.
- With the Prometheus Operator, add `--set serviceMonitor.enabled=true` (or `podMonitor.enabled=true`). Metrics, example queries and alerts are in [docs/metrics.md](docs/metrics.md); what has been tested is in [docs/compatibility.md](docs/compatibility.md).
- To run an image you pushed to your own registry, pin it by digest: `--set image.repository=<registry>/kubeshrink --set image.tag=<tag> --set image.digest=sha256:<digest>`. The pod then runs exactly the image you scanned, even if the tag is pushed again.
- `make scan` checks the image with Trivy. `helm uninstall kubeshrink -n kubeshrink` removes everything the chart created; the namespace stays until you delete it.

### From GitHub Container Registry

Each version tag (`v*`) publishes a multi-arch image (`linux/amd64` and `linux/arm64`) to `ghcr.io/atiasadir/kubeshrink`, tagged with the version without its `v`. It is signed with cosign keyless signing and carries its CycloneDX SBOM as a signed attestation.

Verify the image first. The signature and the SBOM attestation must come from this repository's release workflow, run for the version's tag:

```sh
VERSION=0.1.0   # a released version
IMAGE=ghcr.io/atiasadir/kubeshrink:$VERSION
IDENTITY=https://github.com/atiasadir/kubeshrink/.github/workflows/release.yml@refs/tags/v$VERSION
ISSUER=https://token.actions.githubusercontent.com
cosign verify "$IMAGE" --certificate-identity "$IDENTITY" --certificate-oidc-issuer "$ISSUER"
cosign verify-attestation --type cyclonedx "$IMAGE" \
  --certificate-identity "$IDENTITY" --certificate-oidc-issuer "$ISSUER" > /dev/null
```

`cosign verify` prints the digest it checked (`docker-manifest-digest`). Pin every install beyond a quick try to that digest. On `main` the chart's `appVersion` is the latest release, so from a clone of this repository:

```sh
helm install kubeshrink charts/kubeshrink -n kubeshrink --create-namespace \
  --set image.digest=sha256:<digest>
```

For another release, also set `image.tag="$VERSION"`, so the version label matches the digest.

## Limitations

KubeShrink approximates; it is not a scheduler simulation. The placement check covers node selectors, required node affinity, taints, required pod (anti-)affinity, topology spread and host ports, but not volume zones, priorities or preemption. On Karpenter nodes it also applies Karpenter's stricter reading of hostname topology spread, but it cannot see instance prices. Cluster Autoscaler flags and node group minimums are not visible to it, packing uses requests rather than real usage, and a snapshot is one moment in time. Each report lists these limitations and each finding lists its unknowns. See [docs/DESIGN.md](docs/DESIGN.md).

## Design and roadmap

[docs/DESIGN.md](docs/DESIGN.md) covers the problem, prior art (and where KubeShrink genuinely differs), the evidence model, the code flow, every rule, and the roadmap, including the AI layer, which only ever works from verified findings.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and our [Code of Conduct](CODE_OF_CONDUCT.md). Report security issues as described in [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
