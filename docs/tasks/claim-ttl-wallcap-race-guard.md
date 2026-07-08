# claim-ttl-wallcap-race-guard: decouple WallCap from TTL + detect a stale-at-completion claim

## Problem

A honeybee pass could fully and correctly complete its task — commit, push the
`bee-<taskid>` branch, land on main, flip status — and still have the work silently
redone by a peer, because it finished while holding a claim that had already gone
**stale**. session-audit-013 (Finding #1, ranked #1) measured three instances in one
window (243 turns / 1,459,416 bytes wasted): a session self-reports clean delivery,
then the identical task is redispatched with no review/reject/arbitrate in between.

Root cause, **confirmed in code** (not inference):

- **One number drives two unrelated concerns.** `cmd/honeybee/main.go` built the
  runner with `WallCap: ttl, TTL: ttl` — the SAME `ttl` (from LOCALS.md
  `ttl_minutes`, default 60m; `internal/config`). `TTL` is the claim-staleness
  horizon; `WallCap` is the per-session wall-clock budget. Coupling them means a pass
  is *allowed* to run right up to the instant its own claim goes stale.
- **A task is reclaimable the moment its heartbeat ages past TTL.**
  `internal/plan/state.go:54` `Active` returns `now.Sub(t.Heartbeat) <= ttl`; once
  false the selector treats the claim as abandoned and a peer may reselect
  (`internal/select`, `TestSelectSkipsActiveClaim`).
- **Heartbeats only re-stamp at the TOP of each turn.** `internal/swarm/swarm.go`
  calls `cl.Heartbeat(...)` once per iteration (~L1091), never during the turn body
  (`sess.Prompt`) nor during the `finish() -> publish -> landSourceBranch -> release`
  completion sequence.
- **A single turn body may legitimately run 3× the TTL.** Defaults are
  `TurnTimeoutMinutes=180` vs `TTLMinutes=60` (`internal/config`). So one long turn
  can push `now - lastHeartbeat` well past TTL before it even finishes.
- **The completing turn bypasses the wall check.** The loop-bottom guard
  `if r.now().After(deadline) { break }` sits at `swarm.go:1371`; both done paths set
  `res.Completed = true` and `return res, nil` *above* it (~L1122 ErrResolved path,
  ~L1313 normal path). So WallCap never even fires on the turn that completes — it
  cannot be the thing that protects the completion sequence.

Net: a pass can heartbeat at a turn's top, spend a long turn producing + finishing +
publishing + landing + releasing, and cross the staleness horizon somewhere in there
— with no error on either side. The claim is stale, a peer reclaims, the work is
redone.

## Change

Two independent edits — a preventive decoupling and (load-bearing) a detective
warning.

1. **`cmd/honeybee/main.go` — `WallCap: wallCapFor(ttl)` (was `WallCap: ttl`).**
   New `wallCapFor(ttl) = ttl - ttl/6` (~83%, floored positive). WallCap and TTL are
   no longer literally the same number: a pass's own wall budget can no longer outlive
   its claim's staleness horizon, and there is a margin for the completion sequence.
   This is honest defense-in-depth, **not** the guarantee: the completing turn skips
   the wall check (above), and staleness is measured from the last *heartbeat*, which
   lags a long turn body regardless of WallCap.

2. **`internal/swarm/swarm.go` — detect + durably warn (the real protection).**
   - `Result.ClaimStaleAtCompletion bool` — new field.
   - `lastHeartbeat` tracks the wall-clock of the most recent SUCCESSFUL heartbeat
     re-stamp (seeded at loop entry; set to `hbAt` on each `err == nil` heartbeat).
   - `warnIfClaimStale` closure, called from BOTH done paths right after
     `res.Completed = true` (and after `finish()` has sealed/published the
     transcript): if `r.TTL > 0` and `now - lastHeartbeat > r.TTL`, it sets
     `res.ClaimStaleAtCompletion`, and appends a durable `## ⚠️ warning` block to the
     transcript via the existing `recordPublishFailureWarning` primitive (the same one
     the conflict-retry-exhausted publish failure already uses). Wording contains
     "stale claim" and "reselect" so `internal/audit`'s `lostRaceRe` classifies it
     (Aborted + LostRace) for offline dedupe.

   Crucially it does **not** undo completion: the work really is on main, so
   discarding it or skipping `Release` would be strictly worse. It only surfaces the
   overlap that was previously silent.

`LOCALS.md` is unchanged: no new/split config knob — WallCap is derived from the
existing TTL in code, so nothing operators must set.

## Why detect-and-warn is the load-bearing half

The task's acceptance offered a choice: *either* WallCap can no longer outlive the
claim-staleness threshold, *or* the runner detects and durably warns on a
lost-but-completed publish exactly as publish-fail-durable-warning does. Shrinking
WallCap satisfies the letter of the first, but it is not sufficient on its own,
because the race is timed off heartbeats and the completing turn never consults
WallCap. So this change also implements the second — the genuinely protective control
— and keeps the WallCap decoupling as cheap defense-in-depth. Extending
`recordPublishFailureWarning`'s precedent means the existing audit tooling (which the
session-audit series mines) flags the overlap with zero new plumbing.

