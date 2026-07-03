# Conflict resolution & convergence

How beehive components converge writes through git, what happens when two
converge at once, and — when git can't auto-merge — who resolves it. Read
`sharing-modes.md` first (local vs remote sharing; the startup preflight).

## The convergence protocol (per publisher)

Every component authors in a **private worktree/branch** and lands work by:

1. **merge main into the branch** (pull the latest, resolve any conflicts *on the
   branch*),
2. **fast-forward/merge the branch onto main** (an atomic ref update).

`main` is never edited in place. On a race (main moved under us, no conflict) the
publisher re-merges and re-pushes — an 8-attempt loop. On a **conflict**, it
`git merge --abort`s (which touches only the *branch* worktree; main is left
untouched — the anti-wedge guarantee) and stops. Aborting main-side is correct and
transient; the *task* is what must eventually resolve and retry.

The plan is meant to keep this rare: tasks are sized/separated so two in-flight
honeybees are unlikely to touch the same code or plan region. Conflicts are the
exception, and the exception must be **recoverable, not fatal**. Almost no state
should be unrecoverable by a honeybee; when one genuinely is, abort **without
spending tokens**.

## Three conflict regimes

A publish conflict is one of three things. The instrumentation names the
conflicted paths in the error (`ErrConflict … (conflicted: <paths>)`), which is
how we tell them apart in the logs.

### 1. Gitlink conflict — a *deferred submodule merge*
The hive pointer `submodules/<sm>/repo` is a gitlink: one submodule commit SHA.
Two regimes move it:
- **pointer-sync** (→ submodule tracked-branch tip) is **linear** — conflict-free.
- **work completion → NEEDS-REVIEW** bumps the pointer to the **bee-branch tip**,
  which is *off* submodule main (verified). Two concurrent completions point the
  gitlink at two **divergent** bee-branches → git can't merge a bare SHA → conflict.

**Resolution:** a gitlink conflict *is* an unperformed submodule merge. Resolve by
merging the two bee-branches **inside the submodule** (deterministic when they
don't overlap; hand to a honeybee when the code actually clashes), commit the
merge, and set the gitlink to that merged SHA. **Never pick a side** (`--theirs`)
— that silently drops the other task's work. The review step already performs
submodule merges with an LLM reviewer present, so the machinery half-exists.

### 2. Hive-doc conflict — needs LLM
Two honeybees change the same region of a shared, non-session hive file —
`PLAN.md` (concurrent claim/heartbeat/status edits, or a whole-file reconcile
racing a status flip), `INFRASTRUCTURE.md`, etc. Git flags a text conflict.
**Resolution:** hand the conflict to a honeybee, resolve semantically, retry.
`PLAN.md` is *designed* to minimize this (per-task sections, claim commits scoped
to `PLAN.md`), but adjacent edits and whole-file reconciles still clash.

### 3. Session-transcript conflict — mechanical, never LLM
The session branch writes only its **unique, append-only** `sessions/<sid>.md`; no
other bee touches that path. So a transcript publish should be **conflict-free by
construction**. If it conflicts, that is a **plumbing bug**, not a case for the
LLM — resolving it by waking an agent would burn tokens on a mechanical fault.
And crucially: the transcript is a **convenience artifact** (it also lives on the
session branch, which beehived reads for the live view), so **its publish must
never gate or block the work publish**. *(Implemented — see Status.)*

## Serializing resolution (the lock)

The current model is **optimistic**: no lock, retry on race, abort on conflict.
That's ideal for the common non-conflicting case. For the conflict case, serialize
the *resolution* so two honeybees don't thrash the same merge:

- Reuse the existing **commit-race claim lock** (`internal/claim`) — heartbeat +
  TTL + GC. The winner resolves and lands; the loser waits for the lock to clear
  (deterministic), then re-merges the winner's result.
- **Deterministic fast-path:** the loser re-merges main→branch deterministically;
  it only wakes an LLM if that merge *actually* conflicts. Don't spend tokens
  evaluating a clean merge.
- **TTL/heartbeat** so a honeybee that dies mid-merge doesn't wedge the lock — the
  next pass reclaims it. This is the "nothing unrecoverable" guarantee, concrete.
- **Hybrid, not pure pessimism:** keep optimistic retry for the common race; drop
  to the lock *only when a conflict is detected*, to serialize resolution. The
  expensive LLM work still parallelizes; only the contended merge serializes.
- The submodule's own `main` ref already serializes review merges (atomic ref
  update; a non-ff loser must merge-in-submodule), so the lock most helps the
  concurrent-completion gitlink case and the hive-doc case.

## Retry / give-up

- Bounded retries (re-merge + re-publish), then give up with a **classified,
  path-named** error — never a silent or bare failure.
- Give up only when a conflict is genuinely unresolvable; and when you do, **do not
  spend tokens** — abort cleanly and log, so a human or a later pass can act.

## Status

- **Implemented:** conflicted paths are named in every publish conflict error
  (`git.conflictErr`); the **session-transcript publish is decoupled from the work
  publish** — the work lands first and its success alone gates completion; a
  transcript-publish failure is a logged WARNING that never blocks the task
  (leaving the transcript on the session branch). This removes the coupling that
  let a cosmetic transcript merge-conflict stall delivery.
- **Pending log confirmation:** which regime actually dominates the conflict logs
  (gitlink vs `PLAN.md` vs transcript). The named-path instrumentation feeds the
  standing **log-review** plan item; the resolution work below is prioritized off
  that data rather than a guess.
- **Specified, not yet built:** LLM-in-the-loop resolution (re-enter an agent turn
  on a resolvable conflict, resolve, retry); deterministic gitlink submodule-merge;
  the conflict-serialization lock.
