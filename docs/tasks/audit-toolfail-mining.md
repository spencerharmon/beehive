# audit tool-call-failure mining + `beehive plan validate`

Operator-directed (2026-07-11), from the session-corpus tool-failure analysis.

## What

1. **`internal/audit` tool-call-failure mining (the "tool check").** Every audit
   pass now deterministically mines each transcript for its OWN tool calls and the
   subset that failed, bucketed into stable categories (`missing-git-ref`,
   `unknown-subcommand`, `command-not-found`, `path-missing`, `permission-denied`,
   `fatal-or-panic`, `nonzero-exit`). New per-session fields `ToolCalls`,
   `ToolFails`, `ToolFailCats` (`internal/audit/toolfail.go`,
   `parse.go`). `cmd/beehive audit` prints a full-corpus
   `# tool-call failure summary` + by-category + worst-first per-task sections,
   and two append-only ledger columns `tool_calls`/`tool_fails` land in
   `metrics.tsv` via the existing additive-schema path (defaults `0` for legacy
   rows). `ToolFailCats` is recomputed each pass, not persisted.

   Detection is producer-anchored and fence-scoped: a tool marker is
   `**🔧 <tool>**` at column 0 (fence depth 0); a quoted prior transcript is
   inside a ``` fence and so is never counted, keeping the count immune to the
   session-audit series' own charter of mining prior sessions. A failure is a
   call whose immediately-following fenced output matches a category signature.
   Known conservative limitation (documented in `toolfail.go`): a call that dumps
   a RAW transcript whose output embeds bare ``` lines can desync fence depth —
   rare, since the series mines via `beehive audit` (TSV, no markers).

2. **Full-corpus coverage.** The tool-fail summary aggregates over ALL finalized
   sessions, not just the incremental N-2 window, so each pass looks through every
   mineable session for tool-call waste (the N-2 ledger window is unchanged).

3. **`beehive plan validate <submodule>`.** New read-only subcommand: parses a
   submodule's `PLAN.md`, round-trips it (Parse → String → Parse), and checks the
   task set is preserved with no duplicate ids; non-zero exit on failure. Closes
   the gap the audit found — passes flailing through nonexistent
   `plan check`/`plan lint`/`task list` to confirm "PLAN.md still parses".

4. **`prompts/HONEYBEE.md`.** Review section now states doc-only tasks have no
   `bee-<taskid>` code branch (a missing branch/ref is expected, not a defect — do
   not re-probe git); the per-pass steps point at `beehive plan validate` instead
   of the nonexistent subcommands.

## Why

A manual corpus triage put ~1-in-11 bash calls on stable, fixable failures
concentrated in a handful of classes (reviewer branch probes on doc-only tasks,
nonexistent-subcommand hunts, path/binary misses). Making the count deterministic
and part of every pass turns a one-off spelunk into a tracked trend — the same
treatment `SilentLoss` got — so future passes rank and fix these for free.

## Tests

`internal/audit/toolfail_test.go` (scanner incl. the quoted-marker exclusion,
classifier, aggregate), updated `cmd_audit_test.go` window header, updated
`ledger_compat_test.go`/`audit_test.go` for the 18-col schema,
`cmd_plan_test.go` `TestPlanValidateCommand`. `go test ./...` green,
`go vet ./...` clean.
