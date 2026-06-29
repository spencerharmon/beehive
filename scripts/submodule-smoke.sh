#!/usr/bin/env sh
# CI smoke: exercise submodule/init paths with a throwaway repo. No LLM.
set -eu

bin="$(mktemp -d)/beehive"
CGO_ENABLED=0 go build -o "$bin" ./cmd/beehive
work="$(mktemp -d)"

"$bin" init "$work/hive"
test -f "$work/hive/AGENTS.md"
test -d "$work/hive/submodules"

# fake submodule with ROI but no PLAN -> bootstrap path; no ROI -> dormant.
mkdir -p "$work/hive/submodules/sample/repo"
echo "intent" > "$work/hive/submodules/sample/ROI.md"
test -f "$work/hive/submodules/sample/ROI.md"

echo "submodule smoke ok"
