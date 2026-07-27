# handoff-ordering-adjacent-audit

Design doc for the "audit every adjacent path with the same ordering shape" clause
of the terminal-leak blocker (depends on `handoff-terminal-leak-spec`, which
modeled the leak itself in `TaskStatus.tla` as `NoDoclessTerminal` +
`TaskStatus_leak_{buggy,fixed}.cfg`).

## Goal

Prove the handoff terminal-leak ordering shape — publish a FORWARD terminal status
(`NEEDS-REVIEW`/`DONE`) ahead of its durable backing artifact — exists NOWHERE
except the already-modeled handoff/`Heartbeat` pin path. For any adjacent path that
does reproduce it, add a buggy+fixed cfg in the correct spec (`TaskStatus.tla`
status/doc, `MainConvergence.tla` `main` writes, `SubmodulePointer.tla` gitlink).

## Candidate shapes

- (a) an `internal/claim` verb doing `CommitPaths(planRel())` + `Publish`;
- (b) a git working-tree reset/clean that could drop a not-yet-published artifact.

## Method & verdicts

Each path was read and evaluated against the leak's essential precondition
(forward terminal published while its doc is not yet durable on `main`). The full
verdict table lives in `docs/formal-spec-mapping.md` §"Adjacent-path audit — the
handoff terminal-leak shape". Summary:

- **Shape (a)** — `Heartbeat` IS the leak (modeled). Every other claim verb is
  cleared: `Release`/`RecordReviewCommit`/locks are status-neutral;
  `Reject`/`Strand`/`RecoverLostWork` move BACKWARD (rework/recovery), the inverse
  of leaking a terminal; `BounceUnreachable` sets the non-terminal
  `NEEDS-ARBITRATION`; `FinalizeAlreadyMerged`'s `→ DONE` is gated on a proven
  ancestry-of-tracked-main merge with the work-pass doc already durable — exactly
  `NoFalseDone`'s (durable ∧ merged) conjunct.
- **Shape (b)** — `healLocalMain`/`EnsureCleanCheckout`, the `PublishToMain`/
  `UpdateLocalMain` dirty-tree heal, and the `landSourceBranch`/`demoteUnpushed`
  path all `reset --hard HEAD` (or push) scoped to the MAIN worktree /
  `submodules/<name>/repo` PROJECTION — never the agent's
  `submodules/<name>/worktrees/bee-*` worktree — so they drop only uncommitted
  drift, never a committed artifact. `RestoreConfig`/`RestoreRemotes` touches only
  git-config remote sections: no tree, no artifact, no publish.

## Outcome

All adjacent paths cleared; no uncovered gap. No new cfg added — the sole real
instance is the handoff/`Heartbeat` path already covered by
`TaskStatus_leak_{buggy,fixed}.cfg`. `specs/run-tlc.sh` is unchanged; all 18 cases
still behave as declared.
