# Standing Task Recurrence

A standing measurement task can succeed today without finishing its ongoing
obligation. Explicit daily recurrence records that success as a **TODO cycle**,
not DONE and not a convergence-wait defer. Ordinary tasks are unchanged; recurring
intent is never inferred from a task's name, prose, or exhausted defer counter.

## Adoption

For an existing TODO with a real `Check:` command, enable recurrence through the
normal operator mutation protocol:

```sh
beehive task recur <sm> <id> --every 24h
```

Only `24h` is supported. The command calls `Task.EnableRecurrence` through
`mutatePlanTask`, synchronizing and publishing the plan through the existing
primary-main convergence protocol. It does not reopen a task, reset attempts or
defers, release a claim, change dependencies, or move `not_before`. Repeating the
command does not reset anything. Non-TODO tasks, missing checks, and `check=none`
are refused. A `Verify-After-Merge:` alone is not a cycle check.

Bootstrap/reconcile should opt in only when intent calls for ongoing daily
measurement. Register the real check framework in `submodules/<sm>/CHECKS.md`;
enabling recurrence does not certify a check's approval or result. Deployment-
specific adoption belongs in the hive's change record, not in engine source.

## Successful Cycles

The owning work pass runs:

```sh
beehive task cycle <sm> <id>
```

The CLI resolves `BEEHIVE_WORKTREE` and `SUBMODULE_WORKTREE` through the normal
worktree resolvers. The actual hive Git branch is the session token and must be a
non-primary, attached branch matching the task's claim. It constructs a
`swarm.Runner` with the same config-derived check policy as `task check`, then
calls `Runner.RecordCycle(ctx, subName, taskID, checkoutDir)`.

`RecordCycle` runs the real registered `Check:` on a **clean code checkout at the
tracked-branch tip**. A local implementation commit, even on a clean branch, is
not a measurement-only cycle. Check failures or guard failures do not record a
successful cycle. There is no `--passed` assertion or caller-chosen `--until`.

On success the command appends measured output to the standard change doc,
`submodules/<sm>/docs/bee-<id>-<id>.md`, and commits the doc and plan together on
the local hive branch. The runner's clock (`Runner.Now`, injectable in tests)
determines the cycle time and next daily timestamp. The command does not push or
publish primary main; the enclosing runner publishes the session's commit.

The runner independently executes the registered check at the handoff gate; a
doc receipt alone never certifies success. A rejected handoff restores the
pre-pass cycle state and claim, preserving all earlier successful cycles and
the defer history. Fix the failure and run `task cycle` again. Heartbeat writes
and the CLI's final evidence commit use a shared, private-worktree advisory lock;
long checks allow heartbeat-only changes and keep their claims alive.

After publication the runner verifies that the accepted cycle and receipt really
landed, rather than treating a no-op publish as success. The receipt names the
measured code SHA explicitly: a later tracked-tip advance does not rewrite that
historical measurement. No successor task or automatic reopen is involved.

The task header records:

- `recurrence=24h`: explicit opt-in to daily cycles.
- `cycles=N`: the count of successful measured cycles.
- `last_cycle=<RFC3339>`: the last successful cycle's UTC time.
- `cycle_session=<session>`: the owning hive session that recorded that cycle.
- `not_before=<RFC3339>`: `last_cycle` plus 24 hours, gating the next selection.

The task stays TODO and releases its claim. An old cycle from another session is
not proof that the current pass succeeded. Dependencies remain independent:
**recurrence never satisfies a downstream DONE dependency**. Use a separate
finite acceptance/delivery task when a downstream task needs a DONE prerequisite.

## Defer Is Different

`beehive task defer <sm> <id> --until <time> --reason <why>` means a bounded
passive convergence wait: the effect has not appeared yet. It consumes the
existing capped `defers` budget. A successful cycle means the check actually
passed and schedules the next measurement; it neither consumes nor resets that
budget. Existing history, including `defers=12`, remains intact during adoption
and successful cycles. Recurrence is not permission to bypass a failed check or
to evade the defer cap. Diagnose/remediate failures, file buildable prerequisites,
and reserve human escalation for genuine human-only blockers.

Code changes still require normal NEEDS-REVIEW and reviewer approval. Do not use
`cycle` to self-approve code, reset the code checkout to hide a change, or replace
review with a successful measurement. Keep finite implementation work separate
from the standing measurement obligation.
