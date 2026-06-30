# Bootstrap Prompt (ROI.md present, PLAN.md absent)

Submodule has ROI.md, no PLAN.md. Bootstrap PLAN.md from intent.

- Decompose ROI into the smallest parallelizable, context-window-sized tasks.
- Tag dependencies between tasks; order interdependent steps via dependency tags.
- Status each new task TODO. Add a terse design doc per non-trivial task under docs/.
- **Weight each task on a logarithmic (base-2) priority scale (see "Weighting").**

## Weighting (logarithmic, base-2)

Weights drive a weighted-random selection lottery (a task's pick probability is
its weight over the sum of all selectable weights), so the scale must make high
priority *dominate* while still letting lower tiers run.

Use a logarithmic (base-2) scale keyed to ROI's stated priority order: each step
DOWN the priority order **halves** the weight. Enumerate ROI's priority tiers
top-to-bottom; the top tier gets `2^(T-1)` where T is the number of tiers, and
each lower tier halves, never below 1. Tasks in the same tier share that tier's
weight. A dependency that gates a high task inherits the gated task's tier (do
not starve a P1 behind a low-weight prerequisite).

Example for ROI's current 8-tier order (P1 > P2 > correctness > completeness >
configuration > aesthetics > chat-diff editor > deferred):
`128, 64, 32, 16, 8, 4, 2, 1`. Emit the integer in the task header
`<!-- ... weight=N -->`; omit it only for the bottom (weight=1) tier.
- Commit PLAN.md to main. Race-safe: if another honeybee bootstrapped first, conflict -> reselect.
- Do NOT begin implementation; bootstrapping ends at a committed PLAN.md.
