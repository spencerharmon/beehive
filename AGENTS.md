# AGENTS.md — beehive

Operational conventions for any AI agent working in this repository (the
self-improvement loop's source). Read alongside the project's planning docs
(ROI.md / PLAN.md) and prompts/AGENTS.md (the honeybee runtime prompt). This
file governs how an agent *operates the live system*, not how honeybees run.

## Operational safety — never halt running work without explicit permission

Do **not** kill, stop, signal, restart, or otherwise interrupt any running
process or unit without the operator's explicit approval **in the current
session**. This specifically includes:

- `kill` / `pkill` / `kill -9` against any pid.
- `systemctl --user stop|kill|restart run-*.service` (honeybee passes).
- `systemctl --user stop|restart beehived.service` while it is serving.
- Pausing/stopping `beehive-honeybee.timer` or `.service`.
- Any `git`/worktree command that removes or resets a worktree, branch, or
  checkout an active pass is using.

Why this is a hard rule: stopping a pass mid-flight **discards its in-progress
work** and leaves a **zombie task claim** (the SIGTERM'd process never releases
its session+heartbeat), which blocks selection of that task until the TTL GC
reclaims it (~60 min). One unsolicited stop can stall several tasks and waste a
whole cycle.

When something looks wedged or zombie: **report, don't act.** Surface the unit,
pid, last log line, and age vs TTL, recommend the action, and let the operator
decide. Read-only diagnostics are always fine: `journalctl`, `systemctl status`
/ `list-units`, `git status`/`log`/`show`, reading files.

The only exceptions are an operation the operator explicitly requested this
session, or one they pre-approved in writing here.

## Deploys

Deploying the loop's binaries is `~/.local/bin/beehive-rebuild` (builds from
origin/main into `~/.local/bin`, temp-then-swap). It restarts `beehived` only
when that binary changed — which briefly interrupts the frontend but not any
honeybee pass. Running it is normal and allowed; do not separately bounce
`beehived` to force a restart of in-flight work.

## Source of truth

- `beehive` origin/main is the single source of truth for the loop's code.
- ROI.md is **human-owned / hook-protected** — never edit it as an agent.
- PLAN.md is **honeybee-owned** (written by reconcile/bootstrap) — never
  hand-edit it; doing so races the reconcile pass.
