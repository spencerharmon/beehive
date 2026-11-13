#!/usr/bin/env bash
# Live acceptance harness for beehive's Alertmanager emitter.
#
#   ALERTMANAGER_URL=https://alertmanager.example.com tests/alertmanager-live.test.sh
#
# Builds the real `beehive` and `honeybee` binaries from this checkout, configures
# them (BEEHIVE_CONFIG_DIR -> a private config.yaml whose alertmanager_url is
# $ALERTMANAGER_URL) and drives the PRODUCTION emitter (`beehive alertmanager
# emit`, the same NewHiveNotifier beehived runs) over an ISOLATED fixture hive:
#   1. a committed NEEDS-HUMAN task           -> BeehiveNeedsHuman
#   2. three real honeybee preflight failures  -> BeehiveSwarmWedged
#      (separate processes; ff-only pull of an unreachable remote)
#   3. re-emit: same identities, exactly one alert each, startsAt unchanged
#   4. recovery (remote fixed + honeybee preflight ok; task leaves NEEDS-HUMAN)
#      -> explicit resolution: no active alert left for the test instance.
# Verification uses GET /api/v2/alerts (tests only; production never reads).
# The test instance label is unique and bounded; cleanup resolves ONLY alerts
# carrying that instance label. The operator's real hive is never touched.
# Missing endpoint/config/prerequisites FAIL (exit non-zero); nothing is skipped.
set -euo pipefail

fail() { echo "alertmanager-live: FAIL: $*" >&2; exit 1; }
log() { echo "alertmanager-live: $*" >&2; }

[ -n "${ALERTMANAGER_URL:-}" ] || fail "ALERTMANAGER_URL is not set (point it at an Alertmanager v2 endpoint)"
AM="${ALERTMANAGER_URL%/}"
for bin in go git curl jq; do
	command -v "$bin" >/dev/null 2>&1 || fail "required prerequisite '$bin' not found on PATH"
done
curl -fsS --max-time 10 "$AM/api/v2/status" >/dev/null || fail "Alertmanager at $AM is unreachable (GET /api/v2/status failed)"

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/beehive-am-live.XXXXXX")"
INSTANCE="beehive-live-test-$(date +%s)-$$"
FILTER="$(jq -rn --arg i "$INSTANCE" '"instance=\"" + $i + "\"" | @uri')"

active_alerts() {
	curl -fsS --max-time 10 "$AM/api/v2/alerts?active=true&silenced=true&inhibited=true&filter=$FILTER"
}

cleanup() {
	local rc=$?
	# Resolve ONLY this run's alerts (exact instance label), then drop the fixture.
	local cur
	if cur="$(active_alerts 2>/dev/null)" && [ "$(jq length <<<"$cur")" -gt 0 ]; then
		jq --arg i "$INSTANCE" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
			'[.[] | select(.labels.instance == $i) | {labels, startsAt, endsAt: $now}]' <<<"$cur" |
			curl -fsS --max-time 10 -H 'Content-Type: application/json' --data-binary @- "$AM/api/v2/alerts" >/dev/null ||
			log "WARNING cleanup could not resolve test alerts for $INSTANCE"
	fi
	rm -rf "$WORK"
	exit $rc
}
trap cleanup EXIT

log "building binaries from $SRC"
(cd "$SRC" && CGO_ENABLED=0 go build -o "$WORK/bin/" ./cmd/beehive ./cmd/honeybee) || fail "build failed"
BEEHIVE="$WORK/bin/beehive"
HONEYBEE="$WORK/bin/honeybee"

export BEEHIVE_CONFIG_DIR="$WORK/config"
mkdir -p "$BEEHIVE_CONFIG_DIR"
printf 'alertmanager_url: %s\nabort_on_remote_failure: true\n' "$AM" >"$BEEHIVE_CONFIG_DIR/config.yaml"
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com

HIVE="$WORK/hive"
mkdir -p "$HIVE/submodules/fixture"
git -C "$HIVE" init -q -b main
echo "# fixture hive" >"$HIVE/AGENTS.md"
cat >"$HIVE/submodules/fixture/PLAN.md" <<'PLAN'
# Plan
<!-- Beehive-ROI: abc -->

## live-t1 [NEEDS-HUMAN] <!-- attempts=1 deps= weight=1 category=secret -->
Live harness fixture task.
Human-needed: populate key LIVE_TEST in the secret store
PLAN
git -C "$HIVE" add -A && git -C "$HIVE" commit -qm fixture
git -C "$HIVE" remote add origin "$WORK/does-not-exist.git"

emit() { "$BEEHIVE" alertmanager emit --root "$HIVE" --instance "$INSTANCE" >/dev/null || fail "emit failed"; }
names() { active_alerts | jq -r '[.[].labels.alertname] | sort | join(",")'; }

log "three honeybee preflight failures (unreachable remote) in separate processes"
for i in 1 2 3; do
	if "$HONEYBEE" "$HIVE" >"$WORK/hb$i.log" 2>&1; then fail "honeybee preflight $i unexpectedly succeeded"; fi
	grep -q 'preflight' "$WORK/hb$i.log" || fail "honeybee run $i did not fail in preflight: $(cat "$WORK/hb$i.log")"
done

emit
got="$(names)"
[ "$got" = "BeehiveNeedsHuman,BeehiveSwarmWedged" ] || fail "want BeehiveNeedsHuman,BeehiveSwarmWedged for $INSTANCE, got '$got'"
first="$(active_alerts)"
jq -e --arg i "$INSTANCE" 'all(.[]; .labels.service == "beehive" and .labels.instance == $i)' <<<"$first" >/dev/null || fail "unexpected label identity: $first"
jq -e '[.[] | select(.labels.alertname=="BeehiveNeedsHuman")][0].labels | .submodule=="fixture" and .task=="live-t1" and .category=="secret" and .severity=="critical"' <<<"$first" >/dev/null || fail "NeedsHuman identity wrong: $first"
jq -e '[.[] | select(.labels.alertname=="BeehiveSwarmWedged")][0] | .labels.severity=="critical" and .annotations.stage=="preflight" and .annotations.consecutive=="3"' <<<"$first" >/dev/null || fail "SwarmWedged identity/evidence wrong: $first"

log "refresh/dedup"
sleep 1
emit
second="$(active_alerts)"
[ "$(jq length <<<"$second")" = 2 ] || fail "refresh forked identities: $second"
[ "$(jq -S '[.[]|{l:.labels,s:.startsAt}]|sort_by(.l.alertname)' <<<"$first")" = "$(jq -S '[.[]|{l:.labels,s:.startsAt}]|sort_by(.l.alertname)' <<<"$second")" ] || fail "refresh changed identity/startsAt"

log "recovery: remote fixed, preflight ok, task leaves NEEDS-HUMAN"
git -C "$HIVE" remote remove origin
"$HONEYBEE" "$HIVE" >"$WORK/hb-ok.log" 2>&1 || fail "recovered honeybee preflight failed: $(cat "$WORK/hb-ok.log")"
sed -i 's/\[NEEDS-HUMAN\] <!-- attempts=1 deps= weight=1 category=secret -->/[TODO] <!-- attempts=1 deps= weight=1 -->/; /^Human-needed:/d' "$HIVE/submodules/fixture/PLAN.md"
git -C "$HIVE" commit -qam resolved
emit
for _ in $(seq 1 10); do
	[ "$(active_alerts | jq length)" = 0 ] && { log "PASS ($INSTANCE)"; exit 0; }
	sleep 1
done
fail "alerts not explicitly resolved for $INSTANCE: $(active_alerts)"
