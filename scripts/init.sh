#!/usr/bin/env sh
# beehive init: scaffold a beehive repo. Deterministic, no LLM.
# Usage: init.sh <path>
set -eu
dst="${1:?usage: init.sh <path>}"
mkdir -p "$dst"; cd "$dst"
[ -d .git ] || git init -q
mkdir -p submodules
cat > AGENTS.md <<'EOF'
# Honeybee Protocol
(Authoritative honeybee instructions; see prompts/AGENTS.md.)
EOF
: > INFRASTRUCTURE.md
git add -A && git commit -qm "beehive init

Beehive: init scaffold"
echo "beehive repo at $dst"
