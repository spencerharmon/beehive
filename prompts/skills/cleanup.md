# Skill: Cleanup

> Use when: the repo accumulates stale worktrees, orphan gitlinks, drifted submodule
> checkouts, abandoned session branches, or zombie task claims.

## What accumulates

- **Stale worktrees** under `submodules/<name>/worktrees/` from capped or crashed
  passes.
- **Drifted submodule checkouts** (`M repo`) — the gitlink and the checked-out
  commit diverge. This is *derived* state, never authored: reset it to the recorded
  gitlink, do not commit the drift.
- **Dead session branches** left after a pass exits.
- **Zombie task claims** — a `.bee-lock-*` whose holder process is gone but whose TTL
  has not yet expired, blocking selection of that task.

## Procedure

1. Run the `beehive-hygiene` skill / script for the routine sweep.
2. Resync a drifted checkout: `beehive submodule sync <name>` (resets `repo/` to its
   recorded gitlink).
3. Prune stale `worktrees/` and dead session branches.
4. Zombie claims clear themselves at TTL. Do **not** force-clear a claim whose holder
   may still be alive — confirm the holder is dead first, and never kill a live pass
   without operator approval (see `LOCALS.md`).

## Healthy state

Clean tree, every gitlink checked out at its recorded commit, no worktrees for
finished tasks, no zombie claims past TTL.
