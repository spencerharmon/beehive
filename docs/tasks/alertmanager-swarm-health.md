# alertmanager-swarm-health

Adds `BeehiveSwarmWedged` and `BeehivePassFailing` to the outbound-only
Alertmanager emitter, sharing the `BeehiveNeedsHuman` notifier (one
`Notifier`, one client, same refresh/backoff/resolution lifecycle).

## Evidence (repo-local, cross-process)
Honeybee processes record outcomes into `<git-common-dir>/beehive/health.json`
(flock-serialized, atomic write; shared by all worktrees; never committed).
beehived reads it as a `Source`. No journal / agent DB / Alertmanager reads.
Recording does no network I/O, so notification can never change a pass.
A persistence failure prints `honeybee: WARNING could not persist …` and the
original outcome/error is returned unchanged. A corrupt file is reset and the
reset surfaced.

Recorded at: preflight dirty-tree heal failure, preflight ff-only pull/fetch
failure, base-resolve failure, preflight success (`cmd/honeybee`, before any
session exists, attributed to `honeybee-pid-<n>`); every publish attempt
(`wrapPublish`); every finished `Runner.Run` (`passOutcome`).

## Thresholds / reset
- SwarmWedged (critical; labels alertname, service, severity, instance): 3
  consecutive preflight failures OR 3 consecutive non-conflict publish
  failures. Preflight success resets preflight; publish success resets both.
  `git.ErrConflict` (peer race) neither counts nor resets.
- PassFailing (warning; + submodule): 3 consecutive passes for one submodule
  that errored, timed out/capped, or ended without completion. A completed pass
  resets. Lost claim / no workable task are neutral.
Counts, stage, last error, session, since go in annotations only.

## Resolution across restarts
`Notifier.StatePath` persists the delivered firing set
(`<git-common-dir>/beehive/alertmanager-sent-<instance>.json`), so a restarted
beehived or one-shot `beehive alertmanager emit` explicitly sends endsAt=now
for a condition that cleared while it was not running.

## Live acceptance
`ALERTMANAGER_URL=… tests/alertmanager-live.test.sh` builds the real binaries,
configures `alertmanager_url` via a private `BEEHIVE_CONFIG_DIR`, drives three
real honeybee preflight failures on an isolated fixture hive, emits with the
production emitter, verifies exact identities via GET, refresh/dedup,
recovery + explicit resolution, and cleans up only its unique instance's
alerts. Fails (never skips) when the endpoint/prereqs are absent. Its failure
paths and a happy path against an in-process fake are unit-tested in
`internal/alertmanager/live_harness_test.go`.
