# Skill: Edit on a shared checkout (the worktree process)

> Use when: you (a human, or an agent acting for one) share the same filesystem /
> checkout as running honeybees or a live `beehived`, and you need to change a
> submodule's code or a beehive-layer file. Do it through the worktree process every
> other component uses — never by editing the live checkout in place.

## Why not just edit the files

The live checkout is **derived state, not an editing surface**:

- The beehive repo's working tree on `main` is a **pure projection of committed
  history**. On the next publish the runner/`beehived` resets it to HEAD and
  force-re-checks-out submodules (the dirty-tree heal) — an in-place edit is
  silently discarded, and a wedged dirty tree can block every component's publish.
- `submodules/<name>/repo/` is the **shared submodule checkout**; `beehive submodule
  sync` clobbers it to the tracked tip verbatim. Anything authored there is lost on
  the next sync.

So every component works a **private worktree branched off the freshest `main`/tip,
commits there, and converges by merging/pushing back to `main`**. No shared index,
no write lock, conflict-free. Your edit is just one more participant in that same
protocol.

## Edit a submodule's CODE

1. Cut a target-repo worktree off the synced tracked tip:
   ```
   beehive submodule worktree add <submodule> <branch>
   # -> submodules/<submodule>/worktrees/<branch>/
   ```
2. Edit and **commit inside that worktree**. Never touch `submodules/<submodule>/repo/`.
3. Land the commit on the submodule's tracked branch — the beehive pointer follows
   it: `git push origin HEAD:<tracked-branch>` from the worktree (or a PR that
   merges). The self-hosting beehive submodule tracks `origin/main`.
4. Advance the recorded pointer and tidy up:
   ```
   beehive submodule sync <submodule>          # bump the gitlink to the new tip
   beehive submodule worktree rm <submodule> <branch>
   ```

> `submodule sync` fast-forwards the gitlink to the ALREADY-TRACKED branch tip (step
> 3 landed the commit there first) — the ONLY value a pointer may ever hold (see
> `submodules/beehive/docs/submodule-pointer-invariant.md`). Never point it at a
> `bee-<taskid>` tip or run `git update-index --cacheinfo` on it: a Work-task
> honeybee does not touch the pointer at all — the runner owns and pins it.

## Edit a BEEHIVE-LAYER file (superproject)

Beehive-layer files live in the superproject, not in `repo/`: `ROI.md`,
`INFRASTRUCTURE.md`, `SUBMODULE-LINKS.yaml`, and a submodule's `INFRASTRUCTURE.md` /
`ARTIFACTS.md` / `RULES.md` / `AGENTS.md`. Use `beehive edit` — one deterministic
call replacing the manual worktree -> write -> commit -> push -> cleanup sequence:

```
beehive edit <repo-relative-file> --content-file <new-content> [--message "<msg>"]
```

Content can also be piped via stdin. A whole-file deletion of a human-owned file
(`ROI.md`) is refused unless `--confirm-delete` is given; do not add manual
worktree/push/cleanup steps around this command. `ROI.md` hook-blocks a honeybee
commit regardless of path (see `modify-roi`); prefer the `beehived` editor UI for it
when a human is driving interactively.

The root instruction files (`AGENTS.md`, `HONEYBEE.md`, `BOOTSTRAP.md`,
`skills/*.md`) are NOT edited this way — they are GENERATED from the `beehive`
submodule's `prompts/` templates; edit the source there (Submodule CODE, above) and
let `beehive instruction update` re-render the root copy.

## Rules

- **Never author in the live checkout** — not the primary tree on `main`, not
  `submodules/<name>/repo/`. Use a worktree (submodule code) or `beehive edit`
  (beehive-layer files).
- Never `git reset`/`checkout`/`stash` the live primary tree to "make room" for an
  edit — you race in-flight publishes.
- Never hand-edit `PLAN.md` (reconcile owns it) and never edit any `ROI.md` as an
  agent.
- Do not remove a worktree, branch, or checkout that a running pass is using, and
  never kill a running pass, without operator approval (see `LOCALS.md`).
- Always remove your worktree + branch when done (`beehive edit` already does this
  for you).
