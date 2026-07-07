# review-already-merged-guard

Enqueued by session-audit-005 (F-1): the one live runner-gap in the pass-3 window — the
missing SYMMETRIC half of the DONE `orphan-branch-reclaim-guard` review-dispatch guard.

## Why

A review's two effects are NOT atomic: it (1) merges `bee-<taskid>` into the submodule's
tracked main and pushes to origin (durable, irreversible), then (2) commits the hive-layer
gitlink bump + `PLAN.md` `NEEDS-REVIEW -> DONE` for the runner to land. Interrupted between
them, the code is merged-and-DONE at origin while the hive PLAN still reads `NEEDS-REVIEW`
at the PRE-merge pointer, so the runner re-selects the task and spawns a WHOLE second review
just to re-discover the already-landed merge. `bounceIfUnreachable`
(`internal/swarm/swarm.go`) handles only the OPPOSITE case (recorded pointer reachable
NOWHERE -> `NEEDS-ARBITRATION`); here the pointer IS an ancestor of tracked main, so it
correctly does not fire and a redundant review is dispatched instead. Observed live:
`delivery-traceability`'s review (`bee-delivery-traceability-1783419846`, 58 turns, ~218 KB,
sonnet-5) re-fetched/re-tested/re-diffed an already-merged (`a54123c`) task to byte-identity,
then only flipped DONE — a full session spent on bookkeeping a deterministic check could have
finished for free.

## Do

One deterministic, runner-owned review-dispatch guard mirroring `bounceIfUnreachable` at the
SAME call site (top of `Runner.Run`, `Kind == Review`), reusing `internal/git` ancestor/merge-
base plumbing (`IsAncestor`, already used by `reclaimSourceBranch`) next to `CommitReachable`:

Before spawning a review, check whether `bee-<taskid>`'s recorded submodule-pointer commit is
already an ancestor of the submodule's tracked main (`origin/<branch>` in remote mode,
resolved directly against the local `<branch>` ref in local-sharing mode — derived from
remotes exactly as the orphan guard does, no mode flag). If already merged: do NOT review —
submodule main advances only via approved-review merges, so ancestry-of-main <=> a prior
approval, and auto-finalizing only completes the interrupted bookkeeping (it never approves
anything itself). Finalize by bumping the hive `submodules/<sm>/repo` gitlink to the merge
(the tracked branch's current tip), flipping `PLAN.md` `NEEDS-REVIEW -> DONE`, unlocking
dependents, recording a terse "already merged into tracked main by a prior interrupted
review; runner-finalized DONE (no re-review)" note, and publishing via the same
commit-and-publish-immediately path `Claimer.BounceUnreachable` uses (there is no later
`finish()` in a dispatch-time short-circuit to piggyback on). If NOT an ancestor, fall through
unchanged to the existing dispatch (`bounceIfUnreachable`, then a normal review session).

The three review-dispatch outcomes are then exhaustive and disjoint:
- reachable-and-merged -> finalize DONE (this guard)
- reachable-not-merged -> review (unchanged)
- unreachable -> `NEEDS-ARBITRATION` (`orphan-branch-reclaim-guard`)

Reporting/dispatch only: it never changes what a review judges when one IS spawned.

### The gitlink-bump correctness trap

Staging the bumped gitlink via `git update-index --cacheinfo` alone (without moving the
submodule checkout) looked appealing (no working-tree write) but is UNSAFE here: git always
re-reads a live nested checkout's OWN HEAD the moment ANYTHING re-`git add`s (or
`git commit <pathspec>`s) that path, silently discarding a cacheinfo-only stage — proven
empirically while building this guard. The fix mirrors the already-tested
`syncWorktreeBase` pattern instead: `git.Repo.HardReset` the (per-pass-private, never shared
across concurrent honeybees — verified via sibling `git worktree` submodule checkouts) THIS
submodule checkout to the tracked tip first, THEN let the ordinary `CommitPaths`
(`git add` + `git commit`) read its now-correct, real HEAD.

## Files

- `internal/git/git.go` — no new plumbing needed; `IsAncestor` (already added by
  `orphan-branch-reclaim-guard`) is reused as-is.
- `internal/plan/state.go` — `Task.FinalizeAlreadyMerged`: `NEEDS-REVIEW -> DONE`, releases
  the claim, appends a `Review (runner-finalized): ...` note. Mirrors `BounceUnreachable`.
