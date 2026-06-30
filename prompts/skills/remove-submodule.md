# Skill: Remove a submodule

> Use when: a target should leave the swarm (intent retired, project archived).

## Procedure

1. **Drain in-flight work first.** If `PLAN.md` has tasks that are claimed or in
   `NEEDS-REVIEW`/`NEEDS-ARBITRATION`, let them settle or retire them explicitly so
   nothing is silently dropped. A reconcile pass moves in-flight retirees to
   `NEEDS-REVIEW` rather than deleting them.
2. `beehive submodule rm <name>` — deregisters the gitlink, removes the
   `submodules/<name>/` tree, and drops the `.gitmodules` entry.
3. Remove any `SUBMODULE-LINKS.yaml` cross-links that referenced `<name>`.
4. Commit. Surviving `docs/` history for the target is gone with the tree; if you
   need to keep the change record, copy it out before removal.

## Rules

- Never remove a target with active honeybee worktrees/claims — that orphans the
  running passes. Drain first (and never kill a running pass without operator
  approval; see `LOCALS.md`).
