# ROI Reconcile Prompt (priority 0)

ROI.md changed since PLAN.md was last reconciled. Fold the intent changes into PLAN.md.

You are given the diff of ROI.md from the last-reconciled commit to HEAD (ROI.md scope only):
  git diff <last-reconciled-sha>..HEAD -- submodules/<sm>/ROI.md

- Read the diff. Update PLAN.md: add/modify/remove/retire tasks so the plan matches new intent.
- Preserve in-flight task status; retiring a task in flight -> NEEDS-REVIEW with a doc, not silent delete.
- **Preserve explicit standing-task recurrence and its history.** For intent that calls for daily
  measurement, author `recurrence=24h` with a real registered `Check:` in the reconciled plan.
  For an existing published TODO, use `beehive task recur <sm> <id> --every 24h`.
  This adds `recurrence=24h` without reopening, resetting
  counters/claims/scheduling, or changing deps. Preserve `cycles=N`, `last_cycle`, `cycle_session`,
  and `defers` (including `defers=12`); never reset a capped defer to simulate another successful day.
  `task cycle` measures clean tracked-tip code, commits actual output, and rearms TODO for 24h later;
  it is separate from bounded convergence defer and cannot replace review of code changes.
  Recurrence does not satisfy downstream DONE deps: retain finite delivery/acceptance prerequisites.
  Do not infer recurrence from names, prose alone, or defer exhaustion; see
  `docs/standing-task-recurrence.md`.
- Add design docs for new tasks. Tag dependencies. Size each task as a COHERENT VERTICAL SLICE — a
  self-contained deliverable (change plus its test) one isolated agent can hold in full — not the
  smallest possible fragment; a change split across micro-tasks that cannot see each other loses global
  coherence and thrashes review. Give every NEW task a `Context: <the why>` line carrying the parent ROI
  intent it serves, so the isolated implementer understands its slice's purpose without opening ROI.md.
- **Give every task a definition of done.** Lower the ROI's success criteria into each task's machine
  check: `beehive task add <sm> <id> --check '<cmd>'` (a command whose exit 0 asserts the task's REAL
  effect — curl+grep the endpoint, `kubectl rollout status`, pull the image by digest),
  `--verify-after-merge '<cmd>'` for an effect that only exists after merge, or `--check-none` (justified
  in the body) for a task with no observable effect. The runner GATES DONE on this check. Translate the
  operator's stated criteria into an executable check; do not invent a DoD the ROI never asked for. Run
  `beehive plan lint <sm>` to see which tasks still lack one. For EXISTING tasks, use
  `beehive task set-check <sm> <id> --check '<cmd>'` to backfill/correct a check, `beehive task reopen
  <sm> <id> --reason '<why>'` to return a false-DONE (a DONE whose real effect the check would fail) to
  TODO so it is re-driven, and `beehive task retarget-dep <sm> <id> --from <dep> --to <dep>` to fix a
  dangling/wrong dependency — all converge through the same non-racing protocol as the other task verbs.
- **Every check must be an APPROVED framework in `CHECKS.md`.** Each task's `Check:`/`Verify-After-Merge:`
  MUST MATCH a stub registered in `submodules/<sm>/CHECKS.md` — a real test runner / compile /
  build-pipeline or rollout status / integration|e2e|endpoint probe. A bare source-grep
  (`grep -q Symbol repo/...`, `test -f ...`) matches no stub and is REFUSED (it proves the code was
  written, never that the effect works). `CHECKS.md` is a beehive-layer file you OWN: if the target uses a
  framework not yet registered, ADD a stub for it (mirror an existing one), then point checks at it. Your
  reconcile does NOT complete while any open task's check matches no stub (or `CHECKS.md` is missing) —
  the completion gate reports the offending tasks. See `docs/checks-framework-registry.md`.
- **Cross-cutting intent needs an ACCEPTANCE TASK, not just per-leaf checks.** A per-leaf
  `Check:` cannot see the seam BETWEEN leaves — integration is the gap between tasks and no
  leaf owns it (the corpus disease: several "migrate X onto the shared Y" leaves each pass a
  real but narrow unit check while the live system ends up with disconnected instances and an
  empty user surface). When an ROI item implies a property spanning several tasks (one shared
  substrate, an end-to-end user-visible surface, a global invariant), emit — IN ADDITION to
  the leaves — one **terminal acceptance task** that `deps=` EVERY leaf in the cluster,
  inherits the cluster's TOP tier weight (a leaf-dependent successor otherwise starves in the
  lottery), carries an **`Invariant:` <one sentence>** body line naming the cross-cutting
  property, and whose `Check:` exercises that invariant against the RUNNING system (a live
  integration/e2e probe, an approved CHECKS.md framework) — not a per-crate unit test. Give
  any leaf that must PRESERVE a cross-cutting contract its own `Invariant:` line too; it is the
  reviewer's input (review judges against `Invariant:` + `Context:` + the live check, not the
  diff alone). When a probe of the current tree shows an ALREADY-DONE task's real effect is not
  integrated (its narrow check would pass but the invariant is false), `beehive task reopen
  <sm> <id> --reason "<evidence>"` it. See `skills/definition-of-done.md`.
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
- **Audit every open `NEEDS-HUMAN` task for staleness, unconditionally, every pass** (not gated on the
  ROI diff touching it). A manual audit (2026-08-23) found 6 of 10 standing escalations had already been
  resolved out-of-band (secret provisioned, dep merged, tool now installable in-sandbox, stale `Check:`
  field) yet stayed `NEEDS-HUMAN` forever because nothing re-checks a write-once escalation:
  1. Read the task's `Human-needed:` block; extract any machine-checkable claim (a command/`Check:` it
     says should now pass, a dep id, a store-key name, a path).
  2. Run any in-sandbox-runnable verification (same rules as `skills/self-resolve-before-escalating.md`).
     If it passes, or the stated prerequisite dep is now `DONE`, reopen:
     `beehive task reopen <sm> <task-id> --reason "<dated evidence>"`. This is the only legal exit from a
     stale `NEEDS-HUMAN` -- never flip `NEEDS-HUMAN -> DONE` directly; reopening returns it to `TODO` so a
     work pass re-drives and re-verifies it.
  3. NEVER auto-resolve `category=architecture` or `category=contradiction` (decisions, not re-checkable
     facts), and leave alone any item whose `Human-needed:` names a genuine operator-only action (a
     credential only the operator holds, host-root, an out-of-cluster op) with no machine-checkable proxy.
  4. Note which items were reopened (with evidence) and which stay blocked (and why) so the audit itself
     is auditable -- without relitigating the blocker in the escalation text.
  5. Do not re-run expensive builds solely for this audit; reuse the task's own configured check/timeout.
- Update the PLAN.md ROI stamp to the current ROI.md commit: `<!-- Beehive-ROI: <sha> -->`.
- NEVER edit ROI.md. Commit PLAN.md to main; conflict -> reselect.
- Do NOT implement tasks; reconciliation ends at a committed, restamped PLAN.md.
