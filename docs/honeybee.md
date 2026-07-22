# Honeybee Operation

A honeybee is one process working one task to completion, then exiting. There is no controller — anything that can run a command (cron, script, CI, k8s job) can start honeybees. The swarm coordinates only through git merges. See `docs/orchestration.md` for a scheduled-swarm example (systemd timer).

## Start

```sh
beehive honeybee start submodules/web-frontend
```

The runner (deterministic, no LLM) does, before launch:

1. Weighted-random submodule pick (dormant submodules excluded).
2. ROI reconcile priority: if ROI.md commit differs from PLAN.md's `<!-- Beehive-ROI: <sha> -->` stamp, reconcile wins.
3. Else by priority: GC, arbitration, review, main work. `NEEDS-HUMAN` is excluded.
4. Weighted-random over candidates with dependency tags satisfied.
5. For work tasks, create per-branch code worktree at `submodules/<sub>/worktrees/<branch>` (never shared checkout).
6. Stamp a claim on the PLAN task with `session=<id>` and `heartbeat=<RFC3339>`.

## Turn loop

One opencode session per honeybee: `HONEYBEE.md` (read from the repo root) as system prompt, first user prompt = bootstrap/reconcile/select/review/arbitrate context. cwd = beehive repo root. After each turn the runner checks completion deterministically. The runner only ever verifies adherence to the PROTOCOL (status transitions, the change doc, that the agent's work is committed) — it never runs the target's tests or judges code correctness; that is the honeybees' job (see `docs/runner-protocol-vs-correctness.md`). Per kind:

- Work task: PLAN status transitioned to `NEEDS-REVIEW`, `NEEDS-ARBITRATION`, `DONE`, or `NEEDS-HUMAN`. Normal implementation/review handoff also requires the change doc named `<branch>-<taskid>...` under `submodules/<sub>/docs/`; explicit `NEEDS-HUMAN` escalation does not require a change doc because the `Human-needed:` reason is the handoff artifact. On the `TODO -> NEEDS-REVIEW` handoff the runner also gates on a language-agnostic protocol check: the code worktree must have no uncommitted work (`git status --porcelain` empty), so the agent's change is actually committed for the merge to carry — dirty tree ⇒ blocked, commit-forward same session.
- Review task: status leaves `NEEDS-REVIEW`.
- Arbitration task: status leaves `NEEDS-ARBITRATION`.
- Bootstrap/reconcile: expected PLAN artifact/stamp appears.

Met → exit. Unmet → send `continue` to the same session (context persists). Cap: `max_turns` + wall-clock; exceeded → leave claim for stale-claim GC.

## Heartbeat & GC

There is no `IN-PROGRESS` status. A task is active when it carries `session=<id>` plus a fresh `heartbeat`. The runner re-stamps heartbeat each turn. Stale claim (> `ttl_minutes`) becomes a GC candidate. GC either finishes abandoned work or releases/resets the task.

## Claim race

Claim = commit to main marking the item with this honeybee's session/heartbeat. After publish, runner pulls main and verifies that the session still owns the task. If another session won, this honeybee abandons and reselects. Merge is the lock.

## Human-needed escalation

When a honeybee hits a concrete blocker that cannot be resolved honestly without operator input, it must use the first-class command:

```sh
beehive task human <submodule> <task-id> --reason "specific blocker and exact input needed"
```

This command:

- sets task status to `NEEDS-HUMAN`;
- records `Human-needed: <reason>` in the task body;
- clears session/heartbeat;
- commits the PLAN.md change.

Use this for real blockers: credentials, real configuration/calibration, missing upstream API, contradictory spec, or user-visible contract choice. Do not use it for normal tradeoffs or implementation uncertainty; choose a workable internal path and continue.
