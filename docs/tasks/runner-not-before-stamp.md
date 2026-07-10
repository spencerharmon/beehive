# runner-not-before-stamp

Optional `not_before=<RFC3339>` task-header stamp: a general, deterministic,
runner-owned delay primitive (backoff, TTL wait, spaced re-check/retry — not only
deferred verification). A task whose `not_before` is still in the future is held
out of the ready set exactly like an unmet dep until wall-clock passes it, then it
is normally selectable.

## Layer
Same layer as dep-gating and claim/heartbeat — the deterministic `internal/plan`
parser plus `Candidates` gate. No LLM, no config, no new status.

## Parser (`internal/plan/plan.go`)
- `Task.NotBefore time.Time` (zero = no delay).
- `parseHeader` accepts `not_before=<RFC3339>`; a malformed timestamp is a
  surfaced `error` (`plan: bad not_before ...`), never a swallowed zero — mirrors
  the existing `heartbeat` handling.
- `Task.header()` re-emits `not_before=<RFC3339 UTC>` only when set, so
  `Parse(p.String())` round-trips and an absent stamp never serializes.

## Selector gate (`internal/plan/state.go`, `internal/plan/compat.go`)
- `Task.Delayed(now)` — true iff `NotBefore` is set and `now.Before(NotBefore)`.
- `Task.DelayUntil(ts)` — set/refresh side of the primitive (a task or the runner
  on a failed-but-retryable check).
- `Plan.Candidates` skips any `Delayed(now)` task (all tiers), so a future
  `not_before` excludes a TODO task from selection; once wall-clock passes it, the
  task returns to candidacy normally. Independent of dep-gating: a passed
  `not_before` does not bypass an unmet dep, and a satisfied dep does not bypass a
  future `not_before`.

## Tests (`internal/plan/plan_test.go`)
Parse round-trip + absent-field non-emission, malformed-timestamp error,
`Delayed`/`DelayUntil` boundary behavior, `Candidates` exclude-then-include across
wall-clock, and deps ⟂ not_before independence.

## Docs
`prompts/HONEYBEE.md` (selection "ready" definition), `prompts/bootstrap.md` and
`prompts/reconcile.md` (header-format lines), and
`prompts/skills/deferred-verification.md` (now that the stamp ships).
</content>
</invoke>
