# Skill: Repair a corrupt PLAN.md

> Use when: `beehived`'s plan page (or any plan read) fails to parse a submodule's
> `PLAN.md` with an error like `plan: bad heartbeat "" for <task>` (or `bad
> attempts`, `bad weight`, `bad not_before`), and the plan is otherwise stuck —
> reconcile cannot self-heal a file it can no longer parse.

## What breaks and why

Each task is an H2 header carrying a metadata comment the parser reads strictly:

```
## <task-id> [<STATUS>] <!-- attempts=1 deps=a,b weight=32 session=<id> heartbeat=<RFC3339> not_before=<RFC3339> -->
```

The canonical serializer (`internal/plan/plan.go`, `Task.header()`) is the source
of truth for that line. It **omits** optional keys when they carry no value:

- `weight=` only when `> 1`,
- `session=` only when non-empty,
- `heartbeat=` / `not_before=` only when the timestamp is non-zero.

So a key present with an **empty value** — most commonly `session= heartbeat=` — is
never something `header()` emits. It is corruption: a pass killed mid-write (OOM,
crash, or a hard timeout during a claim/heartbeat/release commit) left a half-
written stamp. The parser then rejects it — `time.Parse(RFC3339, "")` fails, giving
`plan: bad heartbeat "" for <task>` — and every reader that parses that `PLAN.md`
(the frontend, and the reconcile/bootstrap passes that would otherwise fix the
plan) fails on it too. The plan is wedged until the stamp is repaired by hand.

## Why hand-editing PLAN.md is allowed HERE

The standing rule is **never hand-edit `PLAN.md`** — reconcile/bootstrap own it.
This is the one sanctioned exception: a *parse-blocking corruption* that reconcile
cannot repair because it cannot read the file to begin with. It is an operator-
directed, surgical **metadata** repair (the task's status/body content is not
touched), not a plan-content edit. Keep it that way — fix only the malformed stamp,
change nothing else.

## Procedure

1. **Find the offending header.** The error names the task id:
   `grep -nE '<task-id>.*<!--' submodules/<sm>/PLAN.md`. Confirm the metadata
   comment carries an empty-valued key (e.g. `session= heartbeat=`) or an otherwise
   non-parseable value (`attempts=`, `weight=`, a non-RFC3339 `heartbeat=`/
   `not_before=`).

2. **Edit through the worktree process** — `PLAN.md` is a superproject beehive-layer
   file, so change it in a worktree, never the live checkout (see
   `shared-checkout-edits.md`):
   ```
   beehive worktree add fix-plan-<task-id>
   # edit .worktrees/fix-plan-<task-id>/submodules/<sm>/PLAN.md
   ```

3. **Rewrite the stamp to canonical form.** Match exactly what `header()` would
   emit for that task's true state:
   - An unclaimed task (DONE, TODO, …) carries **no** `session=`/`heartbeat=` —
     delete the empty keys:
     `## <id> [DONE] <!-- attempts=1 deps= weight=32 session= heartbeat= -->`
     → `## <id> [DONE] <!-- attempts=1 deps= weight=32 -->`
   - A genuinely claimed task needs a real `session=<id>` and an RFC3339
     `heartbeat=<ts>`; if those are unrecoverable, drop both (the runner re-stamps a
     live claim on the next pass, and stale-claim GC ignores an absent claim).
   - Drop `weight=` when it is `1`; keep `attempts`/`deps` as they are.

4. **Validate the whole file parses before publishing.** No header comment may
   carry an empty-valued or malformed key:
   `grep -nE '^## .*<!--.*(session=($| )|heartbeat=($| )|not_before=($| ))' submodules/<sm>/PLAN.md`
   must return nothing, and every `heartbeat=`/`not_before=` value in a header line
   must be RFC3339. (Prose in task bodies that mentions `heartbeat=` is fine — only
   the `## ` header lines are parsed.)

5. **Publish and clean up.** Commit in the worktree, then converge to `main`:
   ```
   git -C .worktrees/fix-plan-<task-id> push . HEAD:main   # local-only hive (updateInstead)
   # with a remote, ALSO: git push <remote> HEAD:main       # e.g. gitea, so the replica follows
   beehive worktree rm fix-plan-<task-id>
   ```
   Confirm the plan reads clean (the frontend plan page loads; a `beehive` plan
   read no longer errors).

## Healthy state

Every task header's metadata comment round-trips through the parser: no empty-valued
keys, every timestamp RFC3339, `weight`/`session`/`heartbeat`/`not_before` present
only when they carry a real value. The frontend plan page renders and reconcile can
run again.
