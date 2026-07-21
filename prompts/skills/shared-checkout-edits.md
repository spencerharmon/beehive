# Skill: Edit on a shared checkout (the worktree process)

> Use when: you (a human, or an agent acting for one) share the same filesystem /
> checkout as running honeybees or a live `beehived`, and you need to change a
> submodule's code or a beehive-layer file. Do it through the worktree process every
> other component uses — never by editing the live checkout in place.

## Why not just edit the files

The live checkout is **derived state, not an editing surface**:

- The beehive repo's working tree on `main` is a **pure projection of committed
  history**. The publisher (honeybees, the `beehived` editor) treats any
  uncommitted drift in it as corruption: on the next publish it **resets the tree to
  HEAD and force-re-checks-out submodules** (the dirty-tree heal). Edits you make
  directly in the live tree are silently discarded — and worse, a wedged dirty tree
  can block every component's publish.
- `submodules/<name>/repo/` is the **shared submodule checkout**. Honeybees are told
  explicitly never to write it; its commit only ever *follows* the tracked branch
  (`beehive submodule sync` clobbers it to the upstream tip verbatim). Anything you
  author there is lost on the next sync.

So every component instead works a **private worktree branched off the freshest
`main`/tip, commits there, and converges by merging/pushing back to `main`**. No
shared index, no write lock, conflict-free. Your manual edit is just one more
participant in that same protocol.

## Edit a submodule's CODE

1. Cut a target-repo worktree off the synced tracked tip:
   ```
   beehive submodule worktree add <submodule> <branch>
   # -> submodules/<submodule>/worktrees/<branch>/
   ```
2. Edit and **commit inside that worktree** (`submodules/<submodule>/worktrees/<branch>/`).
   Never touch `submodules/<submodule>/repo/`.
3. Land the commit on the submodule's tracked branch — the beehive pointer follows
   it. From the worktree: `git push origin HEAD:<tracked-branch>` (or open a PR and
   let it merge). For the self-hosting beehive submodule the tracked branch is
   `origin/main`, the single source of truth the deploy reads.
4. Advance the recorded pointer and tidy up:
   ```
   beehive submodule sync <submodule>          # bump the gitlink to the new tip
   beehive submodule worktree rm <submodule> <branch>
   ```
   (A running honeybee's next sync would also advance the pointer; doing it
   explicitly keeps the projection current immediately.)

> This `submodule sync` fast-forwards the gitlink to the ALREADY-TRACKED branch tip (step 3 landed the
> commit on the tracked branch first) — which is the ONLY value a submodule pointer may ever hold
> (see `submodules/<sm>/repo`'s tracked branch in `.gitmodules`; and
> `submodules/beehive/docs/submodule-pointer-invariant.md`). A pointer must NEVER be set to a
> `bee-<taskid>` tip or any other commit: a Work-task honeybee does NOT bump the pointer at all — the
> runner owns the gitlink and pins it to the tracked-branch tip. Never run
> `git update-index --cacheinfo 160000,<sha>,submodules/<sm>/repo` to point the gitlink anywhere but
> the tracked-branch tip.

## Edit a BEEHIVE-LAYER file (superproject)

Beehive-layer files live in the superproject, not in `repo/`: `INFRASTRUCTURE.md`,
`SUBMODULE-LINKS.yaml`, the root instruction files, and a submodule's
`INFRASTRUCTURE.md` / `ARTIFACTS.md` / `docs/`.

- **`ROI.md`, `INFRASTRUCTURE.md`, and `SUBMODULE-LINKS.yaml`: prefer the `beehived`
  editor UI.** It opens an edit worktree off `main`, lets you (or its agent) change
  the one file, and merges to `main` for you — the exact same worktree process,
  done end-to-end. `ROI.md` is human-owned and is *only* editable this way (agents
  are hook-blocked from committing it; see the `modify-roi` skill).
- **By hand**, for any superproject file the editor does not cover:
  ```
  beehive worktree add <branch>                 # -> .worktrees/<branch>/ off main
  # edit the file under .worktrees/<branch>/ and commit it there
  git -C .worktrees/<branch> push . HEAD:main   # publish: updateInstead advances the
                                                # live tree (local-only hive). With a
                                                # remote, push to origin/main instead.
  beehive worktree rm <branch>
  ```
  This `push … HEAD:main` is exactly what the components' publisher does; it
  fast-forwards/merges and the hive's `updateInstead` updates the live working tree.

## Rules

- **Never author in the live checkout** — not the primary tree on `main`, not
  `submodules/<name>/repo/`. Use a worktree; converge by publishing to `main`.
- Never `git reset`/`checkout`/`stash` the live primary tree to "make room" for an
  edit — you race in-flight publishes.
- Never hand-edit `PLAN.md` (reconcile owns it) and never edit any `ROI.md` as an
  agent.
- Do not remove a worktree, branch, or checkout that a running pass is using, and
  never kill a running pass, without operator approval (see `LOCALS.md`).
- Always remove your worktree + branch when done so they do not look like live work.
