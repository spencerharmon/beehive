# Beehive documentation

Beehive is git-native orchestration for autonomous coding agents. Team intent, plans, sessions, and source checkouts live in one repo; independent honeybees work isolated tasks and converge through git.

Binaries:

- `beehive` — deterministic CLI. No LLM calls.
- `beehived` — web frontend daemon. Default listen address: `:8955`.
- `honeybee` — one-task agent runner.

## Install directly from repo

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
sudo ./scripts/install.sh
beehive version
```

User-local install:

```sh
git clone https://github.com/spencerharmon/beehive.git
cd beehive
PREFIX="$HOME/.local" \
BEEHIVE_CONFIG_DIR="$HOME/.config/beehive" \
./scripts/install.sh
export PATH="$HOME/.local/bin:$PATH"
beehive version
```

Build without installing:

```sh
mkdir -p bin
for bin in beehive beehived honeybee; do
  CGO_ENABLED=0 go build -trimpath -o "bin/$bin" "./cmd/$bin"
done
```

`./scripts/install.sh` installs all three binaries, prefers matching `dist/` artifacts, builds from source when needed, creates config only if missing, and generates a gpg key only when no secret key exists. It honors `PREFIX`, `BEEHIVE_CONFIG_DIR`, `DESTDIR`, `TMPDIR`, `CGO_ENABLED`, and `BEEHIVE_SKIP_KEYGEN=1`.

## Quick start

```sh
beehive init ~/beehive-infra
cd ~/beehive-infra
beehive submodule add git@github.com:org/project.git
# Author submodules/project/ROI.md in editor or frontend.
beehive honeybee start submodules/project
beehived -repo .                         # http://localhost:8955
```

## Core concepts

- `ROI.md` — human-owned record of intent. Honeybees must not edit it.
- `PLAN.md` — honeybee-owned task plan bootstrapped/reconciled from ROI.
- `honeybee` — one agent, one task, isolated worktree, git commit/merge convergence.
- Task classes — GC, arbitration, review, main work.
- `/etc/beehive` — default shared config and gpg keyring; override with `BEEHIVE_CONFIG_DIR`.

## CLI sketch

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

## Docs index

- `../README.md` — top-level overview and source install instructions.
- `docs/install.md` — install, packaging, `/etc/beehive`, release verification.
- `docs/secrets.md` — gpg secrets workflow.
- `docs/honeybee.md` — honeybee operation and turn loop.
- `docs/orchestration.md` — systemd user unit examples for frontend and scheduled honeybee passes.
- `docs/opencode.md` — agent backend setup.
- `docs/cli.md` — CLI reference.
- `docs/repo-layout.md` — beehive repo layout.
- `docs/frontend-components.md` — frontend routes/components.
- `CONTRIBUTING.md`, `docs/RELEASE-NOTES-TEMPLATE.md`.

See `IMPLEMENTATION.org` and `plan.org` for implementation plan/history.