- `internal/claim/claim.go` — `Claimer.FinalizeAlreadyMerged`: commits the transitioned
  `PLAN.md` + the (caller-synced) gitlink path in one commit, and publishes immediately —
  mirrors `Claimer.BounceUnreachable`.
- `internal/swarm/swarm.go` — `finalizeIfAlreadyMerged`, wired into `Run()` immediately before
  `bounceIfUnreachable` for `Kind == Review`. Resolves the recorded pointer identically to
  `bounceIfUnreachable`, fetches/resolves the submodule's tracked branch (remote or local),
  checks `IsAncestor`, and on a match syncs the submodule checkout (`HardReset`) before
  calling `Claimer.FinalizeAlreadyMerged`.
- `internal/plan/plan_test.go`, `internal/claim/claim_test.go`, `internal/swarm/swarm_test.go`,
  `internal/git/git_test.go` — see Tests below.

## Accept

A `NEEDS-REVIEW` task whose `bee-<taskid>` is already an ancestor of the submodule's tracked
main (hive PLAN still `NEEDS-REVIEW`, gitlink pre-merge) is NOT dispatched to review — the
runner flips it DONE, bumps the gitlink to the merge commit, and unlocks dependents
(regression: before the guard a full redundant review pass is spawned); a reachable-but-NOT-
ancestor commit (genuine pending work) still dispatches a normal review (no false-positive
auto-DONE); an unreachable commit still bounces to `NEEDS-ARBITRATION` via
`orphan-branch-reclaim-guard` (the two guards do not shadow each other); both local (no
origin) and remote modes resolve ancestry against the correct store; dependents unlock on the
runner-finalized DONE exactly as on a review-driven DONE; `go test ./...` green under
`CGO_ENABLED=0`.

## Tests

- `internal/plan/plan_test.go`: `TestFinalizeAlreadyMerged` (`NEEDS-REVIEW -> DONE`, claim
  released, `Attempts` untouched, note recorded), `TestFinalizeAlreadyMergedGuardedAndRequiresNote`
  (only legal from `NEEDS-REVIEW`; requires a non-empty note).
- `internal/claim/claim_test.go`: `TestFinalizeAlreadyMergedPublishesImmediately` (commits AND
  publishes synchronously; seeds a STALE pre-merge gitlink, then makes the gitlink path a real
  nested repo at a NEW sha standing in for "synced to the merged tip", and asserts the
  committed gitlink lands at that new sha — proving `CommitPaths` reads the synced checkout's
  actual HEAD rather than the stale cached pointer), `TestFinalizeAlreadyMergedGuarded` (TODO
  task errors).
- `internal/swarm/swarm_test.go`: `TestReviewDispatchFinalizesAlreadyMergedRemote` (origin
  carries both a still-unreclaimed `bee-R1` source branch AND tracked main already advanced
  PAST the implementer commit by a second, already-completed merge — proving the pre-fix
  failure mode is a genuinely redundant review dispatch, not an incidental
  `NEEDS-ARBITRATION` bounce, and that the finalized gitlink advances to the CURRENT tracked
  tip rather than merely the implementer's own sha; a `refusingClient` fails the test if a
  session is ever opened; verified to fail against the pre-fix code),
  `TestReviewDispatchFinalizesAlreadyMergedLocalSharing` (the same shape with no remote at
  all, ancestry resolved directly against the local tracked branch; verified to fail against
  the pre-fix code), and the pre-existing `TestReviewDispatchReachableLocalSharingUnchanged`
  doc comment now also calls out that it covers the guard's "genuinely pending, not yet
  merged" pass-through path.

All new/changed tests independently verified to fail (or fail to compile) against the
pre-fix code before being confirmed green against this change. `gofmt -l .` clean;
`CGO_ENABLED=0 go build ./...` and `go vet ./...` clean; `CGO_ENABLED=0 go test ./...` green
across every package.

## Caveats

- Reporting/dispatch only — never changes what a review judges when one IS spawned, and never
  approves anything: ancestry-of-main is only ever produced by a PRIOR approved-review merge,
  so finalizing on it completes bookkeeping, it does not grant a new approval.
- Shares the review-dispatch call site with `orphan-branch-reclaim-guard`'s
  `bounceIfUnreachable`; the two guards are ordered (already-merged checked first) so they
  stay disjoint rather than racing or shadowing each other.
- Honours both sharing modes (`repo/docs/sharing-modes.md`); no mode flag, derived from
  remotes exactly like the orphan guard.
