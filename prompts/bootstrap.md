# Bootstrap Prompt (ROI.md present, PLAN.md absent)

Submodule has ROI.md, no PLAN.md. Bootstrap PLAN.md from intent.

- Decompose ROI into COHERENT VERTICAL SLICES — each task a self-contained deliverable one agent can
  hold in full (the change plus its test, across the few files it spans), NOT the smallest possible
  fragment. An isolated honeybee sees only its own task, so a change split across many micro-tasks that
  cannot see each other loses global coherence, breaks at the seams, and thrashes review. Prefer fewer,
  larger, independently-mergeable slices; split further ONLY when the parts are genuinely independent or
  a slice would exceed one agent's grasp. Parallelizable is a bonus, not the primary axis — coherence is.
- **Give every task a `Context:` line** carrying the PARENT INTENT: one or two sentences on the ROI goal
  this task serves and WHY, so the isolated implementer understands the point of its slice without
  opening ROI.md (which it may not). Put it in the task body: `Context: <the why>`.
- Tag dependencies between tasks; order interdependent steps via dependency tags.
- Cross-submodule dependencies are REAL tasks, never placeholders. A dep is LOCAL (bare id -> a task in
  THIS PLAN.md) or CROSS-SUBMODULE (qualified `<other-sm>:<taskid>`, authorized by a registered link,
  satisfied only when that task is DONE). A bare dep naming no local task is unsatisfiable forever and
  silently blocks its task — NEVER emit a placeholder / "sentinel" / not-yet-existing-gate dep. If a task
  needs work owned by another submodule, author that work as a real task in the other submodule's
  PLAN.md (with its design doc under that submodule's docs/), register the link
  (`beehive submodule link <this> <other>`), and depend on it as `deps=<other-sm>:<taskid>`.
- Status each new task TODO. Add a terse design doc per non-trivial task under docs/.
- **Standing daily measurement is explicit recurrence, not repeated defer.** When ROI calls for
  ongoing daily checks, give the TODO a real registered `Check:` and an explicit
  `recurrence=24h` header token. For an existing published TODO, the supported adoption command is
  `beehive task recur <sm> <id> --every 24h`. Do not infer recurrence from names or retry history.
  A successful `beehive task cycle <sm> <id>` keeps it TODO, records `cycles=N`, `last_cycle`,
  `cycle_session`, and schedules `not_before` 24h later. It preserves `defers` (even `defers=12`).
  Cycles measure clean tracked-tip code; code changes still need review. Recurrence does NOT satisfy
  downstream DONE deps: model finite delivery/acceptance separately from ongoing measurement.
  See `docs/standing-task-recurrence.md`; convergence waits remain bounded `task defer` operations.
- **Give every task a definition of done.** Lower the ROI's success criteria into a machine check per
  task: `beehive task add <sm> <id> --check '<cmd>'` (exit 0 asserts the task's REAL effect),
  `--verify-after-merge '<cmd>'` for a merge-gated effect, or `--check-none` (justified in the body) for
  a task with no observable effect. The runner GATES DONE on it. Run `beehive plan lint <sm>` to confirm
  coverage. Translate the operator's stated criteria; do not invent a DoD the ROI never asked for.
- **Every check must be an APPROVED framework in `CHECKS.md`.** Each task's `Check:`/`Verify-After-Merge:`
  MUST MATCH a stub in `submodules/<sm>/CHECKS.md` — a real test runner / compile / build-pipeline or
  rollout status / integration|e2e|endpoint probe. A bare source-grep matches no stub and is REFUSED.
  `CHECKS.md` is a beehive-layer file you OWN: CREATE it for this target (register the frameworks it uses
  — mirror another submodule's CHECKS.md), then point every check at a stub. Bootstrap does NOT complete
  while any open task's check matches no stub (or `CHECKS.md` is missing). See
  `docs/checks-framework-registry.md`.
- **Cross-cutting intent needs an ACCEPTANCE TASK.** A per-leaf `Check:` cannot see the seam
  between leaves — integration is the gap between tasks and no leaf owns it. When an ROI item
  implies a property spanning several tasks (one shared substrate, an end-to-end user-visible
  surface, a global invariant), emit — in addition to the leaves — one **terminal acceptance
  task** that `deps=` every leaf, inherits the cluster's TOP tier weight (a leaf-dependent
  successor otherwise starves in the lottery), carries an **`Invariant:` <one sentence>** body
  line naming the cross-cutting property, and whose `Check:` exercises that invariant against
  the RUNNING system (a live integration/e2e probe on an approved CHECKS.md framework) — not a
  per-crate unit test. Give any leaf that must preserve a cross-cutting contract its own
  `Invariant:` line too (the reviewer judges against it). See `skills/definition-of-done.md`.
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
