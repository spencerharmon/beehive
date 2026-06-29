# Beehive Repo Layout

State + coordination live in the beehive repo. Target repos are submodules;
beehive data is never committed into them.

```
AGENTS.md                 honeybee protocol (system prompt)
INFRASTRUCTURE.md         top-level infra (optional)
SECRETS.yaml.gpg          global secrets, one encrypted yaml doc (optional)
SUBMODULE-LINKS.yaml      cross-submodule dependency graph (optional)
submodules/
  web-frontend/
    repo/                 the submodule checkout (managed by scripted submodule update)
    worktrees/            per-branch honeybee worktrees (isolated writes)
    docs/                 change docs, named <branch>-<taskid>
    ROI.md                record of intent — human-owned, honeybees never write
    PLAN.md               tasks; carries <!-- Beehive-ROI: <sha> --> stamp
    INFRASTRUCTURE.md
    ARTIFACTS.md
    SECRETS.yaml.gpg      per-submodule secrets (optional)
    SUBMODULE-LINKS.yaml
```

## Files

- **ROI.md** intent; sole human-owned doc; pre-receive hook rejects honeybee diffs.
- **PLAN.md** tasks, state machine: TODO→IN-PROGRESS→NEEDS-REVIEW→{DONE|NEEDS-ARBITRATION};
  arb→{TODO|DONE}; >3 rejects→NEEDS-HUMAN. Carries the ROI reconcile stamp.
- **docs/** terse, LLM-targeted change docs, one per branch+task.
- **ARTIFACTS.md / INFRASTRUCTURE.md** outputs + deploy/infra config.
- **SUBMODULE-LINKS.yaml** dependency edges; cycle-checked on every tag write.

## Submodules

Track a branch tip, not a pinned commit. ``beehive submodule sync`` fetches and
auto-advances the pointer (all honeybees want latest, hard-reset on force-push).
Dormant (no ROI.md) submodules are never selected. Worktrees branch off the
synced tip and are deleted on DONE+merge.
