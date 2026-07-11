# opencode Setup

The honeybee swarm is provider-agnostic: no LLM SDK is linked. Each honeybee
drives an [opencode](https://opencode.ai) session via its server API. Model
choice is opencode config, not beehive code — cloud API, local model, or
cluster, all the same to beehive.

## Install opencode

Install per upstream docs; ensure `opencode` is on `PATH` (or set `agent_cmd`
in `/etc/beehive/config.yaml`).

## Run the opencode server

Beehive talks to a long-running `opencode serve` over HTTP at `agent_url`
(default `http://127.0.0.1:4096`). `scripts/install-systemd-user.sh` installs and
enables a `~/.config/systemd/user/opencode.service` for this by default, and
orders `beehived.service` + `beehive-honeybee.service` after it:

```sh
./scripts/install-systemd-user.sh --repo ~/beehive-infra --now
# tune: --opencode-cmd PATH --opencode-hostname HOST --opencode-port PORT
# skip the unit: --no-opencode
```

Provider credentials: run `opencode auth login` (writes `~/.config`), or drop
environment variables into the unit's optional `~/.config/beehive/opencode.env`
(e.g. `ANTHROPIC_API_KEY=...`). Manually:

```sh
opencode serve --hostname 127.0.0.1 --port 4096
```

## Ephemeral per-pass servers (`agent_ephemeral`)

A single shared `opencode serve` never releases per-session state: its heap and
its on-disk store (SQLite session DB + per-session git snapshots) grow
monotonically across the thousands of sessions a busy swarm opens over days. On
2026-07-10 one shared server reached ~40 GB RSS and triggered a host-wide OOM
that also killed co-tenant workloads. `agent_ephemeral` removes that failure
mode:

```yaml
# ~/.config/beehive/config.yaml (or /etc/beehive/config.yaml)
agent_ephemeral: true
```

When true, each honeybee pass spawns its OWN `opencode serve` instead of using
the shared server at `agent_url`:

- launched as `<agent_cmd> serve --hostname 127.0.0.1 --port 0 --print-logs`
  (the OS picks a free port; the runner parses it from the startup line);
- `XDG_DATA_HOME` is pointed at a fresh temp dir, so the server's DB, snapshots,
  and logs land there and are discarded wholesale at teardown. The install's
  `auth.json` (opencode keeps provider credentials in the DATA dir) is copied in
  so the isolated server authenticates to the same provider; the provider/model
  DEFINITIONS under `XDG_CONFIG_HOME` are left untouched so they still resolve;
- on pass exit the runner SIGINTs the server's process group (then SIGKILLs any
  survivor) and removes the temp dir. The OS reclaims all of that pass's agent
  heap and session store, so nothing accumulates across passes.

Only the honeybee PASS uses ephemeral servers. The frontend/editor (`beehived`)
keep using the shared `agent_url` server, since interactive editing wants a
persistent server. Concurrent passes each get their own short-lived server, so
total agent memory is bounded by pass concurrency × one fresh server, never one
unbounded shared one. Spawn failure fails the pass (it does not silently fall
back to a shared server the operator disabled on purpose).

With `agent_ephemeral: true` the `opencode.service` unit is only needed for the
frontend/editor; a pure-swarm host with no interactive editing can disable it.

## Configure the model

opencode config under `/etc/beehive` selects the provider/model. One session
spans a honeybee's lifetime so context persists across turns; pick a model whose
context window fits a rightsized task (tasks are sized to avoid compaction).

## Runner contract

- system prompt: beehive `HONEYBEE.md` (repo root; falls back to the binary default)
- first user prompt: `bootstrap.md` | `reconcile.md` | `select.md`
- cwd: the per-branch worktree
- between turns: `continue.md` into the same session
- caps: 15 turns + wall-clock → mark for GC

The runner decides exit, not the model. See `docs/honeybee.md` and
`prompts/README.md`.
