# instruction-drift-preflight-warning: warn on stale on-disk instruction files (Axis B)

## Problem

`prompt-embed-drift-guard` (DONE) checks exactly ONE drift axis: is the running
binary's build SHA behind the beehive submodule's tracked-main tip ("Axis A" —
`promptEmbedDriftWarning`). session-audit-011 Finding #1 confirmed, live, that Axis A
alone is insufficient: on this host `beehive-rebuild` is wired as `ExecStartPre=` on
the honeybee service and rebuilds the binaries to `origin/main`'s tip automatically
every ~7 minutes — Axis A is fully closed and automatic here. Yet the very session
that confirmed this was STILL handed the bare pre-`588ebb6` `HONEYBEE.md` text, ~5h35m
after that fix had merged.

The reason is a second, independent axis: `cmd/honeybee`'s `honeybeeProtocol()` reads
`HONEYBEE.md` from the hive root's ON-DISK file (falling back to the embedded default
only when the file is absent). That on-disk copy is refreshed only by the separate,
still fully-manual `beehive instruction update` — nothing rebuilds or auto-triggers
it. So a freshly rebuilt (Axis-A-clean) binary can keep injecting a stale on-disk
`HONEYBEE.md` (Axis-B-dirty) indefinitely, and `prompt-embed-drift-guard` never
notices because it only ever compares the BUILD to the tracked git tip, never the
on-disk file to the binary's own embedded default.

`instruction-update-drift` (DONE) already built and tested the exact comparison this
needs — `internal/instruct.StatusOf`/`Scan`: clean/modified/missing vs. the binary's
own embedded default — but wired it only into the `beehived` dashboard badge, never
into `cmd/honeybee`'s own per-pass stderr preflight, which is the channel
`HONEYBEE.md` itself calls out as "a real defect signal, not noise" (the existing
dirty-checkout and Axis-A warnings).

## Design

Reuse `internal/instruct.Scan` verbatim — no new comparison logic, no second
hardcoded list of managed files or defaults.

### `cmd/honeybee/main.go` — `instructionDriftWarning` (Axis B)

A new preflight check, run in `run()` immediately after the existing Axis-A
`promptEmbedDriftWarning` block and before selection/claim — alongside it, not
instead of it:

```go
if w := instructionDriftWarning(primaryRoot); w != "" {
    fmt.Fprintf(os.Stderr, "honeybee: %s\n", w)
}
```

`instructionDriftWarning(root string) string`:

- Calls `instruct.Scan(root)`, which reports the `Status` (`Missing | Clean |
  Modified`) of every beehive-managed file — `HONEYBEE.md`, `AGENTS.md`,
  `BOOTSTRAP.md`, and every `skills/*.md` — against THIS binary's own compiled-in
  default (the same `prompts.Agents/Honeybee/BootstrapGuide`/skill bodies the
  binary would install fresh). This naturally covers the full "hive-root managed
  file" surface the Accept criterion names, not just the three root docs, with zero
  additional comparison code.
- A `Scan` error (e.g. an unreadable root) returns `""`: best-effort, observability
  only, never blocks a pass — mirrors `promptEmbedDriftWarning`'s own contract.
- Collects every file whose status is not `Clean`, sorts the names (map iteration
  order is otherwise nondeterministic — this keeps the log line and tests stable),
  and returns "" when the set is empty (nothing drifted).
- Otherwise formats one `"WARNING preflight: …"` line in the SAME style as the
  existing dirty-checkout / Axis-A warnings, naming the count, each drifted file with
  its status (e.g. `HONEYBEE.md(modified)`), and the fix (`beehive instruction
  update`).

Called unconditionally (no submodule/git-fixture dependency, unlike Axis A which
needs to identify the "self" submodule first) — it only needs the hive root path
already resolved earlier in `run()` (`primaryRoot`).

### Why Scan, not three hand-named `StatusOf` calls

The card names `HONEYBEE.md`/`AGENTS.md`/`BOOTSTRAP.md` as the motivating examples,
but the Accept text is broader: "whenever **any hive-root managed file** is
drift/missing." `instruct.Scan` already enumerates the exact managed set
(`instruct.Files()`: the three root docs + every shipped skill under `skills/`) from
ONE source of truth. Hand-listing three names would (a) under-cover skills, which
carry the identical staleness risk as the root docs and are refreshed by the same
`instruction update` path, and (b) create a second, driftable copy of "which files are
managed" for this call site to maintain. `Scan` avoids both.

## Complementary, not overlapping, with Axis A

- **Axis A** (`promptEmbedDriftWarning`): is THIS BINARY's build behind the
  submodule's tracked-main tip? Answers "do I need to redeploy?"
- **Axis B** (`instructionDriftWarning`, this task): does the ON-DISK file this pass
  actually reads match what THIS SAME binary would install fresh? Answers "do I need
  to run `beehive instruction update`?"

A host can be clean on one and dirty on the other independently (confirmed live,
session-audit-011: this host was Axis-A-clean, Axis-B-dirty at time of writing), so
both run every pass, unconditionally of each other's result.

## Tests

`cmd/honeybee/main_test.go` `TestInstructionDriftWarning` (no git fixture needed —
unlike `TestPromptEmbedDriftWarning`, Axis B never touches git, only the plain
filesystem `instruct.Install` populates):

- `clean-silent` — a fresh `instruct.Install` (byte-identical to the embedded
  default) → `""`.
- `modified-warns` — `HONEYBEE.md` edited → non-empty, `WARNING preflight:` prefix,
  names `HONEYBEE.md`, `modified`, and `beehive instruction update`.
- `missing-warns` — `AGENTS.md` removed → non-empty, names `AGENTS.md` and
  `missing`.
- `uninstalled-root-warns` — a bare empty root (every managed file `Missing`) still
  warns rather than erroring; `Scan` never fails on absent files.

`go build ./...`, `go vet ./...`, and the full `go test ./...` are green.

## Acceptance mapping

- *preflight emits a stderr warning, established style, whenever any hive-root
  managed file is drift/missing, reusing `internal/instruct.StatusOf`* →
  `instructionDriftWarning` via `instruct.Scan` (built on the same `stat`/default
  comparison `StatusOf` exposes per-file); `TestInstructionDriftWarning`.
- *no new comparison logic* → zero new drift/diff code; `Scan` is called as-is.
- *runs alongside, not instead of, Axis A* → both preflight blocks execute
  unconditionally in `run()`; neither result affects the other.
- *non-fatal, never touches selection/claim/publish* → a pure stderr `Fprintf`
  ahead of `sel0er.Select`; no return value threaded into selection/claim/publish.
- *go build/test green* → `go build ./...`, `go vet ./...`, `go test ./...` all pass.

## Not done (deliberate — see companion task)

`rebuild-recipe-completeness-doc` (session-audit-011 Finding #2, separate task)
covers the doc-only follow-ups this finding also surfaced: stamping `-ldflags` into
the host's live `~/.local/bin/beehive-rebuild` deploy script and documenting a
rebuild-then-`instruction-update` recipe in `BOOTSTRAP.md`. Both are operator/doc
concerns outside this task's code-only Files: scope.
