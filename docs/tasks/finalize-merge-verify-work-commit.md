# finalize-merge-verify-work-commit

Close the remaining proxy-sha vector in the finalize-already-merged guards: a
recorded `review=<sha>` stamp that is an ancestor of tracked main but is a
PARENT/proxy of the task's own work commit — never the work commit itself —
was letting `finalizeIfMergedByRecord` close a task DONE without the task's
real code ever landing (observed live: nixconf `wireguard-yoga-profile` /
`wireguard-yoga-build-verify`, recorded `review=b0895eb3bb47f0291edb5f306f50fb3acef80eba`,
the `wireguard-yoga-secret` task's OWN commit and a parent of the profile's
real code commit `8f6779b`, never merged).

## What changed

- `internal/swarm/swarm.go` `recordReviewedCommit`: now takes the Work pass's
  own worktree `*git.Repo` (`wg`) and stamps `wg`'s HEAD — the commit this pass
  actually authored and pushed as `bee-<taskid>` — instead of reading
  `HEAD:submodules/<sm>/repo` off the beehive-layer checkout. That ambient path
  is the single shared gitlink slot: it can read back whichever task most
  recently bumped it (a parent/proxy of THIS task's commit, or another task's
  commit outright) rather than this task's own work. Both call sites (Work
  dispatch's `NEEDS-REVIEW`/`NEEDS-ARBITRATION` completion paths) now pass `wg`.
- `finalizeIfMergedByRecord`: when `bee-<taskid>` still resolves anywhere
  (remote ls-remote or a local ref), its own tip is now authoritative over the
  recorded stamp for the ancestry test — defense-in-depth for a task reviewed
  before this fix landed, whose recorded sha may still be a stale parent/proxy.
  Only once the branch is confirmed gone/reused does the recorded sha stand
  alone (the shape the guard exists for).
- Both `finalizeIfAlreadyMerged` and `finalizeIfMergedByRecord` gained a
  completion-check assertion (`docFilesLandedInTree`/`parseDocFiles`): once the
  ancestry check finds a merge, it additionally confirms the task's own change
  doc's `Files:` entries actually exist in the merged tree before finalizing.
  Fails open on any read/parse gap (never blocks a legitimate finalize on a
  doc-format difference); only a file CONFIRMED absent blocks.

## Deliberately unchanged

- The zero-diff guard (`sourceBranchExists`'s "never existed" no-branch shape),
  the review-finalize-branch-ancestor-gap fix (branch's own tip, never the
  ambient pointer, for `finalizeIfAlreadyMerged`), and the genuine
  interrupted-review-finalize path are all untouched — every existing
  `TestReviewDispatch*` case still passes unmodified.
- `claim.RecordReviewCommit`/`plan.Task.SetReviewCommit` are unchanged: they
  already durably stamp whatever sha they are handed; the bug was solely in
  what sha `recordReviewedCommit` computed to hand them.

## Regression tests (`internal/swarm/swarm_test.go`)

- `TestReviewDispatchDoesNotFinalizeByRecordWhenBranchTipDiverges` reproduces
  the exact nixconf shape: recorded sha is a parent (itself an ancestor of
  main) while `bee-R1`'s own tip is a sibling commit that was never merged.
  Fails red pre-fix (wrongly auto-finalizes), passes post-fix (dispatches a
  real review instead).
- `TestReviewDispatchDoesNotFinalizeByRecordWhenDocFilesMissing` reproduces the
  completion-check-assertion gap: the recorded sha really is merged, but the
  doc's `Files:` entries are absent from that tree. Fails red pre-fix, passes
  post-fix.
- `go test ./internal/swarm/... ./internal/git/... ./internal/claim/...` green
  (`CGO_ENABLED=0`), including all pre-existing `TestReviewDispatch*` cases.

## Sweep

Swept every locally-checked-out submodule's `PLAN.md` for `review=<sha>`
stamps on `DONE`/terminal tasks. `nixconf` confirms the ROI's own citation: the
`wireguard-yoga-secret` / `wireguard-yoga-profile` / `wireguard-yoga-build-verify`
trio all carry the identical `review=b0895eb3bb47f0291edb5f306f50fb3acef80eba` —
`wireguard-yoga-secret`'s own commit, a parent of the profile's real code commit
`8f6779b` (per the ROI diff; `submodules/nixconf/repo` is not checked out on
this host to re-derive ancestry independently). This is the SAME confirmed
phantom the ROI names, not a newly discovered one.

Additional SUSPECT clusters found by the same shape (multiple, otherwise-
unrelated tasks sharing one identical `review=<sha>` — the trivially-an-
ancestor-of-main signature this whole task exists to stop trusting blindly),
found by grep alone (no submodule checkout available on this host for
flux/nixconf to independently re-derive per-task ancestry, so these are
NAMED but NOT individually ancestry-confirmed the way wireguard-yoga is):
- `flux/PLAN.md`: `zuul-beehive-tenant-config-stale`,
  `gitea-post-receive-500-ratio-elevated`, `flux-health-audit-109`,
  `wireguard-yoga-peer-register`, `zuul-scheduler-config-repo-startup-order`
  all share `review=bc804c336c7f5b59e42d2bea08324cb1ae46dfac`.
- `nixconf/PLAN.md`: `nixpkgs-pick-target-rev`,
  `nixpkgs-enumerate-breaking-changes`, `nixpkgs-grub-acceptance-test` share
  `review=1a3b814b20d87d9fd7117d686869418332f2e9c1`; `nixpkgs-build-test-hosts`,
  `nixpkgs-bump-drop-overlay` share `review=00a0119732a3ff662a9c0d2a3d00b0acbd9ce477`.
- `beehive/PLAN.md` itself: `session-audit-023`, `review-doc-only-branch-hint`,
  `lost-work-recover-any-status`, `chat-editor-working-indicator-clear` share
  `review=f6aa6a393204f55e3b36ef9f2fe682c80ee7b02d`. At least `session-audit-023`
  and `review-doc-only-branch-hint` are zero-code-diff/doc-only tasks by name —
  the shape `sourceBranchExists`'s own doc already calls out as a LEGITIMATE
  reason to trivially share the ambient pointer (no code diff ever moves it),
  so this cluster is lower-confidence as an actual phantom than the flux/nixconf
  ones above, which chain real cross-submodule/build-verify code work.

None of these are re-derived with a real `IsAncestor`/tree-contents check here
(no submodule checkout for flux/nixconf on this host, and `internal/swarm/*.go`
is this task's only authorized `Files:` scope — nixconf's and flux's `PLAN.md`
are not this task's to hand-edit per AGENTS.md's shared-checkout-edits rule).
Recommending an operator-directed or reconcile-owned follow-up audit task in
each of `flux` and `nixconf` to run the same `IsAncestor(workBranchTip,
trackedMain)` + doc-files-in-tree check this fix now performs automatically,
against each named cluster, and re-land any confirmed phantom.

