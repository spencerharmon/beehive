# Skill: Modify an ROI

> Use when: a target's intent must change — add, drop, reprioritize, or reword
> work for `submodules/<name>/`.

`ROI.md` is the human/operator-owned record of intent. It is hook-protected: a
**honeybee** (autonomous pass, `BEEHIVE_HONEYBEE=1`) that tries to commit an edit to
any `ROI.md` is blocked. Operator-directed edits ARE allowed — the operator
directly, the beehived ROI/generic editor, or an agent acting on the hive repo when
the operator directs it.

## NEVER commit an ROI edit on the live `main` tree

The live `main` working tree and every `submodules/<name>/repo/` checkout are
**derived state, not editing surfaces**. Honeybee passes and `beehived` share that
filesystem and converge by merging worktree branches into `main` and pushing them to
the hive's publish ref. A raw `git commit` on the primary `main` checkout becomes a
**stray diverging head that only exists locally**: the swarm keeps pushing to the
hive remote (e.g. `gitea/main`), your commit never goes up, and the next
`git pull --ff-only` cannot fast-forward divergent histories → the pull stalls and
every component's publish backs up behind it. This wedges the whole repo.

So an operator-directed ROI edit is **not** a plain "edit the file and commit." It
MUST land through one of the two publish paths in the Procedure below — the
worktree/editor flow is **mandatory, not a suggestion**. Editing the file in place
on the live checkout is doubly wrong: a running pass's dirty-tree heal silently
resets it before you even commit.

## Procedure

Pick ONE of these two paths. Both land the edit as a proper participant in the
merge/publish protocol.

**Path A — the `beehived` ROI/generic editor UI (preferred).** Open the target's
`ROI.md` in the beehived editor and save. It commits and publishes through the
sanctioned flow for you; you never touch the live checkout.

**Path B — the beehive-layer worktree flow.** `ROI.md` is a beehive-layer file, so
use the superproject worktree, not a submodule worktree:

1. `beehive worktree add <branch>` — creates `.worktrees/<branch>/` off the freshest
   `main`.
2. Edit `.worktrees/<branch>/submodules/<name>/ROI.md` (NOT the live
   `submodules/<name>/ROI.md`). Keep entries terse and intent-level (the *what/why*,
   not the implementation). Commit inside the worktree.
3. Publish to the hive's `main` **publish ref**. Check `git remote -v` first — the
   remote may be named `gitea`, `origin`, or anything else; use whatever it is:
   - if the hive has a remote (call it `<hive-remote>`):
     `git -C .worktrees/<branch> push <hive-remote> HEAD:main`;
   - only if the hive is genuinely local-only (no remote, `updateInstead`):
     `git -C .worktrees/<branch> push . HEAD:main`.
   Verify with `git -C .worktrees/<branch> status` / a `git fetch` that the divergence
   against the hive publish ref is `0 0` — a non-zero ahead count means your commit is
   a stray local head; fix it before proceeding.
4. `beehive worktree rm <branch>`.

In **both** paths: do **not** touch `PLAN.md`. The next **reconcile** pass detects
that `ROI.md`'s head is newer than the `<!-- Beehive-ROI: <sha> -->` stamp in
`PLAN.md`, folds the diff into the plan, re-weights tasks on the logarithmic priority
scale, and restamps. Hand-editing `PLAN.md` to "apply" the change races that pass and
is overwritten.

Then verify on the next reconcile that the new stamp matches `ROI.md`'s head and the
intended tasks appear with the expected priorities.

## Rules

- **Never `git commit` an ROI edit on the primary `main` working tree directly** —
  and never edit the live `submodules/<name>/ROI.md` in place. Use Path A or Path B
  above. A stray commit on local `main` wedges the repo (see the section above).
- When publishing via Path B, push to the hive's actual publish ref (the real remote
  from `git remote -v` — e.g. `gitea/main` — when one exists), not a local-only `.`
  unless the hive truly has no remote. Confirm divergence is `0 0` afterward.
- Never edit an `ROI.md` **as a honeybee** (an autonomous pass — the hook blocks it).
  Operator-directed edits are allowed: a hive agent acting on operator instruction,
  or the beehived editor.
- Never hand-edit `PLAN.md` — reconcile owns it.
