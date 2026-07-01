# Contributing

Single Go module `github.com/spencerharmon/beehive`, three binaries built from
`cmd/`. Static, embeddable assets, deps from `/etc/beehive`. No LLM in CLI or
frontend; LLM only inside the honeybee turn loop via opencode.

## Build & test

```
go build ./...
go vet ./...
gofmt -l .              # must be empty
go test -race ./...
./scripts/submodule-smoke.sh
```

CI (`.github/workflows/ci.yml`) runs all of the above + golangci-lint, and on
`v*` tags cross-compiles, checksums, cosign-signs, verifies
(`scripts/verify-release.sh`), and cuts a gh release.

## Conventions

- Commit style: `Pn: short summary` (see `git log`). Honeybee commits carry
  `Beehive: <task-id> <doc-path>` for change-doc linkage.
- Git via exec wrapper (`internal/git`), not go-git, to match submodule/gpg.
- Add deps only when required; note tradeoffs in the PR.
- Tests per package; golden files for PLAN.md transitions.
- ROI.md is human-owned; never write it from automated code.

## Release

Tag `vX.Y.Z`. CI builds dist/, signs, releases. Package debs/rpms with nfpm
(`packaging/nfpm.yaml`). Fill `docs/RELEASE-NOTES-TEMPLATE.md` for the release
body. See `docs/install.md`.
