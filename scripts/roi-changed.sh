#!/usr/bin/env sh
# Deterministic ROI->PLAN drift check. Priority-0 trigger.
# Compares PLAN.md's recorded ROI stamp (last reconciled ROI.md commit) to ROI.md's current commit.
# Exit 0 + print diff range if reconcile needed; exit 1 if up to date.
# Usage: roi-changed.sh <submodule>
set -eu

sm="${1:?usage: roi-changed.sh <submodule>}"
roi="submodules/$sm/ROI.md"
plan="submodules/$sm/PLAN.md"
[ -f "$roi" ] || { echo "no ROI" >&2; exit 1; }

cur="$(git log -1 --format=%H -- "$roi")"
stamp="$(grep -oE 'Beehive-ROI: [0-9a-f]+' "$plan" 2>/dev/null | awk '{print $2}' || true)"

if [ "$stamp" = "$cur" ]; then exit 1; fi          # up to date, no reconcile
echo "${stamp:-ROOT}..$cur $roi"                    # range for: git diff <range>
exit 0
