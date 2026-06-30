# AGENTS.md — operating a beehive repo

Generic guide for any agent (human-driven or autonomous) that operates a beehive
repository. It is NOT the honeybee runtime protocol (that is `HONEYBEE.md`) and it
holds no site-specific facts (those are in `LOCALS.md`). It describes what a beehive
repo is, the files that carry context, and the standard procedures ("skills") for
common operations.

This file is a beehive-managed default: the binary ships it and
`beehive instruction update` refreshes it (with backup) when it advances. Edit it
freely for your install; an update will offer to merge or back up your version.

## What a beehive repo is

A beehive repo is the coordination layer for an autonomous self-improvement swarm.
Independent agents ("honeybees") each work ONE task in isolation and converge by
committing/merging to `main` — there is no central controller and no shared write
lock. The repo's job is to hold, per target, the *intent* (`ROI.md`), the derived
*plan* (`PLAN.md`), the *change record* (`docs/`, `sessions/`), and the target's
*source* (a git submodule under `repo/`).

The repo root is a beehive repo; each tracked target lives at `submodules/<name>/`.

## Context files and ownership

Repo root (beehive-managed defaults unless noted):

- `AGENTS.md` — this file. Generic operating guide + the `skills/` index. Managed.
- `HONEYBEE.md` — the honeybee runtime protocol the runner injects each pass. Managed.
- `BOOTSTRAP.md` — step-by-step guide to stand up a new install (locals, infra,
  submodules, scheduler). Managed.
- `LOCALS.md` — **site-specific** operational facts: machine paths, build/deploy
  commands, scheduler units, hostnames/ports, and local safety rules. **Authored
  per install, NOT managed** — `beehive instruction update` never touches it.
  `BOOTSTRAP.md` walks you through creating it.
- `INFRASTRUCTURE.md` — repo-wide infrastructure notes. Per-repo content.
- `.gitmodules`, `SUBMODULE-LINKS.yaml` — submodule registry and cross-links.

Per submodule (`submodules/<name>/`):

- `ROI.md` — the human-owned record of intent for this target. **Never edited by an
  agent** (a pre-commit hook blocks honeybee edits). Drives bootstrap/reconcile.
- `PLAN.md` — the honeybee-owned task list derived from `ROI.md`, carrying an ROI
  stamp `<!-- Beehive-ROI: <sha> -->`. Written by bootstrap/reconcile passes; do
  not hand-edit (it races the reconcile pass).
- `docs/` — one terse, LLM-targeted change doc per task (`<branch>-<taskid>.md`).
- `sessions/` — recorded honeybee transcripts (one `.md` per session branch).
- `repo/` — the target's actual source, tracked as a git submodule (gitlink).
- `worktrees/` — per-task code worktrees the runner creates and tears down.
- Optional: `INFRASTRUCTURE.md`, `ARTIFACTS.md`, `RULES.md`, `AGENTS.md` (a
  submodule-local rules overlay). Render whether or not present; absent ones are
  created on first edit.

## Skills (the `skills/` directory)

Standard procedures are NOT inlined here. Each lives as its own file under the
repo-root **`skills/`** directory, is individually tracked and maintained, and is
refreshed by `beehive instruction update` (the skill files are part of the managed
set). **Read a skill file into context only when your task matches it** — do not load
them all up front. Resolve a skill's path against `skills/` at the repo root.

| Skill | Read it when | File |
|-------|--------------|------|
| Modify an ROI | a target's intent must change | `skills/modify-roi.md` |
| Add a submodule | bringing a new target under the swarm | `skills/add-submodule.md` |
| Remove a submodule | retiring a target | `skills/remove-submodule.md` |
| Bootstrap | standing up a new target or whole install | `skills/bootstrap.md` |
| Rebootstrap | rebuilding a target's plan from scratch | `skills/rebootstrap.md` |
| Cleanup | clearing stale worktrees/branches/claims/drift | `skills/cleanup.md` |
| Update instructions | refreshing managed files to new binary defaults | `skills/update-instructions.md` |

The binary ships a default for every skill above; a site may add its own skill files
to `skills/` (an update never deletes them) or customize a shipped one (an update
backs the customized copy up before replacing it). Keep this index in sync when you
add a local skill so agents can discover it.

## Absolute rules (apply to every agent)

- NEVER edit any `ROI.md` as an agent — it is the human record of intent.
- NEVER hand-edit a `PLAN.md` — reconcile/bootstrap own it.
- Do not stop, kill, or restart running passes/processes without explicit operator
  approval; see `LOCALS.md` for the local safety rule and how work is scheduled.
- No shortcuts: compute real values; no placeholders, swallowed errors, or fake
  "done".
