# Beehive

Beehive is git-native orchestration for autonomous coding agents. It keeps team intent, plans, sessions, and source checkouts in one repo so independent agents can work in isolated worktrees and converge through normal git commits and merges.

Beehive ships three Go binaries:

- `beehive` — deterministic CLI for repo setup, submodules, secrets, worktrees, instructions, linting, and audit. No LLM calls.
- `beehived` — web frontend daemon. Default listen address: `:8955`.
- `honeybee` — one-task agent runner used by `beehive honeybee start`.

## Requirements

- Go 1.22+ for source builds.
- Git.
- `gpg` for encrypted `SECRETS.yaml.gpg` support and automatic install-time key generation.
- Agent backend on PATH for honeybee runs. Default: `opencode`.

## Build from source

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
mkdir -p bin
for bin in beehive beehived honeybee; do
  CGO_ENABLED=0 go build -trimpath -o "bin/$bin" "./cmd/$bin"
done
./bin/beehive version
```

## Install directly from repo

System install to `/usr/local/bin` and `/etc/beehive`:

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
sudo ./scripts/install.sh
beehive version
```

User-local install without sudo:

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
PREFIX="$HOME/.local" \
BEEHIVE_CONFIG_DIR="$HOME/.config/beehive" \
./scripts/install.sh
export PATH="$HOME/.local/bin:$PATH"
beehive version
```

Installer behavior:

- Installs `beehive`, `beehived`, and `honeybee`.
- Prefers matching `dist/<binary>-<os>-<arch>` or `dist/<binary>` release artifacts when present.
- Builds from source when artifacts are absent.
- Creates config directory and `config.yaml` only if missing.
- Creates gpg keyring and generates key only if `gpg` exists and no secret key exists.
- Honors `PREFIX`, `BEEHIVE_CONFIG_DIR`, `DESTDIR`, `TMPDIR`, `CGO_ENABLED`, and `BEEHIVE_SKIP_KEYGEN=1`.

Staged install for packaging:

```sh
DESTDIR="$PWD/stage" PREFIX=/usr/local ./scripts/install.sh
```

## Run beehived

```sh
beehived -repo /path/to/beehive-repo
# open http://localhost:8955
```

Override listen address when needed:

```sh
beehived -addr 0.0.0.0:8955 -repo /path/to/beehive-repo
```

### Frontend performance & the supported-submodule ceiling

Every dashboard/plan/human request derives its view from files on disk (chiefly
each submodule's `PLAN.md`). To avoid re-reading and re-parsing every plan on
every request, `beehived` memoizes the parse **per repo `HEAD` commit**: the
first request at a given `HEAD` parses each `PLAN.md` once, and subsequent
requests reuse that parse until `HEAD` advances. Because honeybees publish only
by committing/pushing — every claim, heartbeat, status flip, and merge is a
commit — any change to a tracked file advances `HEAD` and drops the whole cache,
so the frontend never serves data from a stale commit. Invalidation is
deliberately coarse (whole-cache on any commit): correctness over hit-rate.

Only the time-*independent* work (the read + parse) is cached. Time-*dependent*
state — whether a task's claim is still fresh or has gone stale — is recomputed
against the wall clock on every request, so a crashed owner's claim still expires
on schedule even though no new commit advanced `HEAD`.

**Ceiling.** The cache holds one parsed plan per submodule for the current
`HEAD`, so live memory is `O(submodules)` parsed plans and each commit re-parses
the plans touched since. This is sized for human-scale hives — up to a few
hundred submodules with tens-of-KB `PLAN.md` files — where the parsed set fits
comfortably in memory and a full re-parse per commit is cheap next to the request
rate. Far beyond that (thousands of submodules, or a commit cadence so high that
nearly every request spans a fresh `HEAD`) the coarse invalidation degrades
toward the uncached cost. That is a hit-rate ceiling, not a correctness cliff:
views stay correct at any scale.

## Quick start

```sh
beehive init ~/beehive-infra
cd ~/beehive-infra
beehive submodule add git@github.com:org/project.git
# Author submodules/project/ROI.md in editor or frontend.
beehive honeybee start submodules/project
beehived -repo .
```

## Systemd user units

Install user units and a user-local gpg keyring/config:

```sh
./scripts/install-systemd-user.sh --repo ~/beehive-infra --now
```

The script writes `~/.config/systemd/user/opencode.service`, `beehived.service`, `beehive-honeybee.service`, and `beehive-honeybee.timer`; creates `~/.config/beehive/config.yaml`; and generates a real gpg key under `~/.config/beehive/gnupg` when no secret key exists. The opencode server unit (`opencode serve`, default `127.0.0.1:4096`) is installed by default; pass `--no-opencode` to skip it.

Example unit contents and manual install steps live in `docs/orchestration.md`. They run the frontend on port `8955` and launch honeybee passes as transient `run-*.service` units so long passes do not block the timer.

Core files:

- `ROI.md` — human-owned record of intent. Agents must not edit it.
- `PLAN.md` — honeybee-owned task plan derived from `ROI.md`.
- `sessions/` — honeybee transcripts.
- `docs/` — task change records.
- `repo/` — target source git submodule.
- `worktrees/` — isolated task worktrees.

## Common CLI commands

```sh
beehive init <path>                       # creates git repo on main if needed
beehive submodule add <repo>
beehive submodule link <a> <b>
beehive submodule plan rollback <plan-id>
beehive secret add|update|edit -f file.yaml
beehive honeybee start <path>
beehive task human <submodule> <task-id> --reason "specific blocker"
beehive worktree add|rm <submodule> <branch>
beehive audit [submodule]
beehive instruction list|update
```

## Configuration

Default config dir: `/etc/beehive`. Override with `BEEHIVE_CONFIG_DIR`.

Default `config.yaml`:

```yaml
gpg_home: /etc/beehive/gnupg
agent_cmd: opencode
ttl_minutes: 60
max_turns: 15
reject_limit: 3
```

## Documentation

- `docs/install.md` — install, packaging, config, release verification.
- `docs/cli.md` — CLI reference.
- `docs/repo-layout.md` — beehive repo layout.
- `docs/honeybee.md` — honeybee runner loop.
- `docs/orchestration.md` — scheduling passes with systemd.
- `docs/opencode.md` — agent backend setup.
- `docs/secrets.md` — gpg secrets workflow.
- `docs/frontend-components.md` — frontend routes/components.
- `CONTRIBUTING.md` — contributor workflow.

See `IMPLEMENTATION.org` and `plan.org` for current implementation plan/history.