No completion predicate or claim semantic HONEYBEE.md relies on is touched: a
completed pass still reports `Completed=true`, still lands, still releases; `Active`,
the heartbeat cadence, GC, and the deterministic completion check are all unchanged.

## Tests

- `internal/swarm` — `TestRunStaleClaimAtCompletionRecordsDurableWarning`: a mutable
  injected clock stamps the claim at a turn top, then jumps +2h (past the 1h TTL)
  inside the turn body before driving the task DONE. Asserts the pass still reports a
  real landed completion (`Completed`, not `GCMarked`), sets `ClaimStaleAtCompletion`,
  writes a trailing `## ⚠️ warning` naming the stale claim, and that
  `audit.ParseFile` classifies it `Aborted + LostRace`.
- `cmd/honeybee` — `TestWallCapFor`: pins the invariants callers rely on — strictly
  below TTL (decoupled) yet positive for any positive TTL (never caps before turn 1);
  `wallCapFor(1h) == 50m`; `wallCapFor(0) == 0` (floored to input).
- Regression coupling verified by hand: neutering `warnIfClaimStale` makes the new
  test fail with `Completed:true ClaimStaleAtCompletion:false`, and the pre-existing
  `TestRunSuccessfulPublishLeavesNoTranscriptWarning` still passes (no false positive
  on a normal, in-TTL completion). Full `CGO_ENABLED=0 go build/vet/test ./...` green.

## Acceptance mapping

- *exact race confirmed and pinned in code (not inference)* → the Problem section
  cites `main.go` `WallCap: ttl, TTL: ttl`, `state.go:54` `Active`, the turn-top-only
  heartbeat (~`swarm.go:1091`), `TurnTimeout=180m` vs `TTL=60m`, and the done paths
  returning above the `swarm.go:1371` wall check.
- *either WallCap can no longer outlive staleness, OR detect+durably warn on a
  lost-but-completed publish* → BOTH: `wallCapFor` (WallCap < TTL) and
  `warnIfClaimStale` (the load-bearing detective control), the latter reusing
  `recordPublishFailureWarning` exactly as publish-fail-durable-warning does.
- *a future audit re-sampling finds no new "clean delivery then fresh redispatch"* →
  behavioral: the overlap is now recorded as Aborted+LostRace in the transcript, so
  audit/dedupe can see and skip the duplicate rather than it staying silent.
- *no completion predicate or claim semantic HONEYBEE.md relies on is broken* →
  completion still reports `Completed=true`, still lands + releases; `Active`,
  heartbeat cadence, GC, and the completion check are untouched.
- *existing internal/swarm + cmd/honeybee tests green plus new regression coverage* →
  full suite green; two new tests above.
