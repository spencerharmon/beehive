# merged-guard-branch-gate

Enqueued by session-audit-007 (Finding #1): the flagship gap in `review-already-merged-guard`
(F-1). Its `finalizeIfAlreadyMerged` (`internal/swarm/swarm.go`) auto-finalizes ANY
`NEEDS-REVIEW` task whose recorded submodule pointer is an ancestor of tracked main — but for a
ZERO-code-diff task that ancestry is trivially true regardless of whether any review happened.

## Why

`finalizeIfAlreadyMerged` took no branch parameter (unlike its sibling `bounceIfUnreachable`,
which is handed `res.Branch`): it only checked whether the recorded gitlink is already an
ancestor of the submodule's tracked main, and `git merge-base --is-ancestor A B` reports true
whenever `A == B`. A diagnose-only Work pass (the whole `session-audit-NNN` family, by design)
never bumps the gitlink, so the recorded pointer still EQUALS main's tip and the ancestry check
is trivially satisfied — the guard then finalized the task straight to DONE with zero review
turns, having confirmed nothing.

Confirmed firing live on `session-audit-006`: claimed for WORK 09:24:09, flipped `NEEDS-REVIEW`
09:49:36, claimed for REVIEW by a different session 09:52:09, and 2 seconds later (09:52:11)
finalize-already-merged fired straight to DONE citing "already merged ... by a prior interrupted
review" against `dbd33f15` — which was simply `review-already-merged-guard`'s OWN pre-existing
implementation commit, since `session-audit-006` made zero submodule code changes. No review
session transcript exists for that claim (zero agent turns, zero judgment of the deliverables)
before DONE. Genuine code-diff reviews in the same window (`audit-warning-tail-guard`,
`session-audit-quickstart`, `review-already-merged-guard` itself) correctly dispatched real
review sessions — the defect is precisely, only, and reproducibly the zero-diff edge case.

`sg.CommitReachable` (`bounceIfUnreachable`'s check) is NOT sufficient to distinguish the two:
an unmoved pointer is trivially "reachable" too — the same triviality that defeats `IsAncestor`.

## Do

Gate `finalizeIfAlreadyMerged`'s ancestry shortcut on FIRST confirming `bee-<taskid>` is a real,
resolvable ref. Only a genuine interrupted review leaves an implementer branch behind:

- **remote mode** — reuse `reclaimSourceBranch`'s existing `sg.LsRemoteBranch` plumbing
  (`ls-remote --heads`, no fetch); a non-empty advertised tip means the branch exists.
- **local-sharing mode** (no remote) — the equivalent local-ref lookup, new
  `git.Repo.LocalBranchExists` (`git show-ref --verify --quiet refs/heads/<branch>`).

If `bee-<taskid>` resolves NOWHERE (no remote head, no local ref), return `(false, nil)` and fall
through to normal dispatch — a real review session opens and judges the pass's deliverables —
instead of finalizing. A remote query error surfaces as `(false, err)` so the caller fails OPEN
(dispatch), never a false finalize. `finalizeIfAlreadyMerged` now takes the `branch` parameter
(passed `res.Branch` at the call site, exactly like `bounceIfUnreachable`); a small
`sourceBranchExists` helper wraps the remote-or-local decision. Nothing else in the finalize
path (the `HardReset` sync, the `Claimer.FinalizeAlreadyMerged` commit-and-publish, or the note
format) changes.

The three review-dispatch outcomes stay exhaustive and disjoint; the already-merged guard now
additionally requires the source branch to have existed:
- merged AND `bee-<taskid>` existed -> finalize DONE (this guard, now gated)
- merged-but-no-branch (zero-diff) / reachable-not-merged -> review (dispatch)
- unreachable -> `NEEDS-ARBITRATION` (`bounceIfUnreachable`)

## Files

- `internal/git/git.go` — new `LocalBranchExists(ctx, branch)`: `show-ref --verify --quiet`
  exit 0 -> exists, exit 1 -> `(false, nil)`, any other exit -> real error. The local-sharing
  counterpart of `LsRemoteBranch`.
- `internal/swarm/swarm.go` — `finalizeIfAlreadyMerged` gains a `branch` param and the
  branch-existence gate; new `sourceBranchExists` helper (remote `LsRemoteBranch` else local
  `LocalBranchExists`); call site passes `res.Branch`.
- `internal/git/git_test.go`, `internal/swarm/swarm_test.go` — see Tests.

## Accept

A `NEEDS-REVIEW` task whose recorded gitlink is unchanged from (equal to) the submodule's tracked
main tip AND whose `bee-<taskid>` branch never existed anywhere (no local ref, no remote ref)
does NOT auto-finalize — a normal review session opens instead (regression: today wrongly
auto-DONEs with zero turns), covering both remote and local-sharing modes. The existing
`TestReviewDispatchFinalizesAlreadyMergedRemote` / `...LocalSharing` (genuine merged-but-
interrupted, `bee-R1` present as an unreclaimed source branch) keep finalizing; a task whose
`bee-<taskid>` exists but is not yet an ancestor of tracked main still dispatches a normal review
(unchanged). `go test ./internal/swarm/... ./internal/git/... ./internal/claim/...` green under
`CGO_ENABLED=0`; no change to the real already-merged finalize path or its note format.

## Tests

- `internal/git/git_test.go`: `TestLocalBranchExists` — present branch -> true; absent branch ->
  `(false, nil)` (no error); keyed on the ref NAME not the commit (a bare sha-as-name reads
  absent).
- `internal/swarm/swarm_test.go`:
  - `TestReviewDispatchZeroDiffNoBranchDispatchesRemote` / `...LocalSharing` (new) — gitlink ==
    tracked main tip, no `bee-R1` anywhere: a real review dispatches (asserts `prompts != 0` and
    NO runner-finalize note), covering both modes. Independently verified to FAIL against the
    ungated code (auto-DONE, zero turns).
  - `TestReviewDispatchFinalizesAlreadyMergedLocalSharing` — updated to seed the unreclaimed
    `bee-R1` LOCAL ref a genuine interrupted review leaves behind (the shape the gate now
    requires); still finalizes via `refusingClient`.
  - `TestReviewDispatchFinalizesAlreadyMergedRemote` (already pushes `bee-R1` to origin) and
    `TestReviewDispatchReachableLocalSharingUnchanged` (creates `bee-R1`, never merged) pass
    unmodified.

`CGO_ENABLED=0 go build ./...`, `go vet`, and `CGO_ENABLED=0 go test ./internal/swarm/...
./internal/git/... ./internal/claim/...` all green.

## Caveats

- Reporting/dispatch only — never changes what a review judges when one IS spawned, and never
  approves anything.
- A stale `bee-<taskid>` from a prior orphaned attempt counts as "existed" in remote mode; that
  is harmless because the guard still additionally requires the recorded pointer to be an
  ancestor of tracked main, which a never-merged orphan is not.
- Honours both sharing modes (`repo/docs/sharing-modes.md`); no mode flag, derived from remotes
  exactly like `LsRemoteBranch` / `CommitReachable`.
