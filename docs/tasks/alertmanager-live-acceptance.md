# Alertmanager live acceptance harness

`ALERTMANAGER_URL=https://alertmanager.example.com tests/alertmanager-live.test.sh`
builds beehive, honeybee and beehived from the current checkout. Requires Go, Git,
curl and Python 3 (stdlib only). It takes about 100 seconds; no shortened refresh
or fake clock is used for live acceptance. Missing prerequisites/endpoint fail.

The shell entrypoint delegates to `tests/alertmanager_live.py`, replacing the
former two-family, two-one-shot test (which never observed background refresh).
All configuration, git remotes, processes and state are isolated in temporary
fixtures. The URL is written into each fixture's supported **in-repo** config
layer, with a private empty host config directory (no inherited host registry).
There are no deployment defaults or mutations of the operator's hive/daemon.

- Three real honeybee preflight failures produce persisted swarm-wedge evidence.
- A committed NEEDS-HUMAN task produces the task alert.
- A per-submodule pass-failure counter is a **repository health input fixture**,
  not an Alertmanager payload. This test does not execute three agent passes;
  `cmd/honeybee` health tests separately cover recorder/outcome wiring.
- A real beehived background notifier emits all three. GET verifies exact label
  and annotation maps plus generatorURL, exactly one alert per identity, original
  startsAt and explicit four-minute TTL. Then it observes endsAt advance at least
  55 seconds with unchanged identities, without invoking the one-shot CLI.
- Successful honeybee preflight plus cleared committed/health conditions must
  disappear within 35 seconds, **before original expiry**. The regression's fake
  v2 receiver independently records actual POSTs to assert explicit resolution
  and timed refresh for every family; cleanup cannot create refresh evidence.
- A separate local receiver returns 503. The real daemon logs retries while its
  frontend remains responsive and honeybee preflight succeeds. Unset config logs
  disabled; the diagnostic CLI fails on unset/delivery errors instead of lying.
- Even cleanup uses the production emitter after clearing fixture conditions;
  no handcrafted POST can substitute for emitter wiring. Only owned fixture
  daemons are terminated. Cleanup errors fail the test, not silently leak alerts.

Regression: `go test ./internal/alertmanager -run '^TestLiveHarness' -count=1 -v`.
The strengthened receiver assertions fail with the old shell driver (no
PassFailing; expiry extension about one second) and pass with this driver.

**Boundary:** a successful isolated harness proves the production-binary seam to
that endpoint. It does NOT enable the operator's real daemon, prove its effective
configuration, or prove downstream ntfy/phone delivery. Deployment acceptance must
also configure the actual hive's `alertmanager_url`, actively load it via the
supported deployment path, and verify that hive's stable instance at the
receiver. Current beehived reloads committed config every 15 seconds; older
startup-only binaries first need the site's allowed build/deploy operation
(see [configuration and activation](../alertmanager.md)). Do not poll an unset or
stale daemon expecting configuration to appear passively. Infrastructure-specific
URL/access/configuration and measured evidence belong in the owning hive docs.
