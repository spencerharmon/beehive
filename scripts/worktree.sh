#!/usr/bin/env sh
# beehive honeybee worktree management. All honeybee writes happen here, never repo/.
# Branches off the synced tracked-branch tip. Worktree deleted on DONE+merge.
# Usage: worktree.sh add <submodule> <branch> | rm <submodule> <branch>
set -eu

cmd="${1:?usage: worktree.sh add|rm <submodule> <branch>}"
sm="${2:?submodule}"
br="${3:?branch}"
repo="submodules/$sm/repo"
wt="submodules/$sm/worktrees/$br"

case "$cmd" in
  add)
    ./scripts/submodule-sync.sh "$sm"   # branch off fresh tip
    git -C "$repo" worktree add -b "$br" "../worktrees/$br" HEAD
    echo "$wt"
    ;;
  rm)
    git -C "$repo" worktree remove "../worktrees/$br" --force
    git -C "$repo" branch -D "$br" 2>/dev/null || true
    ;;
  *) echo "usage: worktree.sh add|rm <submodule> <branch>" >&2; exit 1 ;;
esac
