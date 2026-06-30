# opencode Setup

The honeybee swarm is provider-agnostic: no LLM SDK is linked. Each honeybee
drives an [opencode](https://opencode.ai) session via its server API. Model
choice is opencode config, not beehive code — cloud API, local model, or
cluster, all the same to beehive.

## Install opencode

Install per upstream docs; ensure `opencode` is on `PATH` (or set `agent_cmd`
in `/etc/beehive/config.yaml`).

## Configure the model

opencode config under `/etc/beehive` selects the provider/model. One session
spans a honeybee's lifetime so context persists across turns; pick a model whose
context window fits a rightsized task (tasks are sized to avoid compaction).

## Runner contract

- system prompt: beehive `HONEYBEE.md` (repo root; falls back to the binary default)
- first user prompt: `bootstrap.md` | `reconcile.md` | `select.md`
- cwd: the per-branch worktree
- between turns: `continue.md` into the same session
- caps: 15 turns + wall-clock → mark for GC

The runner decides exit, not the model. See `docs/honeybee.md` and
`prompts/README.md`.
