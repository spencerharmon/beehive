#!/usr/bin/env sh
# Thin wrapper: beehive repo scaffold is owned by `beehive init` (writes the full
# embedded AGENTS.md protocol). This avoids drift between shell and binary.
# Usage: init.sh <path>
set -eu
exec beehive init "${1:?usage: init.sh <path>}"
