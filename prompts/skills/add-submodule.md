# Skill: Add a submodule

> Use when: a new target repository should be brought under the swarm.

A "submodule" here is a target whose source is tracked as a git submodule under
`submodules/<name>/repo/`, wrapped by the beehive coordination layer (`ROI.md`,
`PLAN.md`, `docs/`, `sessions/`, `worktrees/`).

## Procedure

1. `beehive submodule add <name> <git-url>` — registers the source as a submodule at
   `submodules/<name>/repo/`, scaffolds the beehive layer, and updates `.gitmodules`.
2. Author `submodules/<name>/ROI.md` — the intent for this target. This is the only
   input a bootstrap pass needs. (See the `modify-roi` skill for ownership rules.)
3. Optionally record cross-target dependencies with
   `beehive submodule link <a> <b>` (writes `SUBMODULE-LINKS.yaml`).
4. The next **bootstrap** pass (ROI present, PLAN absent) decomposes `ROI.md` into a
   weighted `PLAN.md`. Nothing to do by hand — verify `PLAN.md` appears.

## Rules

- Do not commit a `PLAN.md` yourself; bootstrap owns the first one.
- The submodule's own source is never polluted with beehive state — all beehive
  files live in `submodules/<name>/`, outside `repo/`.
