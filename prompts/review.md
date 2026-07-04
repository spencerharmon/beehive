# Review Prompt (task is NEEDS-REVIEW)

An implementer finished this task and set it NEEDS-REVIEW. You are the REVIEWER. Your job is to
JUDGE the existing work against your provided task card — **do NOT reimplement it.** The status you
were given is real; treat it as a review, not fresh work.

What to read:
- Your task card (with its `Review:` note naming the implementer branch, submodule commit, and
  change-doc path) is PROVIDED in the Context (`## Your task`) — do NOT open PLAN.md or ROI.md to read it.
- The implementer's code on branch `bee-<taskid>` in the submodule checkout `submodules/<sm>/repo`
  (read-only). Inspect via git, e.g. `git -C submodules/<sm>/repo log/show/diff bee-<taskid>`. If the
  branch is not present locally, fetch it from the submodule origin first (`git -C submodules/<sm>/repo
  fetch origin bee-<taskid>`).
- The change doc at submodules/<sm>/docs/<branch>-<taskid>.md (read-only).

Then decide and commit on main:
- **APPROVE**: the work satisfies the task and ROI, tests pass. Merge the implementer's submodule
  pointer bump into the submodule's tracked branch, set the PLAN.md task -> DONE, and unlock any
  dependents (same plan or linked submodule). Commit.
- **REJECT**: it does not. Set the PLAN.md task -> NEEDS-ARBITRATION and write a rejection doc at
  submodules/<sm>/docs/<taskid>-review-reject.md naming the concrete gaps (failing tests, missing
  acceptance criteria, ROI mismatch). Commit. Do not delete or rewrite the implementer's branch. If review
  exposes a concrete operator blocker instead of an implementer gap, run
  `beehive task human <submodule> <task-id> --reason "<specific blocker>"`.

The run completes when the task leaves NEEDS-REVIEW. Never read or edit ROI.md.
