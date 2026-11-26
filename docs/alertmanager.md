# Alertmanager configuration and activation

`beehived` owns one asynchronous emitter per registered hive. The registry name
is the `instance` label. It computes all three alert families using `HiveSource`:
committed NEEDS-HUMAN tasks plus repo-local swarm/pass health evidence. The daemon
only POSTs; Alertmanager is never a source of runtime state or on a pass path.

## Publish configuration, then confirm it loaded

Keep `alertmanager_url` on the infrastructure side, e.g. the owning hive's tracked
root `config.yaml` (not this engine's defaults):

```yaml
alertmanager_url: https://alertmanager.example.com
```

Edit in a private **hive** worktree, commit, and publish using the hive's normal
convergence protocol. Never patch the shared primary checkout. No new config
writer or remote-sharing mode is necessary. `beehive edit`'s coordination-file
allowlist does not include `config.yaml`; do not use it for this file.

The daemon's existing 15-second emitter loop also resolves configuration, even
while disabled. It layers built-in defaults, host config, and **committed** global
hive config, pins the commit before reading the blob, and ignores dirty/staged
YAML. Per-submodule agent config does not select a hive-wide receiver. The startup
log and each effective URL transition name the instance, receiver, and config
revision. Invalid YAML/URL or a read failure logs an error and retains the last
good setting; a later repair retries without restarting the frontend.

A config-only commit loads without a signal/restart **once this implementation is
deployed**. Older startup-only binaries still need the site's allowed normal
build/deploy operation. Do not run an unreviewed bee binary as a second production
daemon, introduce a timer, or restart live passes to load configuration. On a
self-hosted hive that deploys only reviewed `origin/main`, retain a
`Verify-After-Merge` live check until review, merge, and deployment occur.

Unchanged settings retain the same notifier and its startsAt/refresh/backoff.
Endpoint changes swap only its transport, serially, and immediately reconcile to
the new receiver. Removing the global key falls back to the host layer under the
existing zero-is-unset configuration semantics; with no host value it disables
delivery. Disabling/changing receivers does **not** assert that real conditions
resolved: previously sent alerts expire at their explicit endsAt (up to four
minutes). Re-enabling retains startsAt. There are never two sending loops for a
single hive in one daemon. Delivery errors keep the existing bounded retry logic.

## Read-only deployment evidence

1. Record the live daemon's version/revision and its activation journal, including
   `enabled for <instance> ... (config revision <sha>)`. A new binary on disk is
   not proof that the live process loaded it.
2. Print the actual desired labels without host config reads, network access, or
   changes to the notifier's sent set:
   `beehive alertmanager desired --root <hive> --instance <registry-name>`.
   Output is `{revision, labels}`; compare with GET labels under that exact
   instance. Health evidence is sampled at command time, not synthesized. If
   conditions change during measurement, re-sample; do not clear real alerts.
3. Observe two GET snapshots, 80 seconds apart (one normal 60s refresh plus a
   15s poll margin), with the committed read-only probe. For a bee checkout use
   `beehive submodule worktree exec <sm> <branch> -- ...` to run the command in
   that checkout. This example assumes `$AM` and `$INSTANCE` are supplied by the
   infrastructure configuration:

   ```sh
   curl --fail-early --rate 45/h -fsS --max-time 15 \
     "$AM/api/v2/alerts?active=true&filter=instance%3D%22$INSTANCE%22" \
     --next -fsS --max-time 15 \
     "$AM/api/v2/alerts?active=true&filter=instance%3D%22$INSTANCE%22" |
     jq -e -s --arg instance "$INSTANCE" -f tests/alertmanager-production-refresh.jq
   ```

   Use a URL-encoded instance if it contains reserved characters. The probe
   rejects empty/duplicate/wrong-instance/inactive/expired alerts, changed
   startsAt/identities, and non-advancing endsAt. Success prints the production
   labels, stable startsAt, both expiries and measured advance seconds. Snapshot
   failure is not permission to inject conditions or POST resolutions.
4. API visibility is not notification/phone delivery evidence. Receiver routing
   and phone confirmation remain the infrastructure owner's separate check.

Regression tests (Go/Git/jq required):
`go test ./internal/alertmanager ./internal/config ./cmd/beehive ./cmd/beehived`.
`TestDaemonReloadsCommittedAlertmanagerConfig` builds the real daemon and proves a
post-startup commit activates its emitter against a test-owned HTTP receiver.
The existing isolated live acceptance harness remains separate and unchanged.
