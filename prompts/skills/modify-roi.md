# Skill: Modify an ROI

> Use when: a target's intent must change — add, drop, reprioritize, or reword
> work for `submodules/<name>/`.

`ROI.md` is the human/operator-owned record of intent. It is hook-protected: a
**honeybee** (autonomous pass, `BEEHIVE_HONEYBEE=1`) that tries to commit an edit to
any `ROI.md` is blocked. Operator-directed edits ARE allowed — the operator
directly, the beehived ROI/generic editor, or an agent acting on the hive repo when
the operator directs it.

## NEVER commit an ROI edit on the live `main` tree

The live `main` working tree is **derived state, not an editing surface** — a raw
`git commit` there becomes a stray diverging head the swarm never pushes, which
wedges every future `git pull --ff-only`. Use one of the paths below; both converge
to `main` through the sanctioned publish sequence, never the live checkout.

## Procedure

Pick ONE path.

**Path A — the `beehived` ROI/generic editor UI (preferred).** Open the target's
`ROI.md` in the beehived editor and save. It commits and publishes through the
sanctioned flow for you; you never touch the live checkout or the CLI.

**Path B — `beehive edit` (CLI, no LLM needed).** One deterministic call runs the
whole worktree -> write -> commit -> publish -> cleanup sequence and shares the
exact convergence path Path A uses:

```
beehive edit submodules/<name>/ROI.md --content-file <new-content> \
    [--message "<commit message>"]
```

Read from stdin instead of `--content-file` by piping content in. A whole-file
deletion of `ROI.md` (a human-owned file) is refused unless you pass
`--confirm-delete`. On success the edit is already on `main` — no further worktree,
push, or cleanup step is needed or should be attempted.

In **both** paths: do **not** touch `PLAN.md`. The next **reconcile** pass detects
that `ROI.md`'s head is newer than the `<!-- Beehive-ROI: <sha> -->` stamp in
`PLAN.md`, folds the diff into the plan, re-weights tasks on the logarithmic priority
scale, and restamps. Hand-editing `PLAN.md` to "apply" the change races that pass and
is overwritten.

Then verify on the next reconcile that the new stamp matches `ROI.md`'s head and the
intended tasks appear with the expected priorities.

## Rules

- **Never `git commit` an ROI edit on the primary `main` working tree directly** —
  and never edit the live `submodules/<name>/ROI.md` in place. Use Path A or Path B
  above.
- `beehive edit` already resolves the correct publish target (a trusted remote's
  `main` when one exists, the local `main` otherwise) and cleans up its own worktree
  — do not add a manual push, worktree, or cleanup step around it.
- Never edit an `ROI.md` **as a honeybee** (an autonomous pass — the hook blocks it).
  Operator-directed edits are allowed: a hive agent acting on operator instruction,
  or the beehived editor.
- Never hand-edit `PLAN.md` — reconcile owns it.
