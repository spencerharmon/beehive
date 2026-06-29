# Bootstrap Prompt (ROI.md present, PLAN.md absent)

Submodule has ROI.md, no PLAN.md. Bootstrap PLAN.md from intent.

- Decompose ROI into the smallest parallelizable, context-window-sized tasks.
- Tag dependencies between tasks; order interdependent steps via dependency tags.
- Status each new task TODO. Add a terse design doc per non-trivial task under docs/.
- Commit PLAN.md to main. Race-safe: if another honeybee bootstrapped first, conflict -> reselect.
- Do NOT begin implementation; bootstrapping ends at a committed PLAN.md.
