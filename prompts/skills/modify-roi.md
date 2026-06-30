# Skill: Modify an ROI

> Use when: a target's intent must change — add, drop, reprioritize, or reword
> work for `submodules/<name>/`.

`ROI.md` is the human/operator-owned record of intent. It is hook-protected: a
honeybee that tries to commit an edit to any `ROI.md` is blocked. Only the operator
(directly or via the UI editor) changes it.

## Procedure

1. Edit `submodules/<name>/ROI.md` directly and commit it. Keep entries terse and
   intent-level (the *what/why*, not the implementation).
2. Do **not** touch `PLAN.md`. The next **reconcile** pass detects that `ROI.md`'s
   head is newer than the `<!-- Beehive-ROI: <sha> -->` stamp in `PLAN.md`, folds
   the diff into the plan, re-weights tasks on the logarithmic priority scale, and
   restamps. Hand-editing `PLAN.md` to "apply" the change races that pass and is
   overwritten.
3. Verify on the next reconcile that the new stamp matches `ROI.md`'s head and the
   intended tasks appear with the expected priorities.

## Rules

- Never edit an `ROI.md` as an autonomous agent.
- Never hand-edit `PLAN.md` — reconcile owns it.
