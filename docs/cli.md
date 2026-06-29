# CLI Reference (`beehive`)

Deterministic. No LLM. Edits files, commits, pushes.

```
beehive init <path>                       scaffold a beehive repo
beehive version                           print version

beehive submodule add <repo>              add submodule (dormant until ROI authored)
beehive submodule link <a> <b>            cross-submodule dependency link
beehive submodule plan rollback <plan-id> revert PLAN.md to a prior state

beehive secret add    -f <file.yaml>      create SECRETS.yaml.gpg
beehive secret update -f <file.yaml>      replace secrets
beehive secret edit                       decrypt to $EDITOR, re-encrypt

beehive honeybee start <path>             select + run one honeybee on a submodule
beehive worktree add <submodule> <branch> create per-branch worktree
beehive worktree rm  <submodule> <branch> remove worktree
```

A new submodule is dormant (never selected) until a human authors its `ROI.md`.
The first honeybee then bootstraps `PLAN.md`. ROI.md is human-owned; honeybees
may never edit it (enforced by pre-receive hook). Frontend runs `beehived`
(:8080).
