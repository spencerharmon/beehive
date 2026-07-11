# session-runner-commit-stamp

Stamp the runner binary's build commit into every session transcript header so a
future audit pass can determine which commit produced a session from the repo
alone — no out-of-repo journalctl/host read (the gap session-audit-015/016 hit
when reconstructing a guard's real deploy epoch).

## What changed

- `internal/swarm/swarm.go` (recorder header construction, alongside the existing
  submodule/kind/branch/model fields): append a trailing `· runner: <sha>` field
  to the transcript header. The value comes from `internal/version.Build()`; an
  unstamped (`go build` with no `-ldflags`) binary honestly records `runner: dev`,
  mirroring `version.String()`'s rule — a SHA is never fabricated.
- `internal/audit/parse.go`: `header` gains a `Runner` field; `parseHeaderLine`
  captures the trailing `runner:` key (same additive path as `model:`), and
  `parseTranscript` copies it into `Session.Runner`.
- `internal/audit/audit.go`: `Session` gains the `Runner` field; `""` for a legacy
  transcript predating the stamp.
- Tests: `TestParseHeaderRunner` pins stamped-SHA capture, the `dev` fallback, and
  backward-compat (legacy header with no runner field → `Runner == ""`).

## Deliberately unchanged

- `cmd/honeybee/main.go`'s existing preflight drift-warning logic (stderr-only) is
  untouched — this change is purely additive: it writes the SHA somewhere the repo
  can read back, it does not alter the drift comparison.
- Older transcripts still parse cleanly; the field defaults empty, so the corpus
  census is unaffected (matching audit-parse-model-header's backward-compat
  precedent).

## Verification

`gofmt`, `go vet`, `go test ./...` all green under `CGO_ENABLED=0`.
