#!/usr/bin/env bash
# Live production-binary acceptance. Endpoint is infrastructure configuration,
# never a source default. The Python driver uses only isolated repository fixtures.
set -euo pipefail
fail() { echo "alertmanager-live: FAIL: $*" >&2; exit 1; }
[ -n "${ALERTMANAGER_URL:-}" ] || fail "ALERTMANAGER_URL is not set (point it at an Alertmanager v2 endpoint)"
for bin in go git curl python3; do
	command -v "$bin" >/dev/null 2>&1 || fail "required prerequisite '$bin' not found on PATH"
done
curl -fsS --max-time 10 "${ALERTMANAGER_URL%/}/api/v2/status" >/dev/null || fail "Alertmanager at $ALERTMANAGER_URL is unreachable (GET /api/v2/status failed)"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/beehive-am-live.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
printf 'alertmanager-live: building production binaries from %s\n' "$SRC"
(cd "$SRC" && CGO_ENABLED=0 go build -o "$WORK/bin/" ./cmd/beehive ./cmd/honeybee ./cmd/beehived)
python3 "$SRC/tests/alertmanager_live.py" "$WORK"
