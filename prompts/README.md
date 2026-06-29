# System Prompts Index

Embedded in `cmd/honeybee` via `//go:embed prompts/*`. Selected deterministically by the runner.

| Prompt           | When                                              | Effect                                |
|------------------|---------------------------------------------------|---------------------------------------|
| AGENTS.md        | every honeybee start (system prompt)              | full honeybee protocol                |
| reconcile.md     | ROI.md newer than PLAN.md stamp (priority 0)      | fold ROI diff into PLAN, restamp      |
| bootstrap.md     | ROI present, PLAN absent (user prompt)            | decompose ROI -> PLAN, no impl        |
| select.md        | PLAN present (user prompt)                        | "select a task from PLAN.md and begin"|
| continue.md      | turn end, completion criteria unmet               | "continue"                            |

Runner contract: start one opencode session per honeybee (AGENTS.md system prompt + first user prompt:
bootstrap/select/reconcile), cwd = worktree. After each turn run the deterministic completion check; met ->
end session/exit, unmet -> send continue.md into the SAME session (context persists); cap 15 turns +
wall-clock then mark task for GC. The LLM never decides exit — the runner does. Model is opencode config.
