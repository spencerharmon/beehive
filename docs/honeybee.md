# Honeybee Operation

A honeybee is one process working one task to completion, then exiting. There is
no controller — anything that can run a command (cron, script, CI, k8s job) can
start honeybees. The swarm coordinates only through git merges.

## Start

```
beehive honeybee start submodules/web-frontend
```

The runner (deterministic, no LLM) does, before launch:

1. **Weighted-random** submodule pick (dormant submodules excluded).
2. **ROI reconcile (priority 0):** `scripts/roi-changed.sh` — if ROI.md commit
   differs from PLAN.md's `<!-- Beehive-ROI: <sha> -->` stamp, that's the task.
3. Else by priority: **GC > arbitration > review > main**. NEEDS-HUMAN excluded.
4. Weighted-random over candidates with all dependency tags DONE.
5. Per-branch worktree at `submodules/<sub>/worktrees/<wt>` (never shared checkout).

## Turn loop

One opencode session per honeybee: `AGENTS.md` as system prompt, first user
prompt = bootstrap/reconcile/select. cwd = worktree. After each turn the runner
checks completion: PLAN status transitioned, doc file named `<branch>-<taskid>`
present, branch/merge correct, in-progress timestamp removed. Met → exit. Unmet
→ send `continue` to the **same** session (context persists). Cap: 15 turns +
wall-clock; exceeded → mark for GC. The LLM never decides exit; the runner does.

## Heartbeat & GC

IN-PROGRESS carries a heartbeat re-stamped each turn. Stale (>1h, `ttl_minutes`)
→ GC candidate. GC either finishes the branch or resets to TODO. Worktree
deleted on DONE+merge.

## Claim race

Claim = commit to main marking the item in-progress. After push, re-pull and
assert your timestamp won; else abandon and reselect. Merge is the lock.
