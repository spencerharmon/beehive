# ROI Reconcile Prompt (priority 0)

ROI.md changed since PLAN.md was last reconciled. Fold the intent changes into PLAN.md.

You are given the diff of ROI.md from the last-reconciled commit to HEAD (ROI.md scope only):
  git diff <last-reconciled-sha>..HEAD -- submodules/<sm>/ROI.md

- Read the diff. Update PLAN.md: add/modify/remove/retire tasks so the plan matches new intent.
- Preserve in-flight task status; retiring a task in flight -> NEEDS-REVIEW with a doc, not silent delete.
- Add design docs for new tasks. Tag dependencies. Rightsize for one context window.
- Cross-submodule dependencies are REAL tasks, never placeholders:
  - A dep is LOCAL (bare id -> a task in THIS PLAN.md) or CROSS-SUBMODULE (qualified `<other-sm>:<taskid>`,
    authorized by a registered link, satisfied only when that task is DONE). A bare dep naming no local
    task is unsatisfiable forever and silently blocks its task — NEVER emit a placeholder / "sentinel" /
    not-yet-existing-gate dep.
  - If a task here needs work owned by another submodule, author that work as a real task in the other
    submodule's PLAN.md (with its design doc under that submodule's docs/), register the link
    (`beehive submodule link <this> <other>` if not already linked), and depend on it as
    `deps=<other-sm>:<taskid>`.
  - Leave cross-repo-linked tasks alone: before retiring/renaming/rewriting any task, check whether
    another submodule's PLAN.md depends on it (`<this-sm>:<taskid>`). If so it is a cross-repo contract
    this ROI reconcile does not own — do not touch it.
  - A cross-repo intent conflict (this ROI contradicts what a dependent submodule needs from a linked
    task) -> `beehive task human <sm> <task-id> --category contradiction --reason "..."`, not a unilateral rewrite.
- **(Re)weight every task on the logarithmic (base-2) priority scale below** whenever the ROI diff
  changes the priority order or adds/retiers tasks. Selection is a weighted-random lottery, so weight
  must make high priority dominate while still letting lower tiers run:
  - Keyed to ROI's stated priority order, each step DOWN the order **halves** the weight. Enumerate the
    priority tiers top-to-bottom; top tier = `2^(T-1)` (T = tier count), each lower tier halves, floor 1.
    Tasks in one tier share its weight. A dependency gating a high task inherits the gated task's tier
    (never starve a P1 behind a low-weight prerequisite).
  - Current 8-tier order (P1 > P2 > correctness > completeness > configuration > aesthetics > chat-diff
    editor > deferred) -> `128, 64, 32, 16, 8, 4, 2, 1`. Emit `weight=N` in the header; omit only for the
    bottom (weight=1) tier. Re-emit weights for existing tasks when their tier moved.
- Update the PLAN.md ROI stamp to the current ROI.md commit: `<!-- Beehive-ROI: <sha> -->`.
- NEVER edit ROI.md. Commit PLAN.md to main; conflict -> reselect.
- Do NOT implement tasks; reconciliation ends at a committed, restamped PLAN.md.
