# instruction-drift-preflight-warning: warn per-pass when the on-disk protocol drifts from the binary

## Problem

`prompt-embed-drift-guard` (DONE) closed only ONE drift axis. Two independent
things must be current for a live honeybee pass to run the intended protocol:

- **Axis A — binary vs tracked-main tip.** The prompts/code are `go:embed`ded into
  the binaries, so a merged change only reaches a pass after a redeploy.
  `promptEmbedDriftWarning` (in `cmd/honeybee/main.go`) already warns when the
  running binary predates the beehive submodule's tracked-main tip. On this host
  Axis A is fully closed and automatic: `beehive-rebuild` is wired as
  `ExecStartPre=` on the honeybee service and rebuilds to `origin/main` every pass.
- **Axis B — on-disk instruction file vs the binary's embedded default.**
  `honeybeeProtocol(root)` (`cmd/honeybee/main.go`) reads `HONEYBEE.md` from the
  hive root's ON-DISK file — the operator-editable copy is authoritative — and only
  falls back to the embedded default when that file is absent. That on-disk file is
  refreshed ONLY by the still-manual `beehive instruction update`; `beehive-rebuild`
  never touches it. So a binary can be perfectly Axis-A-clean and STILL inject a
  stale (or absent) protocol.

Observed live: this host was Axis-A-clean yet its injected `HONEYBEE.md` was the
bare pre-`588ebb6` text ~5h35m after that fix merged, because nothing had run
`beehive instruction update`. Nothing surfaced that gap to the per-pass stderr
channel that `HONEYBEE.md` itself calls "a real defect signal, not noise". Axis B
was visible only as a beehived dashboard badge (`instruction-update-drift`), never
in `cmd/honeybee`'s own preflight.

## Design

Add a second, complementary preflight guard next to the Axis-A one. It runs
ALONGSIDE (not instead of) `promptEmbedDriftWarning` — the two are orthogonal and
this host is Axis-A-clean / Axis-B-dirty right now.

### Reuse `internal/instruct.StatusOf` — no new comparison logic

`instruction-update-drift` (DONE) already built the exact per-file comparison the
frontend badge uses: `internal/instruct.StatusOf(root, name) (Status, ok, err)`,
which byte-compares the on-disk file against the binary's single embedded default
(`prompts.Agents/Honeybee/BootstrapGuide` via `instruct.Files()`) and returns
`Clean | Modified | Missing`. The new guard reuses it verbatim — no second copy of
the defaults, no reimplemented diff. This is precisely `StatusOf`'s documented
shape: a caller that iterates its OWN declared set and asks one file at a time.

### `cmd/honeybee/main.go` — `instructionDriftWarning(root)`

- Iterates the hive-root managed instruction files it names explicitly —
  `AGENTS.md`, `HONEYBEE.md`, `BOOTSTRAP.md` — and calls `instruct.StatusOf` for
  each. Naming them (rather than every managed file) deliberately scopes the guard
  to the injected ROOT protocol, so a drifted skill under `skills/` never fires an
  Axis-B warning about the protocol, and a site-authored `LOCALS.md` (never in the
  managed set, `ok=false`) categorically cannot trip it.
- Collects any file whose status is `Modified` (tagged `(modified)`) or `Missing`
  (tagged `(missing)`). If none drifted, returns `""` (silent).
- Otherwise returns one line in the SAME `WARNING preflight: …` style as
  `promptEmbedDriftWarning`, naming the drifted/missing files and pointing at the
  fix: `run \`beehive instruction update\` here`.
- **Observability only.** A per-file read error is skipped (`continue`), never
  fatal; the guard returns a string and touches nothing — no selection, claim,
  publish, or completion logic. It is wired in `run()` immediately after the
  Axis-A block, printed with the identical `fmt.Fprintf(os.Stderr, "honeybee: %s\n", w)`.
  It checks `primaryRoot`, the exact root `honeybeeProtocol(primaryRoot)` reads
  from, so the check and the injection cannot disagree about which file is meant.

## Tests

`cmd/honeybee/main_test.go` `TestInstructionDriftWarning` uses `instruct.Install`
to lay down the byte-identical embedded defaults as the no-drift baseline:

- `clean-silent` — freshly installed defaults → `""`.
- `modified-warns` — an edited `HONEYBEE.md` → a warning containing
  `WARNING preflight`, `HONEYBEE.md`, `modified`, and `beehive instruction update`.
- `missing-warns` — a removed `BOOTSTRAP.md` → a warning naming it `missing`.
- `multiple-drift-all-named` — a modified `AGENTS.md` + a removed `HONEYBEE.md` are
  BOTH named in the one line.
- `skill-drift-silent` — editing `skills/cleanup.md` (a managed file, but NOT a
  root doc) stays silent, locking the deliberate root-only scope.
- `unmanaged-file-silent` — a site-authored `LOCALS.md` alongside clean root docs
  never fires (`StatusOf` `ok=false`).
- `empty-root-warns-all` — a bare root (no managed files) treats all three as
  missing and names each.

`CGO_ENABLED=0 go build ./...`, `go vet ./...`, and the full `go test ./...` are
green.

## Acceptance mapping

- *preflight emits a stderr warning, established style, whenever any hive-root
  managed file is drift/missing vs the embedded default* → `instructionDriftWarning`
  wired after the Axis-A block, `WARNING preflight: …` line;
  `modified-warns`/`missing-warns`/`multiple-drift-all-named`/`empty-root-warns-all`.
- *reusing `internal/instruct.StatusOf`; no new comparison logic* → the guard only
  calls `StatusOf`; `internal/instruct` is unchanged.
- *runs alongside, not instead of, the Axis-A check* → both blocks run in `run()`;
  `promptEmbedDriftWarning` and its tests untouched.
- *no change to publish/claim/completion logic* → the function returns a string and
  is side-effect-free; only a stderr line is added.
- *go build/test green* → full suite passes.
