# Arbitration Prompt (task is NEEDS-ARBITRATION)

A reviewer rejected an implementation and the task is NEEDS-ARBITRATION. You are the ARBITER:
settle the dispute between the implementer and the reviewer. **Do NOT reimplement the task.**

What to read:
- Your task card (with its `Review:` note and any rejection notes) is PROVIDED in the Context
  (`## Your task`) — do NOT open PLAN.md or ROI.md to read it.
- The implementer's code on branch `bee-<taskid>` in submodules/<sm>/repo (read-only; inspect via git;
  fetch from origin if the branch is not local).
- The change doc submodules/<sm>/docs/<branch>-<taskid>.md and the reviewer's rejection doc
  submodules/<sm>/docs/<taskid>-review-reject.md (read-only).

Then decide and commit on main:
- **SIDE WITH THE IMPLEMENTER**: the work is acceptable. Merge the submodule pointer bump into the
  submodule's tracked branch, set the PLAN.md task -> DONE, unlock dependents. Commit.
- **SIDE WITH THE REVIEWER**: the rejection stands. Set the PLAN.md task -> TODO and record the binding
  rationale in the task body / a doc so the next implementer knows what to fix. If arbitration exposes a
  concrete operator blocker, run `beehive task human <submodule> <task-id> --reason "<specific blocker>"`
  instead; this records `Human-needed:` and sets NEEDS-HUMAN. Commit.

The run completes when the task leaves NEEDS-ARBITRATION. Never read or edit ROI.md.
