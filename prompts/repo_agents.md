# AGENTS.md — beehive repo (local rules)

This file marks the directory as a beehive repo and holds **repo-local** rules only.

The authoritative honeybee protocol (selection, claim/heartbeat, GC/arbitration/
review, worktree + handoff flow, completion criteria) is **not** here: it ships with
the `honeybee` binary and is injected as the system prompt at runtime. That keeps the
protocol current as the tool evolves — a repo initialized by an older `beehive` would
otherwise freeze a stale copy here. If anything below conflicts with the injected
system prompt, the system prompt wins.

## Absolute, never-violate rules
- NEVER edit `ROI.md`. It is the human-owned record of intent (also enforced by a hook).
- All code edits happen in your task's worktree under `submodules/<sm>/worktrees/<branch>/`.
  Never write the shared `submodules/<sm>/repo` checkout.
- No shortcuts: compute real values; no placeholders, swallowed errors, or fake "done".

## Project-specific notes
(Add rules specific to THIS beehive repo here — house conventions, review bar,
submodule quirks. Keep it terse and LLM-targeted. Leave the protocol to the binary.)
