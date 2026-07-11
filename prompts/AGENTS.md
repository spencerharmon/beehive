# AGENTS.md — operating a beehive repo

Generic guide for any agent (human-driven or autonomous) that operates a beehive
repository. It is NOT the honeybee runtime protocol (that is `HONEYBEE.md`, which the
runner injects into every pass) and it holds no site-specific facts (those are in
`LOCALS.md`). It describes what a beehive repo is, the files that carry context, the
standard procedures ("skills"), how to change files without racing the swarm, and —
so you never redo work the runner already does — what the deterministic process owns.

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

## Editing files — NEVER edit the live checkout

> This is the step agents miss most. Read `skills/shared-checkout-edits.md` BEFORE
> you change any tracked file in a beehive repo whose passes/`beehived` are running.

The working tree on `main` and every `submodules/<name>/repo/` checkout are **derived
state, not editing surfaces**. Honeybees and `beehived` share this filesystem: on the
next publish they reset the working tree to committed history (the dirty-tree heal)
and `beehive submodule sync` clobbers `repo/` to the tracked tip verbatim. So an
in-place edit is **silently discarded** — and a wedged dirty tree can block every
component's publish. Every component instead works a private worktree off the
freshest `main`/tip, commits there, and converges by merging/pushing back to `main`;
your manual edit is just one more participant in that protocol. Three cases:

- **Submodule CODE** — `submodules/<sm>/repo/` contents, including the self-hosting
  `beehive` submodule itself:
  `beehive submodule worktree add <sm> <branch>`; edit + commit inside
  `submodules/<sm>/worktrees/<branch>/` (never `submodules/<sm>/repo/`); land it on
  the tracked branch (`git push origin HEAD:main`); then
  `beehive submodule sync <sm>` and `beehive submodule worktree rm <sm> <branch>`.
- **Beehive-layer / superproject files** — `INFRASTRUCTURE.md`, `SUBMODULE-LINKS.yaml`,
  `LOCALS.md`, and a submodule's `INFRASTRUCTURE.md` / `ARTIFACTS.md` / `docs/`:
  `beehive worktree add <branch>`; edit under `.worktrees/<branch>/`; publish with
  `git -C .worktrees/<branch> push . HEAD:main` (local-only hive with
  `updateInstead`; push to `origin/main` if the hive has a remote); then
  `beehive worktree rm <branch>`. The root instruction files (`AGENTS.md` incl. this
  one, `HONEYBEE.md`, `BOOTSTRAP.md`, `skills/*.md`) look like they belong in this
  case too but don't — they're GENERATED; see the note right after this list.
- **`ROI.md`** — human-owned: prefer the `beehived` editor UI. Agents are hook-
  blocked from committing it under the honeybee identity; operator-directed edits go
  through the editor or the worktree process. See `skills/modify-roi.md`.

**Managed root files are generated — a durable fix edits the submodule source, not
the root copy.** `AGENTS.md` (this file), `HONEYBEE.md`, `BOOTSTRAP.md`, and every
`skills/*.md` are rendered from the `beehive` submodule's `prompts/<name>.md` (the
binary's `//go:embed` default): `beehive instruction update` overwrites the root
copy from that default, and `beehive init` seeds a brand-new install straight from
the binary — neither ever reads a root edit. So editing the root file via the
beehive-layer case above is cosmetic: the next `instruction update` reverts it, and
no other install ever sees it. The durable edit is a **Submodule CODE** change (the
first case above) to the source template: `beehive submodule worktree add beehive
<branch>`, edit `prompts/<name>.md`, commit + push, bump the gitlink. Source
mapping — `AGENTS.md` ← `prompts/AGENTS.md`; `HONEYBEE.md` ← `prompts/HONEYBEE.md`;
`BOOTSTRAP.md` ← `prompts/bootstrap_guide.md` (**not** `prompts/bootstrap.md`, the
unrelated per-pass bootstrap-task runtime prompt); `skills/<n>.md` ←
`prompts/skills/<n>.md`. After the pointer bump lands, confirm with `beehive
instruction list` (reports `clean`/`modified`/`missing` per managed file).
`LOCALS.md` is the one exception: it is **not** generated (no `prompts/` source
exists for it), so it genuinely is edited in place via the beehive-layer case above.

Never `git reset`/`checkout`/`stash` the live primary tree to "make room" — you race
in-flight publishes. Always remove your worktree + branch when done.
`skills/shared-checkout-edits.md` carries the full procedure and failure modes.

## The deterministic runtime (don't redo what the runner does)

A honeybee pass is a thin LLM turn-loop wrapped by a deterministic runner. The runner
— not the agent — owns everything in this list; a honeybee (and an operator-directed
agent) must NOT re-implement, second-guess, or manually reproduce any of it.
`HONEYBEE.md` is the per-kind role contract the agent DOES own; this section is its
exact complement. Each pass, the runner automatically:

- **Selects one task per submodule and fixes its KIND** deterministically — priority:
  bootstrap (no `PLAN.md`) → ROI reconcile (`PLAN.md`'s `Beehive-ROI` stamp is behind
  `ROI.md`) → a weighted-random pick among ready tasks. The picked task's status sets
  the kind: `TODO`→work, `NEEDS-REVIEW`→review, `NEEDS-ARBITRATION`→arbitrate. The
  agent never chooses the task or the kind and never re-runs selection.
- **Hands the agent its task description** — a work/review/arbitration pass gets the
  full task card injected in its Context, so the agent never opens `PLAN.md` or
  `ROI.md` to find or understand its task (it still writes `PLAN.md` for the status
  transition and dependent unlocks).
