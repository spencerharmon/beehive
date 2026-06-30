# System Prompts Index

Embedded in the binaries via `//go:embed`. The runtime prompts are selected
deterministically by the runner; the instruction docs are installed to the repo
root (see `internal/instruct`) and edited there.

## Instruction docs (installed to the repo root; binary holds the defaults)

| File             | Role                                                                 |
|------------------|----------------------------------------------------------------------|
| HONEYBEE.md      | honeybee runtime protocol; the runner reads it from the repo root and injects it as the system prompt (falls back to the embedded default if absent) |
| AGENTS.md        | generic operating guide + skills for any agent; NOT the runtime prompt |
| bootstrap_guide.md (→ BOOTSTRAP.md) | install setup walkthrough |

`beehive init` installs these; `beehive instruction update [--clobber]` refreshes
them to the binary's defaults, backing up operator-customized copies.

## Runtime prompts (selected per pass by the runner)

| Prompt           | When                                              | Effect                                |
|------------------|---------------------------------------------------|---------------------------------------|
| reconcile.md     | ROI.md newer than PLAN.md stamp (priority 0)      | fold ROI diff into PLAN, restamp      |
| bootstrap.md     | ROI present, PLAN absent (user prompt)            | decompose ROI -> PLAN, no impl        |
| select.md        | PLAN present (user prompt)                        | "select a task from PLAN.md and begin"|
| review.md / arbitrate.md | task is NEEDS-REVIEW / NEEDS-ARBITRATION   | judge existing work, don't re-implement|
| continue.md      | turn end, completion criteria unmet               | "continue"                            |

Runner contract: start one opencode session per honeybee (HONEYBEE.md system
prompt + first user prompt: bootstrap/select/reconcile/review/arbitrate), cwd =
worktree. After each turn run the deterministic completion check; met -> publish +
exit, unmet -> send continue.md into the SAME session (context persists); cap at
MaxTurns + wall-clock then mark task for GC. The LLM never decides exit — the
runner does. Model is opencode config.
