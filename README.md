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

Example user units for `beehived.service`, `beehive-honeybee.service`, and `beehive-honeybee.timer` live in `docs/orchestration.md`. They run the frontend on port `8955` and launch honeybee passes as transient `run-*.service` units so long passes do not block the timer.

```sh
systemctl --user daemon-reload
systemctl --user enable --now beehived.service
systemctl --user enable --now beehive-honeybee.timer
```

Core files:

- `ROI.md` — human-owned record of intent. Agents must not edit it.
- `PLAN.md` — honeybee-owned task plan derived from `ROI.md`.
- `sessions/` — honeybee transcripts.
- `docs/` — task change records.
- `repo/` — target source git submodule.
- `worktrees/` — isolated task worktrees.

## Common CLI commands

```sh
beehive init <path>
beehive submodule add <repo>
beehive submodule link <a> <b>
beehive submodule plan rollback <plan-id>
beehive secret add|update|edit -f file.yaml
beehive honeybee start <path>
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
