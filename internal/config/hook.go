package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// preCommitHook is the beehive pre-commit guard. It enforces two repo invariants:
//
//  1. ROI.md is human-owned: a honeybee identity (BEEHIVE_HONEYBEE=1) may never
//     change it. The frontend (env unset) is allowed. A server pre-receive
//     mirrors this for pushes.
//  2. PLAN.md dependency tags stay acyclic: when a commit touches any PLAN.md it
//     runs `beehive lint`, which loads the combined cross-submodule graph and
//     exits non-zero on a wait cycle. This rejects a honeybee (or human) dep-tag
//     commit that would deadlock the swarm, mirroring links.AddDep's CLI-time
//     cycle check for plan writes that never go through the CLI. The check is
//     best-effort: if the beehive binary is not on PATH it is skipped (the lint
//     still runs in CI and on the next CLI invocation).
const preCommitHook = `#!/usr/bin/env sh
# beehive pre-commit guard (installed by CLI). See internal/config/hook.go.
if [ "${BEEHIVE_HONEYBEE:-0}" = "1" ]; then
  if git diff --cached --name-only | grep -E '(^|/)ROI\.md$' >/dev/null; then
    echo "beehive: honeybee identity may not modify ROI.md" >&2
    exit 1
  fi
fi
if git diff --cached --name-only | grep -E '(^|/)PLAN\.md$' >/dev/null; then
  if command -v beehive >/dev/null 2>&1; then
    if ! beehive lint; then
      echo "beehive: PLAN.md dependency tags form a wait cycle (rejected)" >&2
      exit 1
    fi
  fi
fi
exit 0
`

// postReceiveHook keeps submodule checkouts in sync after each updateInstead
// push and SKIPS orphan gitlinks so it never fatals on an unregistered gitlink.
//
// Honeybees publish bookkeeping (PLAN flips, beehive-pointer bumps) by pushing to
// this repo's checked-out branch via receive.denyCurrentBranch=updateInstead.
// updateInstead refreshes tracked files but NOT submodule contents, so a pointer
// bump leaves the submodule checkout stale and the working tree dirty, which makes
// the NEXT push get refused and wedges the loop. This hook re-syncs the submodules
// so updateInstead pushes are self-healing.
//
// It iterates ONLY the paths declared in .gitmodules: the hive also carries orphan
// gitlinks (committed honeybee worktree checkouts under submodules/*/worktrees/)
// with no .gitmodules URL, and a blind `git submodule update --init --force`
// FATALS ("No url found for submodule path ... in .gitmodules") on them. Per-path
// iteration skips them; the producer-side fix is stop-worktree-gitlink-leak. It
// never fails the push (all errors swallowed: a sync hiccup must not wedge bees).
//
// The backtick in the live hand-installed copy is rendered as a single quote here
// so this stays one Go raw-string literal; the behavior is byte-for-byte identical.
const postReceiveHook = `#!/bin/sh
# post-receive: keep submodule checkouts in sync with the just-pushed gitlinks.
#
# Honeybees push bookkeeping (PLAN flips, beehive-pointer bumps) to this repo's
# checked-out branch via receive.denyCurrentBranch=updateInstead. updateInstead
# refreshes tracked files but NOT submodule contents, so a pointer bump leaves the
# submodule checkout stale and the working tree dirty -- which makes the NEXT push
# get refused, wedging the loop. Re-sync submodules here so updateInstead pushes
# are self-healing.
#
# This matters only for the SHARED persistent-checkout case (several honeybees
# reusing one working tree); ephemeral per-pass checkouts never hit it.

# Hooks run with GIT_DIR set and cwd at the git dir. Resolve the working tree and
# drop the receive-pack env so git operates on the real worktree, not the
# quarantine/index used while receiving the push.
git_dir=$(git rev-parse --absolute-git-dir 2>/dev/null) || exit 0
work_tree=$(CDPATH= cd -- "$git_dir/.." 2>/dev/null && pwd) || exit 0
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_QUARANTINE_PATH GIT_PUSH_OPTION_COUNT
cd "$work_tree" || exit 0

# Force the submodule checkouts to the gitlinks recorded by the just-applied
# commit. --force because updateInstead leaves them stale; we want them to match
# the tree exactly so the working tree is clean for the next push. Iterate only
# the real submodules declared in .gitmodules: the hive also carries orphan
# gitlinks (committed honeybee worktree checkouts under submodules/*/worktrees/)
# that have no .gitmodules URL, and a blind 'git submodule update' fatals on them.
# Never fail the push over this.
if [ -f .gitmodules ]; then
  git config -f .gitmodules --get-regexp '\.path$' 2>/dev/null | while read -r _ path; do
    [ -n "$path" ] && git submodule update --init --force -- "$path" >/dev/null 2>&1
  done
fi
exit 0
`

// InstallHooks lays down ALL beehive git hooks into repoRoot/.git/hooks at mode
// 0755 with canonical content: the ROI-protect/dep-cycle pre-commit guard and the
// submodule-sync post-receive hook. .git/hooks is never tracked by git, so a fresh
// clone or a second host is otherwise silently unprotected / out of sync; this is
// the reproducible install path (`beehive init` and `beehive hook install`).
//
// Idempotent: each hook is rewritten to its canonical content and chmod'd to 0755,
// so a re-run is byte-identical (no duplication) and UPGRADES a stale or hand-edited
// hook in place. Errors if repoRoot is not a git repo (no .git).
func InstallHooks(repoRoot string) error {
	if err := InstallROIHook(repoRoot); err != nil {
		return err
	}
	return InstallPostReceiveHook(repoRoot)
}

// InstallROIHook writes the beehive pre-commit guard into the repo's .git dir.
// (Named for its original ROI-only role; it now also installs the dep-cycle
// guard. See preCommitHook.)
func InstallROIHook(repoRoot string) error {
	return writeHook(repoRoot, "pre-commit", preCommitHook)
}

// InstallPostReceiveHook writes the submodule-sync post-receive hook (see
// postReceiveHook). Separate from InstallHooks so a caller may install just this
// one; InstallHooks is the normal entry point.
func InstallPostReceiveHook(repoRoot string) error {
	return writeHook(repoRoot, "post-receive", postReceiveHook)
}

// writeHook writes one hook file at repoRoot/.git/hooks/<name> with content, at
// mode 0755, creating the hooks dir. It errors if repoRoot is not a git repo.
//
// The write truncates+rewrites, so re-installing is idempotent (re-run produces a
// byte-identical file). os.WriteFile does NOT change the mode of an EXISTING file,
// so we chmod explicitly afterward to guarantee 0755 even when upgrading a stale
// hook that was left non-executable or world-writable.
func writeHook(repoRoot, name, content string) error {
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", repoRoot)
	}
	dir := filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		return err
	}
	return os.Chmod(p, 0o755)
}
