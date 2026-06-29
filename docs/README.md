# Beehive

AI coding-agent platform: developer command/control frontend + autonomous honeybee swarm that
continuously reconciles code with team intent. State is a git repo; coordination is git merges.

## Install
```
# package manager generates gpg key at install, or bring your own
sudo apt install beehive            # places binaries + /etc/beehive
beehive --version
```
Keys/config in `/etc/beehive` shared by cli, frontend, honeybee. Works single-host, config-managed, or
container bind-mount.

## Quick start
```
beehive submodule add git@github.com:org/web-frontend.git   # dormant until ROI authored
# author submodules/web-frontend/ROI.md in the frontend, then:
beehive honeybee start submodules/web-frontend              # bootstraps PLAN.md, works tasks
beehived                                                     # frontend on :8080
```

## Concepts
- **ROI.md** record of intent. Human-owned. Honeybees may never edit it.
- **PLAN.md** tasks, bootstrapped from ROI by first honeybee; rightsized for one context window.
- **honeybee** one agent, one task; coordinates via commit-race to main. Runs one opencode session per task;
  model set in opencode config under /etc/beehive (provider-agnostic).
- **task types** GC > arbitration > review > main. Stuck (>1h) tasks GC'd.

## CLI
```
beehive init <path>
beehive submodule add <repo>
beehive submodule link <a> <b>
beehive submodule plan rollback <plan-id>
beehive secret add|update|edit -f file.yaml
beehive honeybee start <path>
beehive worktree add|rm <submodule> <branch>
```

## Secrets
Single `SECRETS.yaml.gpg`, one encrypted yaml doc, referenced by INFRASTRUCTURE.md. gpg-managed.

## Docs
- `docs/install.md` — install, packaging, /etc/beehive, release verification
- `docs/secrets.md` — gpg secrets workflow
- `docs/honeybee.md` — honeybee operation + turn loop
- `docs/opencode.md` — agent backend setup
- `docs/cli.md` — CLI reference
- `docs/repo-layout.md` — beehive repo layout
- `docs/frontend-components.md` — frontend routes/components
- `CONTRIBUTING.md`, `docs/RELEASE-NOTES-TEMPLATE.md`

See `IMPLEMENTATION.org` for the full plan.