- **Owns the claim** — stamps `session=<id>` + a `heartbeat` on the task and
  re-stamps every turn; releases it on completion; on failure/timeout/cap leaves the
  stale claim as the GC signal. The agent never writes session/heartbeat and changes
  only the task STATUS.
- **Creates the code worktree** (work kind) at
  `submodules/<sm>/worktrees/bee-<taskid>/` off the submodule tip before turn 1. The
  agent edits there and never runs worktree/submodule plumbing or writes
  `submodules/<sm>/repo`.
- **Reverts git-config / remote drift** every turn, so a stray remote never outlives
  a turn.
- **Guards task removal** — pulls `main`; if the plan or the task vanished under it,
  the pass ends rather than working something nobody wants.
- **Checks completion deterministically** each turn, per kind — work: terminal status
  (`DONE` / `NEEDS-REVIEW` / `NEEDS-ARBITRATION`, or `NEEDS-HUMAN` with a reason) AND
  the change doc at `submodules/<sm>/docs/bee-<taskid>-<taskid>.md`; review: task left
  `NEEDS-REVIEW`; arbitrate: task left `NEEDS-ARBITRATION`; reconcile: `PLAN.md`'s ROI
  stamp matches `ROI.md` HEAD; bootstrap: `PLAN.md` exists. The agent need not
  self-declare done — meeting the predicate ends the pass.
- **Publishes and cleans up** — merges the commits the agent made in its worktree (the
  `bee-<taskid>` code branch, the submodule-pointer bump, `PLAN.md`/`docs/`) to `main`,
  drives conflict resolution (handing the agent only the conflicted files to rewrite),
  reclaims the merged source branch, streams the session transcript to `sessions/`, and
  removes the worktree. The agent authors and commits the change — including pushing its
  `bee-<taskid>` branch to the submodule origin and bumping the pointer, per its role
  step; the runner merges that to `main`, it does not write it for the agent.
- **Dedups reconcile** (skips one already applied by a peer) and **bounds each turn**
  with a timeout, sending "continue" until the completion check passes or a cap hits.

So: do your kind's role step (`HONEYBEE.md`), edit only your worktree + the beehive
layer, flip STATUS, write the doc — and let the runner do the rest.

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

- `ROI.md` — the human-owned record of intent for this target. **Never edited by a
  honeybee** — a pre-commit hook blocks commits touching it under the honeybee
  identity (`BEEHIVE_HONEYBEE=1`), and a server pre-receive mirrors that for pushes.
  Operator-*directed* edits ARE allowed and expected: an agent acting on the hive
  repo when an operator tells it to, or the beehived ROI/generic editor. Drives
  bootstrap/reconcile.
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
set). **Read a skill file into context only when your task matches it** — lazily,
never all up front. Resolve a skill's path against `skills/` at the repo root.

Two audiences use these. An **operator-directed hive agent** (you, acting on an
operator's instruction at the hive root) runs the management skills below. An
**autonomous honeybee** works under `HONEYBEE.md` with a pre-made worktree and
normally needs NO skill — its only reach for one is `shared-checkout-edits`, and only
if a task forces it to touch a file outside its handed worktree. The "Audience"
column below marks who each is for.

| Skill | Read it when | Audience | File |
|-------|--------------|----------|------|
| Edit on a shared checkout | before changing ANY submodule/layer file while passes or `beehived` share the filesystem — **mandatory, read first** | either | `skills/shared-checkout-edits.md` |
| Modify an ROI | a target's intent must change | operator | `skills/modify-roi.md` |
| Add a submodule | bringing a new target under the swarm | operator | `skills/add-submodule.md` |
| Remove a submodule | retiring a target | operator | `skills/remove-submodule.md` |
| Bootstrap | standing up a new target or whole install | operator | `skills/bootstrap.md` |
| Rebootstrap | rebuilding a target's plan from scratch | operator | `skills/rebootstrap.md` |
| Cleanup | clearing stale worktrees/branches/claims/drift | operator | `skills/cleanup.md` |
| Repair a corrupt PLAN.md | a plan fails to parse (e.g. `bad heartbeat ""`) after a pass was killed mid-write | operator | `skills/repair-plan.md` |
| Deferred verification | a work task's effect only shows after an external system converges (GitOps reconcile, CI run, cache/TTL) | honeybee | `skills/deferred-verification.md` |
| Update instructions | refreshing managed files to new binary defaults | operator | `skills/update-instructions.md` |

The binary ships a default for every skill above; a site may add its own skill files
to `skills/` (an update never deletes them) or customize a shipped one (an update
backs the customized copy up before replacing it). Keep this index in sync when you
add a local skill so agents can discover it.

## Absolute rules (apply to every agent)

- NEVER author in the live checkout — not the `main` working tree, not
  `submodules/<name>/repo/`. Any tracked-file change goes through the worktree
  process in "Editing files" (see `skills/shared-checkout-edits.md`); in-place edits
  are silently reset by running passes.
- Do NOT re-run, reproduce, or second-guess what the deterministic runner owns (task
  selection, claim/heartbeat, worktree creation, completion checks, the merge/publish
  to `main`, transcript, cleanup) — see "The deterministic runtime".
- NEVER edit any `ROI.md` **as a honeybee** (an autonomous pass) — it is the human
  record of intent, hook-protected against the honeybee identity. Operator-directed
  edits are allowed: a hive agent acting on an operator's instruction, or the
  beehived ROI/generic editor.
- NEVER hand-edit a `PLAN.md` — reconcile/bootstrap own it.
- Do not stop, kill, or restart running passes/processes without explicit operator
  approval; see `LOCALS.md` for the local safety rule and how work is scheduled.
- No shortcuts: compute real values; no placeholders, swallowed errors, or fake
  "done".
