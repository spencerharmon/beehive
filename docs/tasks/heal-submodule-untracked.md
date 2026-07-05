# heal: git-clean untracked content in submodule checkouts

## Problem

The dirty-tree heal (`Repo.healLocalMain` in `internal/git/git.go`, the reset
fallback shared by `PublishToMain` / `UpdateLocalMain` and the honeybee startup
preflight via `EnsureCleanCheckout`) restores the live `main` checkout to a clean
projection of committed HEAD. It did so by:

1. `git reset --hard HEAD` in the main worktree (tracked-file drift), then
2. `git submodule sync` + `git submodule update --init --force` per declared
   submodule (re-materialize each `submodules/<name>/repo` at its recorded gitlink).

Both steps only touch **tracked** content. `submodule update --force` runs a
force checkout inside the submodule, which resets tracked files but **leaves
untracked files in place**. So any stray untracked file inside a submodule
checkout survived the heal — most commonly an operator's Emacs auto-save `#file#`
and dangling `.#file` lock symlinks left by editing the live checkout (the exact
thing agents are told never to do). Git then reports the superproject gitlink as
`modified (untracked content)`, the heal can never reach a clean HEAD projection,
and **every** honeybee pass aborts in preflight:

```
preflight: ... dirty and cannot be reset ...
git: main worktree still dirty after heal: M submodules/<name>/repo
```

One stray file wedged the whole swarm until an operator cleaned it by hand.

## Fix

In `healLocalMain`, after force-updating each declared submodule, run
`git clean -ffd` **inside that submodule checkout**, iterating the same
declared-submodule set the reset already walks (`declaredSubmodulePaths`, sourced
from `.gitmodules`). This removes the untracked cruft so the gitlink returns to a
clean projection and the tree passes the final `git status --porcelain` check.

Flag choice:

- `-f` — force (git refuses to clean without it);
- second `-f` (`-ff`) — also remove an untracked *nested git directory* (a stray
  clone), which a single `-f` would merely skip; a tracked/registered nested
  submodule is index content and is never touched by `git clean`;
- `-d` — recurse into untracked directories.
- **No `-x`.** Ignored files do **not** dirty the gitlink (verified: only
  non-ignored untracked content makes the superproject show `M sub`), so removing
  them buys nothing toward un-wedging and would risk nuking legitimate ignored
  build artifacts. Leaving them also keeps this step consistent with the
  tracked reset above, which likewise never removes ignored files.

Scope: the clean runs only on declared `submodules/<name>/repo` paths — never the
operator worktrees under `submodules/<name>/worktrees/` (those are not in
`.gitmodules`), and never the main worktree's own top-level untracked files (a
top-level `??` is genuine anomalous drift the heal still surfaces, unchanged).

## Non-silent

The clean never swallows a failure. A path `git clean` cannot fix (e.g. a
permission-denied untracked file) is recorded in the `failed` set and surfaced by
the existing dirty-tree check, which now reads
`... still dirty after heal (submodule resync/clean failed for <paths>)`.
Independently, any leftover keeps `git status --porcelain` non-empty, so the heal
returns an error regardless of `git clean`'s own exit code — the preflight aborts
loudly and for free instead of the swarm silently spinning on the wedge.

## Tests (`internal/git/git_test.go`)

- `TestHealCleansSubmoduleUntrackedContent` — seeds a real declared submodule with
  the field cruft (`#scratch.md#`, a dangling `.#scratch.md` symlink, a stray
  untracked dir), asserts the superproject is dirty, runs the heal, and asserts the
  tree is clean, the cruft is gone, and the submodule's tracked content survives.
- `TestHealSurfacesUncleanableSubmodule` — an untracked file inside a read-only
  directory (git-clean cannot unlink it) makes the heal return an error naming
  "still dirty after heal", proving the non-silent path (skipped as root, which
  bypasses directory permissions).
- Existing `TestEnsureCleanCheckout` / `TestPublishToMainHealsDirtyLocalTree` still
  pass: tracked resets, already-clean no-ops, and a top-level untracked file that
  still (correctly) surfaces as un-resettable drift are all unchanged.
