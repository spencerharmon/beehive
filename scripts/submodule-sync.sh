#!/usr/bin/env sh
# beehive submodule branch-tracking sync.
# Nonstandard submodule use: track the tip of a configured branch, not a pinned commit.
# Pulls latest from remote tracked branch and auto-advances the beehive pointer (no review).
# Usage: submodule-sync.sh <submodule>
set -eu

sm="${1:?usage: submodule-sync.sh <submodule>}"
repo="submodules/$sm/repo"
[ -d "$repo" ] || { echo "no repo at $repo" >&2; exit 1; }

# tracked branch from .gitmodules (submodule.<path>.branch); default main.
branch="$(git config -f .gitmodules "submodule.$repo.branch" 2>/dev/null || echo main)"

git -C "$repo" fetch origin "$branch" --prune
git -C "$repo" checkout "$branch"
# auto-clobber: tracked branch may be force-pushed/rebased; always take remote tip. Honeybees adapt the
# plan dynamically to whatever upstream becomes.
git -C "$repo" reset --hard "origin/$branch"

# advance beehive pointer iff it moved; auto-commit, no review.
if ! git diff --quiet -- "$repo"; then
  git add "$repo"
  git commit -m "submodule sync: $sm -> $branch tip

Beehive: submodule-sync $sm"
fi
echo "$repo on $branch at $(git -C "$repo" rev-parse --short HEAD)"
