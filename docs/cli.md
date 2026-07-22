# CLI Reference (`beehive`)

Deterministic. No LLM. Edits files, commits, pushes.

```
beehive init <path>                       scaffold a beehive repo; git init + main when needed
beehive version                           print version

beehive submodule add <repo>              add submodule (dormant until ROI authored)
beehive submodule link <a> <b>            cross-submodule dependency link
beehive submodule plan rollback <plan-id> revert PLAN.md to a prior state

beehive secret add    -f <file.yaml>      create SECRETS.yaml.gpg
beehive secret update -f <file.yaml>      replace secrets
beehive secret edit                       decrypt to $EDITOR, re-encrypt

beehive honeybee start <path>             select + run one honeybee on a submodule
beehive task human <submodule> <task-id>  set NEEDS-HUMAN with --reason/--reason-file
beehive task add   <submodule> <task-id>  file a new TODO task (+ design doc) via convergence
beehive task block <submodule> <task-id>  add a dep (--on <dep>) to a TODO task + release its claim
beehive worktree add <submodule> <branch> create per-branch worktree
beehive worktree rm  <submodule> <branch> remove worktree
```

A new submodule is dormant (never selected) until a human authors its `ROI.md`.
The first honeybee then bootstraps `PLAN.md`. ROI.md is human-owned; honeybees
may never edit it (enforced by pre-receive hook). Frontend runs `beehived`
(default `:8955`).

`submodule sync`/`submodule remote`, `plan`, `task`, and `instruction update`
author commits directly on the primary `main` (no worktree). In a
remote-sharing hive that write MUST be sync-before-author/publish-after or it
can fork `main` and silently drop commits — see
[docs/main-convergence-protocol.md](main-convergence-protocol.md) for the write
recipe and which of these verbs already carry it.
