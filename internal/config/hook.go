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

// preReceiveHook is the SERVER-SIDE mirror of the pre-commit ROI guard. The
// pre-commit hook is client-side and bypassable (`--no-verify`, or a direct
// push from a checkout that never ran it); the real enforcement point for
// "ROI.md is human-owned" is a pre-receive hook on the repo that accepts the
// push. It rejects any pushed ref update whose commits touch a ROI.md path when
// the pushing identity is a honeybee; the frontend / human is allowed.
//
// Honeybee identity OVER THE WIRE. The task caveat is explicit that the hook must
// NOT rely on client-only env (a push env does not traverse a remote push) and
// names a "push option" as an acceptable mechanism, so identity is recognized
// from EITHER of two signals (either one marks the push a honeybee's):
//
//  1. Push option `beehive-honeybee=1` (`git push -o beehive-honeybee=1`): the
//     transport-independent signal, delivered to the hook as GIT_PUSH_OPTION_COUNT
//     / GIT_PUSH_OPTION_<i> (requires receive.advertisePushOptions=true on the
//     receiver). This is the mechanism a real ssh/https honeybee push carries and
//     the one that does NOT depend on client-only env — it is what satisfies the
//     design caveat.
//  2. BEEHIVE_HONEYBEE=1 in the environment: inherited by the hook ONLY on a
//     LOCAL push (`git push .` / a filesystem-path remote, same process tree),
//     which is exactly the hive's updateInstead main-convergence path
//     (git.PublishToMain / cmd_basic init) where honeybees publish today. Kept
//     symmetric with the pre-commit signal so the single-host hive is guarded now
//     with no other change; a remote push relies on signal (1). Empirically the
//     env does reach receive-pack + this hook on a local push.
//
// Both signals are COOPERATIVE — a honeybee's own git carries them, exactly as
// the pre-commit hook trusts the runner's env; they identify the swarm's own
// pushes and are not an adversarial boundary. Hardening against a HOSTILE pusher
// is a dedicated push user/key whose identity the client cannot forge, at which
// point the check moves to that identity; that is out of scope for the local
// convergence model and noted as the extension point. The producer that SETS
// either signal on a honeybee's git operations is a follow-up (see the change
// doc): like the pre-commit guard, this one stays dormant until the runner
// exports the identity.
//
// Detection is per-commit across the pushed range (git log --name-only), so a
// touch-then-revert within one push is still caught, not merely the net old..new
// diff; --root catches an initial commit that adds ROI.md; the path regex matches
// ROI.md at the repo root or under any submodule directory. New-ref pushes
// (old all-zeros) inspect only the not-yet-present commits (`<new> --not --all`);
// ref deletions (new all-zeros) are nothing to inspect and pass.
const preReceiveHook = `#!/bin/sh
# beehive pre-receive guard (installed by CLI). See internal/config/hook.go.
# Server-side mirror of the pre-commit ROI rule: ROI.md is human-owned, so a
# honeybee identity may never push a change that touches a ROI.md path. The
# frontend / human is allowed. Honeybee identity is recognized from EITHER signal,
# so the guard does NOT rely on client-only env (per the task caveat):
#   (1) push option beehive-honeybee=1 (git push -o beehive-honeybee=1), delivered
#       as GIT_PUSH_OPTION_COUNT / GIT_PUSH_OPTION_<i> -- transport-independent
#       (needs receive.advertisePushOptions=true on the receiver);
#   (2) BEEHIVE_HONEYBEE=1 in the env -- inherited only on a LOCAL push
#       (git push . / a path remote, same process tree), the hive's own
#       updateInstead main-convergence path, symmetric with the pre-commit hook.
honeybee=0
[ "${BEEHIVE_HONEYBEE:-0}" = "1" ] && honeybee=1
i=0
n=${GIT_PUSH_OPTION_COUNT:-0}
while [ "$i" -lt "$n" ]; do
  eval "opt=\${GIT_PUSH_OPTION_$i}"
  [ "$opt" = "beehive-honeybee=1" ] && honeybee=1
  i=$((i + 1))
done
# Frontend / human identity is never restricted.
[ "$honeybee" = "1" ] || exit 0
# All-zeros OID = ref create/delete sentinel; length-agnostic (sha1 or sha256).
is_zero() { case "$1" in "" | *[!0]*) return 1 ;; *) return 0 ;; esac; }
rc=0
while read -r old new ref; do
  is_zero "$new" && continue          # ref deletion: nothing to inspect
  if is_zero "$old"; then
    set -- "$new" --not --all         # new ref: only its not-yet-present commits
  else
    set -- "$old..$new"
  fi
  # Reject if ANY commit in the pushed range touches a ROI.md path. Per-commit
  # (--name-only) so a touch-then-revert within the range is still caught, not
  # just the net old..new diff; --root so an initial commit that ADDS ROI.md is
  # seen. Regex matches ROI.md at the root or under any submodule directory.
  touched=$(git log --name-only --pretty=format: --root "$@" 2>/dev/null | grep -E '(^|/)ROI\.md$' | sort -u)
  if [ -n "$touched" ]; then
    echo "beehive: honeybee identity may not push changes to ROI.md (ref $ref)" >&2
    echo "$touched" | sed 's/^/  /' >&2
    rc=1
  fi
done
exit $rc
`

// InstallHooks lays down ALL beehive git hooks into repoRoot/.git/hooks at mode
// 0755 with canonical content: the ROI-protect/dep-cycle pre-commit guard, the
// submodule-sync post-receive hook, and the server-side ROI-protect pre-receive
// hook. .git/hooks is never tracked by git, so a fresh clone or a second host is
// otherwise silently unprotected / out of sync; this is the reproducible install
// path (`beehive init` and `beehive hook install`).
//
// Idempotent: each hook is rewritten to its canonical content and chmod'd to 0755,
// so a re-run is byte-identical (no duplication) and UPGRADES a stale or hand-edited
// hook in place. Errors if repoRoot is not a git repo (no .git).
func InstallHooks(repoRoot string) error {
	if err := InstallROIHook(repoRoot); err != nil {
		return err
	}
	if err := InstallPreReceiveHook(repoRoot); err != nil {
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

// InstallPreReceiveHook writes the server-side ROI-protect pre-receive hook (see
// preReceiveHook), symmetric to InstallROIHook. Separate from InstallHooks so a
// caller may install just this one; InstallHooks is the normal entry point.
func InstallPreReceiveHook(repoRoot string) error {
	return writeHook(repoRoot, "pre-receive", preReceiveHook)
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
