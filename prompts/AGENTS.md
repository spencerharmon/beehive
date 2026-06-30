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

- `AGENTS.md` — this file. Generic operating guide + skills. Managed.
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

## Skills (standard procedures)

### Modify an ROI
`ROI.md` is human/operator-owned and hook-protected against honeybee edits. To
change a target's intent, edit `submodules/<name>/ROI.md` directly (as the operator,
or via the UI editor), commit it, and let the next **reconcile** pass fold the diff
into `PLAN.md` (it compares `ROI.md`'s head against `PLAN.md`'s `Beehive-ROI` stamp).
Never edit `PLAN.md` by hand to "apply" an ROI change — reconcile owns that and will
re-weight tasks on the logarithmic priority scale.

### Add a submodule
`beehive submodule add <name> <git-url>` registers the target's source as a
submodule under `submodules/<name>/repo/`, scaffolds the beehive layer, and updates
`.gitmodules`. Then author `submodules/<name>/ROI.md` (the intent). The next
**bootstrap** pass (ROI present, PLAN absent) decomposes it into a weighted `PLAN.md`.
Use `beehive submodule link <a> <b>` to record cross-submodule dependencies.

### Remove a submodule
Use `beehive submodule rm <name>` (deregisters the gitlink, removes the
`submodules/<name>/` tree and its `.gitmodules` entry). Removing intent for a target
that still has in-flight work: retire its `PLAN.md` tasks first (reconcile moves
in-flight retirees to `NEEDS-REVIEW`) so nothing is silently dropped.

### Bootstrap
Standing up a new target: add the submodule, write `ROI.md`, and a bootstrap pass
will create `PLAN.md`. Standing up a whole install: follow `BOOTSTRAP.md`.

### Rebootstrap
To rebuild a target's plan from scratch (e.g. ROI was rewritten wholesale), remove
`submodules/<name>/PLAN.md` and let a bootstrap pass regenerate it from `ROI.md`.
In-flight worktrees/claims should be drained first; surviving `docs/` remain as
history. Prefer a normal reconcile for incremental ROI edits — rebootstrap is the
heavy hammer.

### Cleanup operations
Stale worktrees, orphan gitlinks, drifted submodule checkouts, and abandoned
session branches accumulate. Use the `beehive-hygiene` skill / `beehive submodule
sync <name>` to resync a drifted submodule checkout to its recorded gitlink, and
prune stale `worktrees/` and dead session branches. Healthy state: clean tree, every
gitlink checked out, no zombie task claims past TTL.

### Update instructions
`beehive instruction update [--clobber]` rewrites the managed instruction files
(`AGENTS.md`, `HONEYBEE.md`, `BOOTSTRAP.md`) to the binary's current defaults:
- An unchanged or missing file is written in place.
- A file you have modified is, without `--clobber`, offered for confirmation
  (overwrite y/N); with `--clobber` it is backed up to `<name>.<epoch>.bak` and
  replaced. The backup and the new copy are both committed.
`LOCALS.md` and per-repo content are never touched.

## Absolute rules (apply to every agent)

- NEVER edit any `ROI.md` as an agent — it is the human record of intent.
- NEVER hand-edit a `PLAN.md` — reconcile/bootstrap own it.
- Do not stop, kill, or restart running passes/processes without explicit operator
  approval; see `LOCALS.md` for the local safety rule and how work is scheduled.
- No shortcuts: compute real values; no placeholders, swallowed errors, or fake
  "done".
