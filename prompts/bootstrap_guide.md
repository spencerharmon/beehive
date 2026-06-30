# BOOTSTRAP.md — standing up a beehive install

Step-by-step guide to take an empty directory to a running beehive loop. Generic:
every site-specific value you decide here gets recorded in `LOCALS.md`, which this
guide helps you write. Run through it top to bottom; each step is idempotent.

This file is a beehive-managed default (refreshed by `beehive instruction update`).

## 0. Prerequisites

- The `beehive` and `honeybee` binaries built and on `PATH` (or note their install
  path in `LOCALS.md`).
- An agent CLI the runner can drive (e.g. `opencode`) and a model.
- git configured with whatever push credentials the targets need.

## 1. Create the beehive repo

```
beehive init <dir>
```

This scaffolds `submodules/`, installs the managed instruction files
(`AGENTS.md`, `HONEYBEE.md`, `BOOTSTRAP.md`), installs the git hooks (ROI-protect
pre-commit + submodule-sync post-receive), and sets `receive.denyCurrentBranch=
updateInstead` so honeybee worktrees can publish to the checked-out `main` with no
remote. Make `<dir>` a git repo (`git init`) if it is not already.

## 2. Author LOCALS.md

Create `LOCALS.md` at the repo root capturing the facts unique to THIS install. At
minimum:

- **Source & build**: where the loop's source lives, the exact build command and
  environment (cache/tmp dirs, flags, toolchain version), and where binaries land.
- **Deploy**: the command that rebuilds/installs binaries and what it restarts.
- **Scheduler**: how passes are launched (e.g. a systemd timer/service, cron, or a
  manual launcher) and how to start/inspect/stop them.
- **Observability**: how to read logs (per-pass and the frontend), and the frontend
  URL/port.
- **Topology**: hostnames, whether targets have remotes or the hive is local-only,
  and the single source of truth for code.
- **Safety rules**: any local "do not do X without approval" rules (e.g. never halt
  a running pass — halting discards in-flight work and leaves a zombie claim until
  TTL GC).
- **Config**: location and key values of the runner config (see step 4).

`LOCALS.md` is yours: `beehive instruction update` never overwrites it.

## 3. Add targets and write their intent

For each target you want the swarm to improve:

```
beehive submodule add <name> <git-url>
# then author submodules/<name>/ROI.md  (the human record of intent)
```

Record cross-target dependencies with `beehive submodule link <a> <b>`. A bootstrap
pass (ROI present, PLAN absent) will decompose each `ROI.md` into a weighted
`PLAN.md`. Do not write `PLAN.md` by hand.

## 4. Configure the runner

Point the runner at its config (location is your choice — record it in `LOCALS.md`;
a common spot is `/etc/beehive/config.yaml`). Typical keys: `agent_cmd`, `model`,
`max_turns`, `reject_limit`, `ttl_minutes`, `turn_timeout_minutes`. These bound a
pass and the claim TTL.

## 5. Schedule passes

Wire your scheduler to launch one honeybee pass per tick against the repo root.
Passes are independent and worktree-isolated; running several concurrently is safe
(claims + the singleton reconcile/bootstrap lock prevent duplication). Keep the
cadence high enough to keep the swarm busy. Record the exact units/commands in
`LOCALS.md`.

## 6. Verify

- `beehive instruction update` reports the managed files clean.
- A manual pass selects a task, creates a worktree, and either reconciles, bootstraps,
  or works a task, then publishes to `main` with a clean tree afterward.
- The frontend lists the submodules, their plans, and live/idle sessions.

## 7. Keep instructions current

After upgrading the binaries, run `beehive instruction update` to refresh the managed
instruction files (it backs up any you have customized). `LOCALS.md` and your
`ROI.md`s are never touched.
