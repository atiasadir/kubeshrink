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

1. Write a `func(*nodeContext) []Finding` in `internal/explain`.
2. Add it to `Catalog` in `internal/explain/catalog.go` with a plain-language explanation.
3. Add positive and negative cases to the table in `internal/explain/explain_test.go`.
4. Describe it in the rules table in `docs/DESIGN.md` and note it in `CHANGELOG.md`.

## Pull requests

- Keep changes focused; one rule or fix per PR is ideal.
- Add a short entry under *Unreleased* in `CHANGELOG.md`.
- New Go files start with:

  ```go
  // SPDX-License-Identifier: Apache-2.0
  // Copyright The KubeShrink Authors.
  ```

By contributing you agree that your contributions are licensed under the Apache License 2.0.
