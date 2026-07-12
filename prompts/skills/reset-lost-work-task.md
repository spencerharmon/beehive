# Skill: Reset a lost-work / looping task for reimplementation

> Use when: a task is stuck in a `recover-lost-work` loop, or was escalated to
> `NEEDS-HUMAN` purely by that loop, and an operator wants it reimplemented cleanly
> on the next pass. This is the **manual, operator-directed** counterpart to the
> deterministic runner guard (`lost-work-recover-any-status`); until that guard is
> deployed everywhere, or for a task already wedged past `reject_limit`, an operator
> resets it by hand.

## The failure this fixes

A work pass authors in its worktree, then before the runner can durably publish
(commit + push `bee-<taskid>` to the submodule origin + bump the gitlink) the pass is
capped/OOM-killed/crashes. The runner's completion check then finds the implementer
commit unreachable **everywhere** — no local branch, no branch on the submodule
remote after a prune-fetch, gitlink not advanced onto it, no
`docs/bee-<taskid>-<taskid>.md` — and runs `RecoverLostWork`, which bumps `attempts`
and resets the task to `TODO`. Each loop `attempts++`; once `attempts > reject_limit`
(default 3) the SAME lost-work signal escalates to `NEEDS-HUMAN` — a no-decision gate
that strands the task and everything that deps on it.

A frequent aggravating variant: the deliverable **already landed** on the submodule's
`origin/main` (an early attempt pushed the commit straight to `main` instead of via
the `bee-<taskid>` merge flow) and the gitlink already points at it, but the runner
still looks for the `bee-<taskid>` branch, never finds it, and loops. Passes then burn
their whole turn budget re-deriving "why does this commit already exist / is it
merged" and never converge on the bookkeeping the runner expects.

## Why a naive "flip to TODO" is WRONG

The common mistake (the one this skill exists to prevent): resolving the task by only
flipping the status marker `NEEDS-HUMAN` → `TODO`. That leaves `attempts` at its
elevated value, so the very next `RecoverLostWork` (or any single review rejection)
pushes `attempts` back over `reject_limit` and re-escalates to `NEEDS-HUMAN`
immediately. The task bounces straight back. **You must reset the attempt counter
too**, plus clear the stale claim and strip the accumulated failure notes.

## Procedure

Read `shared-checkout-edits.md` first — `PLAN.md` is edited through the top-level
worktree process, never the live checkout.

1. **Diagnose before resetting.** Check whether the deliverable already exists, so you
   do not schedule a pointless reimplementation:
   - `git ls-tree HEAD submodules/<sm>/repo` — the recorded gitlink commit.
   - `git -C submodules/<sm>/repo ls-remote origin bee-<taskid>` — is the bee-branch on
     origin? (Loop cause if empty.)
   - `git -C submodules/<sm>/repo branch --contains <gitlink-sha> -a` and inspect the
     commit — is the task's code already merged into the submodule's tracked `main`?
   - Skim the newest `submodules/<sm>/sessions/bee-<taskid>-*.md` tails: if passes keep
     re-discovering an already-landed commit and running out of turns, the work is done
     but the bookkeeping never converged. In that case the correct resolution may be to
     mark it reviewable/`DONE` rather than reimplement — decide with the operator; do
     not silently drop or fake a review.

2. **Open the plan through a top-level worktree** (`beehive worktree add <branch>`;
   edit `.worktrees/<branch>/submodules/<sm>/PLAN.md`).

3. **Reset the task block** so the next pass gets a genuinely fresh attempt:
   - Status → `TODO`.
   - `attempts=0` (the elevated count was all the SAME lost-work signal, not genuine
     review rejections — do not carry it forward).
   - Clear any stale claim: remove `session=<id>` and `heartbeat=<...>` from the
     metadata comment.
   - Strip the accumulated `Recovered (runner, lost work): …` and `Human-needed: …`
     body lines and any stale prior reset note — they are dead narrative.
   - Add ONE concise operator note: date, that this is an operator-directed clean reset,
     and the anti-recurrence instruction — **do not flip the task to `NEEDS-REVIEW`
     until the code is committed in the worktree, `bee-<taskid>` is pushed to the
     submodule origin, the gitlink is bumped, and `docs/bee-<taskid>-<taskid>.md`
     exists** — otherwise the work strands unreachable and loops again.

4. **Validate**: `beehive plan validate <sm>` (must report the plan parses and
   round-trips) before publishing.

5. **Publish** the plan edit to `main` per `shared-checkout-edits.md` (push to the
   local hive `main`; rebase onto the latest tip first if a pass advanced it under
   you), then remove the worktree.

## After the reset

- Confirm the task's deps are still satisfied so it is actually selectable.
- If the same task loses work again after a clean reset, the cause is upstream: a pass
  reliably dying before publish (turn cap / OOM on the memory-constrained host, or
  agents burning the budget on an already-landed-commit dead end). That is a
  runner/turn-budget problem, not another manual reset — track it as its own task
  (see `lost-work-recover-any-status`, which generalizes the runner guard to reset ANY
  non-terminal lost-work task to `TODO` and never route lost work to `NEEDS-HUMAN`).

## Healthy state

Task `TODO`, `attempts=0`, no stale `session=`/`heartbeat=`, no residual lost-work /
Human-needed notes, deps satisfied, plan validates.
