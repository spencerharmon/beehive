# Skill: Rebootstrap

> Use when: a target's plan must be rebuilt from scratch — e.g. `ROI.md` was
> rewritten wholesale and an incremental reconcile would not converge cleanly.

Rebootstrap is the heavy hammer. For ordinary ROI edits, prefer the `modify-roi`
skill and let reconcile fold the diff incrementally.

## Procedure

1. **Drain in-flight work.** Settle or retire claimed/`NEEDS-REVIEW` tasks so a
   regenerated plan does not collide with running passes. Never kill a live pass
   without operator approval (see `LOCALS.md`).
2. Delete `submodules/<name>/PLAN.md` and commit.
3. The next **bootstrap** pass (ROI present, PLAN absent) regenerates `PLAN.md` from
   the current `ROI.md`.
4. Surviving `docs/` remain as history; existing worktrees should already be drained.

## Rules

- Do not rebootstrap to "fix" a small ROI change — that throws away task priority
  history and in-flight context. Reconcile is the right tool for incremental edits.
