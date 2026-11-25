<p align="center">
  <img src="docs/images/kubeshrink-logo.png" alt="KubeShrink" width="600">
</p>

# KubeShrink

**Find out why your cluster won't shrink.**

You scaled replicas down for the weekend, but node-hours stayed flat. KubeShrink reads your Kubernetes objects, read-only, and explains which pods keep which nodes alive, and which autoscaler settings keep those pods around. Every finding cites its evidence, states competing explanations and lists what it could not see.

> Status: early prototype (v0.1, unreleased). Expect breaking changes.
>
> **AI is part of the loop; this release is the rules engine it builds on.** The current version uses deterministic rules only: no model calls, and no data leaves your machine. The AI layer comes next: it will explain findings, decide what to check next and weigh competing causes, always grounded in facts the rules have verified. See [How AI fits in](docs/DESIGN.md#how-ai-fits-in-the-next-layer).

## What it looks at

- **Node layer** (Cluster Autoscaler, Karpenter): PodDisruptionBudgets with no room, pods covered by two PDBs, `do-not-disrupt` / `safe-to-evict` / `scale-down-disabled` annotations, pods without a controller, local storage, kube-system pods, utilization thresholds, and pods that cannot fit on any other node.
- **Replica layer** (HPA, KEDA, kube-downscaler): workloads held at `minReplicas` / `minReplicaCount`, paused KEDA scaling, a KEDA ScaledObject fighting a separate HPA, and downscaler targets below an HPA floor.
- **What the autoscaler said**: Cluster Autoscaler and Karpenter events, quoted as evidence.

## Quick start

Requires Go 1.26+.

```sh
make build                       # bin/kubeshrink, with version info
./bin/kubeshrink explain nodes -f examples/weekend-scale-down
./bin/kubeshrink explain node node-c -f examples/weekend-scale-down
./bin/kubeshrink rules           # every rule, in plain language
```

```text
NODE    VERDICT           BLOCKERS  POSSIBLE WARNINGS  CONTEXT  PODS
node-a  blocked                  1         0        1        1     2
node-b  blocked                  1         1        0        0     1
node-c  blocked                  2         1        1        3     2
node-d  no-blocker-found         0         0        1        2     1
```

An excerpt of `explain node node-c`:

```text
[1] BLOCKER  no-room-elsewhere  Pod/ml/infer-0
    No other schedulable node in the snapshot can take this pod (checked node selector, taints,
    and CPU, memory and pod-count requests). Draining this node would leave the pod Pending,
    so autoscalers keep the node.
    Confidence: medium
    Evidence:
      - Pod/ml/infer-0 .spec.nodeSelector = accelerator=gpu-a
      - Node/node-a fit check = node selector accelerator=gpu-a not matched
    Other explanations:
      - Karpenter may replace this node with a cheaper one instead of deleting it; ...
```

Add `-o json` for machine-readable output.

### Your own data, offline

Export with kubectl and point KubeShrink at the file or directory:

```sh
mkdir -p .kubeshrink   # git-ignored
kubectl get nodes,pods,pdb,hpa,deploy,sts,rs,events -A -o yaml > .kubeshrink/cluster.yaml
kubeshrink explain nodes -f .kubeshrink/cluster.yaml
```

Add `scaledobjects.keda.sh` and `vpa` to the list if you use them.

### Live, read-only

```sh
kubeshrink explain nodes --context my-dev-cluster
kubeshrink explain node <node> --context my-dev-cluster -o json
kubeshrink snapshot --context my-dev-cluster --out .kubeshrink/dev   # save for offline replay
```

- `--context` is required; the current context is never used implicitly.
- KubeShrink only issues `list` calls, on a fixed set of resources, and never reads Secrets or ConfigMaps.
- For API-server-enforced read-only access, bind a dedicated identity to [docs/rbac.yaml](docs/rbac.yaml) (get/list only).

### As a kubectl plugin

```sh
make plugin && cp bin/kubectl-shrink /usr/local/bin/
kubectl shrink explain nodes --context my-dev-cluster
```

## Limitations

KubeShrink approximates; it is not a scheduler simulation. Affinity, topology spread and autoscaler configuration (CA flags, Karpenter NodePool budgets) are not evaluated yet, and a snapshot is one moment in time. Each report lists these limitations and each finding lists its unknowns. See [docs/DESIGN.md](docs/DESIGN.md).

## Design and roadmap

[docs/DESIGN.md](docs/DESIGN.md) covers the problem, prior art (and where KubeShrink genuinely differs), the evidence model, the code flow, every rule, and the roadmap, including the AI layer, which only ever works from verified findings.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and our [Code of Conduct](CODE_OF_CONDUCT.md). Report security issues as described in [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
