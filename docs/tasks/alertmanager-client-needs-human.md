# alertmanager-client-needs-human

Optional `alertmanager_url` (config.yaml, layered) enables `internal/alertmanager`,
started per served repo by `cmd/beehived` (`-external-url` sets generatorURL base).
Unset: one `alertmanager: disabled` log line.

- Source: `NeedsHumanSource` reads `git show HEAD:submodules/<sm>/PLAN.md` (committed
  state only) and yields one `BeehiveNeedsHuman` per NEEDS-HUMAN task.
- Labels (dedup identity): alertname, service=beehive, severity (critical for
  secret/external-permission, else warning), instance (registry repo name),
  submodule, task, category. Annotations: summary, escalation_summary, description.
- Notifier.Tick: diff desired vs sent; send on change or every Refresh (1m) with
  endsAt=now+4m; tasks leaving NEEDS-HUMAN sent with endsAt=now, retained until
  delivered. Failures: per-request timeout 10s, exponential backoff 5s..5m.
- Restart: no persisted state; first tick re-fires all committed NEEDS-HUMAN
  (same fingerprint). Tasks resolved while down expire via last endsAt.
- Runs only in beehived goroutines; honeybee passes never touch it.
