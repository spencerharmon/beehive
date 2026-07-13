# lost-work-recover-any-status — broaden lost-work recovery to ANY status, never NEEDS-HUMAN

## Problem

The dispatch-time lost-work guard (`swarm.recoverIfLost`, sibling of
`bounceIfUnreachable` / `finalizeIfAlreadyMerged`) reset only a task the work
pass had already flipped to `NEEDS-REVIEW` / `NEEDS-ARBITRATION`. But the
identical unrecoverable-commit signal — `bee-<taskid>` reachable NOWHERE (no
local ref, none on the submodule remote after a prune-fetch), the gitlink never
advanced onto it, and no `docs/bee-<taskid>-<taskid>.md` — also arises for a
plain **work** pass OOM-killed / capped BEFORE it could push `bee-<taskid>` and
bump the pointer, leaving the task in `TODO` (any non-terminal state) with a
stale claim and no reviewable status. Such a task was re-dispatched into pass
after doomed pass.

Worse, `plan.Task.RecoverLostWork` mirrored `Reject`/`Strand`: past a retry limit
it escalated the lost-work signal to `NEEDS-HUMAN`. Observed live: phantom-library
`m14-per-user-rig` accumulated `attempts=4` of the runner correctly DETECTING the
lost commit yet still escalating `Human-needed` on that same signal, stranding a
hard dep of `m14-ledger-done` behind a no-decision operator gate. A detected
unrecoverable commit is never a human decision — it is always a clean reset.

## Fix

Layered in `internal/swarm` (same layer as claim/heartbeat/dep-gating) reusing
`internal/git` reachability plumbing next to `CommitReachable`.

1. **Reachability helper (`git.Repo.BranchReachableAnywhere`)** — the
   branch-existence sibling of `CommitReachable` (which checks a specific sha; a
   lost work pass records no sha at all, only the branch is missing). Local ref →
   present; else, when a remote exists, prune-fetch + `ls-remote`. Same
   conservative contract: `remote == ""` (local sharing) skips the remote probe;
   a network/auth fetch failure that is NOT a definitive "no such ref" is
   returned as an error (fail open), so a slow fetch or an unpushed-local branch
   is never misread as loss. `recoverIfLost` now calls this instead of
   re-implementing the checks inline.

2. **Ungate the status** — the dispatch guard now runs for a Work (TODO) task in
   addition to Review/Arbitrate. `plan.Task.RecoverLostWork` accepts ANY
   non-terminal status (rejects only DONE / NEEDS-HUMAN) and always resets to
   `TODO`, clearing the claim and bumping `attempts`.

3. **No reset-livelock** — for Work the guard fires ONLY when a prior pass left a
   STALE claim (`sel.Task.Session != ""` in the pre-claim selection — the sole
   proof a pass actually ran and abandoned it; `cl.Claim` only succeeds over a
   stale, never a live, claim). A brand-new TODO (never claimed) and a task the
   guard JUST reset (claim cleared) both fall straight through to a genuine work
   dispatch instead of being re-reset with zero real attempts. For Review/
   Arbitrate the guard always runs — a reset flips the kind to Work, so it
   structurally cannot loop.

4. **Never NEEDS-HUMAN** — the `attempts > limit → NEEDS-HUMAN` branch is removed
   from `RecoverLostWork` (`plan` and `claim` signatures drop the `limit` arg). A
   lost commit is ALWAYS reimplemented. NEEDS-HUMAN stays reserved for genuine
   human input (credential, out-of-GitOps action, intent/scope decision).

5. **Bounded-retry health signal** — after K consecutive zero-progress resets on
   the same task (K = `RejectLimit`, default 3) the guard KEEPS reimplementing
   but surfaces the repeated-loss pattern (a pass reliably dying before publish —
   likely OOM on the memory-constrained host or the turn cap; cross-ref
   build/test cleanup + turn-cap levers) as a distinct `HEALTH lost-work-loop`
   line on the concise/observability channel where journalctl/audit can mine it —
   NOT a task status change.

Sibling of `orphan-branch-reclaim-guard` / `review-already-merged-guard` (which
assume a commit exists somewhere); this is the earlier "nothing reachable at all"
case and does not shadow them.

## Tests

- `internal/git`: `TestBranchReachableAnywhere` (present-local, remote-only,
  absent-with/without-remote, fail-open on fetch error).
- `internal/plan`: `RecoverLostWork` recycles TODO/REVIEW/ARB to TODO and never
  escalates to NEEDS-HUMAN at any attempts count; rejected only on DONE/HUMAN.
- `internal/claim`: `RecoverLostWork` legal on TODO, rejected on DONE.
- `internal/swarm`: `TestWorkDispatchRecoversTrulyLostWork` (Work TODO + stale
  claim + high attempts → TODO reset, no NEEDS-HUMAN, HEALTH signal emitted);
  `TestWorkDispatchSkipsRecoveryWithoutStaleClaim` (fresh TODO → guard skipped,
  real dispatch, attempts untouched).

`go test ./...` green under `CGO_ENABLED=0`.
</content>
</invoke>
