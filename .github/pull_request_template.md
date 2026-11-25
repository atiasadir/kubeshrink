## What and why

## How it was tested

<!-- The commands you ran and what they showed. -->

## Checklist

- [ ] `make test` and `make lint` pass
- [ ] New or changed rules have a case where they fire and one where they must not, in `ruleCases`
- [ ] When output changed: `make goldens` run and the golden diff reviewed
- [ ] Docs updated where behavior changed (README, `docs/`, the rules table in `docs/DESIGN.md`, `docs/metrics.md`)
- [ ] Fixtures and docs use synthetic names only (no real cluster data)
- [ ] Still read-only: no write verbs, no Secrets or ConfigMaps
- [ ] `CHANGELOG.md` updated under *Unreleased*
