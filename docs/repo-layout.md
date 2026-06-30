# Beehive Repo Layout

State + coordination live in the beehive repo. Target repos are submodules;
beehive data is never committed into them.

```
AGENTS.md                 generic operating guide + skills (managed)
HONEYBEE.md               honeybee runtime protocol = system prompt (managed)
BOOTSTRAP.md              install setup walkthrough (managed)
LOCALS.md                 site-specific ops facts (authored per install)
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
- **PLAN.md** tasks, state machine: TODO→NEEDS-REVIEW→{DONE|NEEDS-ARBITRATION};
  arbitration→{TODO|DONE}; reject overflow or explicit `beehive task human ... --reason` → NEEDS-HUMAN with a `Human-needed:` reason. Active work is session+heartbeat metadata, not an IN-PROGRESS status. Carries the ROI reconcile stamp.
- **docs/** terse, LLM-targeted change docs, one per branch+task.
- **ARTIFACTS.md / INFRASTRUCTURE.md** outputs + deploy/infra config.
- **SUBMODULE-LINKS.yaml** dependency edges; cycle-checked on every tag write.

## Submodules

Track a branch tip, not a pinned commit. ``beehive submodule sync`` fetches and
auto-advances the pointer (all honeybees want latest, hard-reset on force-push).
Dormant (no ROI.md) submodules are never selected. Worktrees branch off the
synced tip and are deleted on DONE+merge.
