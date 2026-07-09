# audit: cross-session silent-loss detection — flag work that completed but was discarded

## Problem

`internal/audit`'s per-session heuristics — `Aborted`, `CompletionMiss`, and the
regex `LostRace` — all key off a `## ⚠️ warning` block the runner appends to a
session's **own** transcript when that session detects a problem. None can fire
for a session that fully and cleanly completes every one of its own steps
(commit, push, status flip, zero warnings) and then has its deliverable silently
discarded or superseded **out of band** — by a later session redoing the same
task, or the runner's own merge/reconcile step. The victim's transcript never
narrates the loss, so it is structurally invisible to any own-transcript rule.

session-audit-014 measured the blind spot: a cross-session correlation check over
its 98-session window surfaced **30 duplicate-dispatch instances across 16 tasks —
2,690 turns / ~11.1MB discarded in one window**, ~10x session-audit-013's headline.
Of the 30, **23 (77%, 2,222 turns) read completely clean** per the engine's own
flags (`aborted=false lost_race=false completion_miss=false`) — the still-open
blind spot. A hand spot-check of 9/30 against `git log --all --grep=<taskid>`
confirmed 100%, zero false positives, including the full 5-session
`session-list-links-labels` chain (five consecutive work sessions, none landed).

## Fix (reporting only — reads only the beehive layer, no git access)

A new **corpus-level** pass, mirroring the existing `ReconcileLoop` annotation:
`internal/audit` stays pure (repo-only, CGO-free, no git dependency — the
submodule overlay's "the repo is the only data source" invariant).

### Detect (`internal/audit/silentloss.go`)

`markSilentLosses(s []Session)` groups the **task-bearing** sessions
(work/review/arbitrate — the same `isTaskBearingKind` filter the abort path uses)
by `TaskID` in the epoch order `ParseDirCensus` already sorts into, then flags a
session when the session **immediately following it for the same task carries the
same kind**.

Kind is the header-authoritative proxy for the status a session was dispatched at
(`work⟺TODO`, `review⟺NEEDS-REVIEW`, `arbitrate⟺NEEDS-ARBITRATION`), so a
same-kind successor means the task was handed out **again at the identical
status**: the earlier session's own flip never reached `main` and its whole run
was discarded. Only the earlier (superseded) session of each pair is flagged; the
later one actually re-did the work. A different-kind successor advanced the task
(its flip landed) and is never flagged — so a legitimate `work→review→arbitrate`
progression yields **zero** silent losses, and an interleaved review between two
work sessions (`work→review→work`) breaks the same-kind adjacency, flagging
nothing, while `work→work→review` flags only the first work.

Reading the **header kind** rather than body-scanning for the injected
`[STATUS]` token keeps this immune to the prompt-pollution that constrains the
abort heuristics. Non-task-bearing kinds (bootstrap/reconcile) are excluded: they
own no task-status handoff and share a synthetic `TaskID`, so folding them in
would misread the adjacent-reconcile pattern (already `ReconcileLoop`'s job) as a
silent loss. Wired into `ParseDirCensus` right after `markReconcileLoops`.

### Why transcript-only (and not git-reachability yet)

The task prefers git-history reachability "where affordable". That refinement is
**deliberately deferred, not skipped**, on an architectural boundary: `internal/
audit` reads only committed beehive-layer artifacts (`sessions/*.md`) and has no
git dependency — the property that keeps it CGO-free and unit-testable with plain
func literals. The correct place to add git is the same injection seam the stub
census already uses: `BranchResolver` (`parse.go`), a caller-supplied,
`*git.Repo`-backed function type passed in from `cmd/beehive` with a nil-safe
no-op default. A future `silent_loss` refinement — confirm the earlier session's
flip commit never reached reachable history, filtering the one known
false-positive class (a reconcile reopening an already-delivered task) — belongs
there, injected exactly like `ClassifyStubs`, never by giving this package a git
dependency. The transcript-only heuristic already satisfies the binding
acceptance criterion: it reproduces the finding at audit-run time with **no manual
git-log cross-referencing**.

### Ledger schema — additive (`internal/audit/ledger.go`)

`metrics.tsv` grows a trailing `silent_loss` column, appended per
`audit-tool-abort-stall-guard`'s append-only discipline. The reader
(`readMetricsTSV` + `validateMetricsHeader` + `padMetricsRow`) accepts the current
16-col schema **or** any legacy prefix no narrower than the original 15-col schema
(`minMetricsCols`), right-padding missing trailing columns from `metricsDefaults`
(`silent_loss → false`). A header that reorders/renames an existing column, or is
narrower than 15, is rejected — appended columns are permitted, a changed prefix
is not. Already-ledgered 15-col rows keep parsing unchanged.

### Surface it (`cmd/beehive/cmd_audit.go`)

`printSilentLoss` adds two read-only sections after the aggregate: a
`# silent-loss summary (cross-session discarded work, full corpus)` headline
(`tasks / silent_losses / turns / bytes` — the reproducible analogue of
session-audit-014's manually-derived total) and a `# silent-loss per task`
breakdown (`taskid / silent_losses / turns / bytes / lost_sessions`). The window
TSV gains a `silent_loss` column. `AggregateSilentLoss` rolls the flagged
sessions up per task, sorted deterministically.

## Tests

`internal/audit/silentloss_test.go`:

- `TestSilentLossGenuinePair` — binding accept: two consecutive same-task,
  same-kind work sessions flag only the earlier.
- `TestSilentLossProgressionNotFlagged` — binding accept: `work→review→arbitrate`
  (a different kind each hop) flags nothing.
- `TestSilentLossInterleavedReviewNotFlagged` — `work→review→work` flags nothing
  (the review breaks same-kind adjacency, evidencing the first flip landed).
- `TestSilentLossChain` — three consecutive work sessions flag the first two, not
  the last (the `session-list-links-labels` shape).
- `TestSilentLossExcludesNonTaskBearing` — adjacent reconcile/bootstrap sessions
  are never silent losses (that is `ReconcileLoop`'s domain).
- `TestSilentLossCrossTaskNotFlagged` — same-kind adjacency across *different*
  tasks is not a loss.
- `TestSilentLossCorpus` / `TestAggregateSilentLoss` — the committed corpus
  fixture and the per-task rollup (counts, summed turns/bytes, epoch-sorted ids).

`internal/audit/ledger_compat_test.go`:

- `TestLedgerReadsLegacyMetrics` — binding accept: a 15-col (pre-`silent_loss`)
  `metrics.tsv` still parses, `silent_loss` defaulted false, prior columns intact.
- `TestLedgerReadsCurrentMetricsSilentLoss` — the 16-col schema round-trips the
  column true/false.
- `TestLedgerRejectsBadMetricsHeader` — a too-narrow or renamed-prefix header is
  rejected.

`cmd/beehive/cmd_audit_test.go` window-header assertions updated for the new column.

Verified: `gofmt`/`go vet`/`go test ./...` green; `CGO_ENABLED=0` static build ok;
`beehive audit --submodule beehive` read-only reproduces the finding over the full
corpus (72 tasks / 735 silent losses) with no manual git-log step.
