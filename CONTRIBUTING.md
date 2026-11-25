# Contributing to KubeShrink

Thanks for your interest! KubeShrink is an early prototype, so issues describing a real "my cluster won't shrink" situation are as valuable as code.

## Ground rules

- **Read-only, always.** No code path may create, update, patch, delete or evict anything in a cluster, and nothing may read Secrets or ConfigMaps.
- **Evidence first.** Every finding needs at least one evidence reference, an honest confidence level, and unknowns where the data cannot settle the question.
- **Synthetic data only.** Fixtures, tests, docs and examples must use made-up names. Never commit real cluster, namespace or workload names, events or telemetry. Keep real exports in `.kubeshrink/` (git-ignored).

## Development

```sh
make test     # go test -race ./...
make lint     # golangci-lint run (install: https://golangci-lint.run/welcome/install/)
make vuln     # govulncheck
make example  # run against the synthetic example
```

## Adding a rule

1. Write the check in `internal/explain`: a `func(*nodeContext) []Finding` for a node, or a `func(*nsContext) []Finding` for a namespace. Start it with a short comment that says what it looks at and why that keeps capacity.
2. Add it to `Catalog` in `internal/explain/catalog.go` with a title and a plain-language explanation.
3. Add a case where it fires and a case where it must not to `ruleCases` in `internal/explain/explain_test.go`. `TestEveryRuleHasCases` fails until both exist.
4. If an example under `examples/` triggers the rule, run `make goldens` and review the diff (see below).
5. Describe it in the rules table in `docs/DESIGN.md` and add an entry under *Unreleased* in `CHANGELOG.md`.

## Golden files

`cmd/kubeshrink/testdata/golden` holds what `explain cluster`, `explain nodes`, `explain node` and `explain team` print for every example snapshot, as text and as JSON. `make test` compares the current output with these files, so a change to a finding, a number or the wording shows up in review.

After an intended change:

```sh
make goldens                              # rewrite the files; removes ones nothing produces any more
git diff cmd/kubeshrink/testdata/golden   # every changed line should follow from your change
```

Never edit golden files by hand. A new directory under `examples/` gets its golden files the first time you run `make goldens`.

Metric names, types and labels are pinned by `TestMetricsContract` in `internal/serve/contract_test.go`. Dashboards and alerts depend on them, so change them only on purpose, together with `docs/metrics.md` and `CHANGELOG.md`.

## Pull requests

- Keep changes focused; one rule or fix per PR is ideal.
- Add a short entry under *Unreleased* in `CHANGELOG.md`.
- New Go files start with:

  ```go
  // SPDX-License-Identifier: Apache-2.0
  // Copyright The KubeShrink Authors.
  ```

By contributing you agree that your contributions are licensed under the Apache License 2.0.
