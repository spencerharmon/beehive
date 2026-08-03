# review-revert content-finalize spec - the already-merged finalize shortcut vs a revert-after-merge

## Problem (live false-DONE, flux:omada-ingress-tls, 2026-08-02)

The interrupted-review finalization shortcut (`swarm.go finalizeIfAlreadyMerged`,
`plan/state.go FinalizeAlreadyMerged`) completes a review whose merge already
landed on the tracked branch but whose session died before flipping the status -
"already merged into tracked main (7f36d00) ... runner-finalized DONE (no
re-review, branch not required)". It concludes "already merged" from commit
ANCESTRY (the merge commit stays reachable in `main`'s history), NOT from the
reviewed tree CONTENT surviving.

Ancestry != content. A revert-after-merge (`7f36d00 Revert "Merge branch
bee-omada-ingress-tls"`, 14 min after the merge `6f34902`) deletes every
delivered file while the original merge commit stays an ancestor of `main`. So
the ancestry test reads green while the effect is gone: the shortcut finalizes
DONE with `commits=none`, the ingress absent from the cluster, and the target's
own `Check:` never run ("branch not required"). Same corpus-eating shape as the
empty-checksum canonical bug, one layer down on the Review finalize path - a
`NoFalseDone` hole specific to the Review shortcut.

This task is the SPEC-FIRST half: reproduce the invariant violation in TLA+ and
lock the fixed contract before the Go fix (`review-revert-content-finalize-fix`,
which depends on this) lands.

## Model (`specs/TaskStatus.tla`)

- VARIABLE `contentPresent` - the reviewed tree CONTENT actually survives on the
  tracked branch. A merge sets it TRUE alongside `merged`; a revert sets it FALSE
  while `merged` (ancestry) stays TRUE. Ancestry != content, made first-class.
- `MergeLandsInterrupted` - the runner's merge landed on the tracked branch
  (`merged=TRUE`, `contentPresent=TRUE`) but the session was interrupted before
  flipping DONE, so the on-disk status is still REVIEW/ARB. This is the exact
  state the finalize shortcut exists to complete (and is what makes
  `FinalizeAlreadyMerged` reachable in the model at all).
- `RevertMerge` (adversary, NOT forced) - a `Revert "Merge branch bee-..."`
  lands: `contentPresent -> FALSE`, `merged` (ancestry) UNCHANGED. The live
  omada-ingress-tls shape.
- `FinalizeAlreadyMerged` now carries `(ContentFinalize => contentPresent)`.
  CONSTANT `ContentFinalize`:
  - FALSE (the bug) - ancestry-only finalize: flips DONE on `merged` alone.
  - TRUE (the fix) - refuses DONE unless the content is present on `main` AND
    (existing gate) the declared `Check:` passes.
- `ReopenReverted` (fix-only, `ContentFinalize`) - a merge whose content was
  reverted away (`merged /\ ~contentPresent`) is NOT a completable interrupted
  review; the runner re-opens it for rework (clears `merged`, `attempts++`, past
  `Limit -> NEEDS-HUMAN`) instead of finalizing on bare ancestry. Keeps the task
  terminating; the revert/re-open loop is bounded by the attempts budget.
- `NoFalseDone` strengthened: `status = "DONE" => workDurable /\ merged /\
  contentPresent /\ DodSatisfied` (and `ArtifactsPresent`'s DONE arm likewise).

## cfgs (wired into `specs/run-tlc.sh`)

- `TaskStatus_revertfinalize_buggy.cfg` - `ContentFinalize=FALSE`, every other
  DONE gate ON (Gated/CheckGated/RevertOverPin all TRUE) to isolate this Review
  content hole. MUST reproduce `NoFalseDone` violated.
- `TaskStatus_revertfinalize_fixed.cfg` - `ContentFinalize=TRUE`. MUST hold
  `NoFalseDone`/`LegalTransitionsOnly`/`AttemptsBounded`/`Terminates`/
  `LostWorkRecovers` across the full revert trace.

The seven pre-existing `TaskStatus_*.cfg` gained `ContentFinalize = TRUE` (the
fixed behavior) so their declared outcomes are unchanged.

## Fix contract for the Go half (`review-revert-content-finalize-fix`)

`finalizeIfAlreadyMerged` must NOT conclude DONE from ancestry
(`git merge-base --is-ancestor`) alone. It must verify the reviewed content is
actually present in the tracked-branch tree AND run the task's `Check:` on that
tree, exactly as a normal review approve does; a reverted merge is re-opened for
rework, never finalized. Modeled by `ContentFinalize=TRUE` + `ReopenReverted`.

## Verification (run-tlc.sh, TLC 2.19)

`Check: ./specs/run-tlc.sh` - full suite passes (fixed cfgs pass, buggy cfgs
reproduce). The two new cases:

```
OK   TaskStatus_revertfinalize_fixed.cfg (expected pass)
OK   TaskStatus_revertfinalize_buggy.cfg (expected fail)
```

Buggy counterexample (ancestry-only finalize reaches DONE with content absent):

```
State 1  <Initial>               status=TODO   merged=FALSE contentPresent=FALSE
State 2  <DoWork>                 workDurable=TRUE
State 3  <HandoffToReview>        status=REVIEW
State 4  <MergeLandsInterrupted>  merged=TRUE  contentPresent=TRUE
State 5  <RevertMerge>            merged=TRUE  contentPresent=FALSE  (ancestry kept, content gone)
State 6  <FinalizeAlreadyMerged>  status=DONE   merged=TRUE  contentPresent=FALSE
         => Invariant NoFalseDone is violated.
```

Fixed cfg: `Model checking completed. No error has been found.` (1744 distinct
states) - `FinalizeAlreadyMerged` is refused while `contentPresent=FALSE`, the
reverted merge re-opens via `ReopenReverted`, and the task still terminates.
