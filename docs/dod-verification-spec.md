# Definition-of-Done verification: the check contract

Status: **specified; core landed** (see "Rollout" for what has landed vs planned).

## Motivation

The swarm's loop was open: `ROI (intent) → PLAN → execute → DONE`, where "DONE"
was only ever checked against a *change doc at review time* — never re-checked
against reality. Every task could be individually DONE while the aggregate intent
was unmet. The canonical failure: `jellyfin:zuul-image-build-publish` was marked
DONE (its config commit was real and reviewed) while its own acceptance bar —
"image pullable by digest" — was false; the image never existed, the endpoint
stayed down, and nothing in the protocol ever curled it.

Two independent defects produced that:

1. **A definition of done that lived only in prose.** The DoD ("pullable by
   digest") was written in the task text; nothing *enforced* it, so a runner
   finalize closed the task on a reviewed sibling commit while the DoD was unmet.
2. **A dangling dependency.** `flux:phantom-library-bluegreen-repin-gitea-images`
   depended on `jellyfin:jellyfin-image-build` — a task ID that does not exist —
   which both faked a "blocked" yield and wedged the task forever, because a
   nonexistent dep is treated as "not DONE → blocked".

This spec closes both by making the **definition of done a machine-checkable
contract the deterministic runner enforces**, and by refusing to let any writer
introduce a dangling dependency.

## Non-goals

- **This is not a monitoring system.** It converges intent→reality *once,
  verifiably, per change*. Continuous drift detection (cert expiry, upstream
  breakage after DONE) is a *deployed capability* an ROI can ask for and
  honeybees build like any other work — never a behavior the swarm simulates by
  re-running probes forever.
- **The runner does not reason.** Observation is a deterministic command +
  expected result. Remediation (when a check fails) is a normal work pass,
  spawned only on a real failure.

## Principles

- **Enforcement lives in the deterministic layer; prompts only teach.** The DoD
  cannot live only in prompts or agents forget it (exactly how jellyfin failed).
  The PLAN parser + runner gate enforce; the generated prompts explain.
- **Gate the DONE *state*, not a pass kind.** Wherever DONE is written — review
  approve, arbitration, a misbehaving work agent, an interrupted-review finalize
  — the check must pass. No actor routes around it.
- **Absence of a check is an explicit, reviewable declaration** (`check=none`,
  mirroring `commits=none`), never a silent gap.
- **Observation is cheap; remediation is expensive.** Steady state (things work)
  costs ~zero agent tokens.

## State-transition model (authoritative)

A **work** pass acts on a task that entered the pass `TODO`. Its permitted
terminal transitions are exactly:

| From | To | Meaning |
|------|-----|---------|
| TODO | NEEDS-REVIEW | work finished; awaits review (the forward path) |
| TODO | NEEDS-ARBITRATION | work agent flags a dispute for arbitration (rare) |
| TODO | TODO (self-defer / dep-yield) | not ready to progress; release the claim; the selector holds it until [a real dep is DONE] or [`not_before` elapses]. **A short doc is required** and any blocking dep **must exist**. |
| TODO | NEEDS-HUMAN | concrete blocker with a category + reason |

**`TODO → DONE` is forbidden.** A work pass may not set DONE. DONE is entered
only by:

- `NEEDS-REVIEW → DONE` (review approve),
- `NEEDS-ARBITRATION → DONE` (arbitration sides with implementer),
- a **successor check task**'s own `TODO → NEEDS-REVIEW → DONE`, or
- the interrupted-review finalize, which is a `NEEDS-REVIEW → DONE` recognition
  (gated on `status == NEEDS-REVIEW` + reachability), never `TODO → DONE`.

Entering DONE by **any** path requires the task's declared **check** to pass, or
an explicit `check=none`.

## Task schema

Commands are multi-line and cannot live in the header comment (it is parsed with
`strings.Fields`), so they are **body fields** (mirroring the existing
`Human-needed:` body field). The justified-absence flag is a **header token**
(mirroring `commits=none`).

- **`Check:` body field** — the DoD command whose exit status is the machine
  definition of done. Multi-line: the `Check:` line plus following non-blank
  lines, terminated by a blank line (same span rule as `Human-needed:`). Exit 0 =
  satisfied. Parsed into `plan.Task.Check`.
- **`Verify-After-Merge:` body field** — the DoD command whose effect only exists
  *after* the change is merged (GitOps and anything the reviewer lands). Its
  presence marks the task's effect **merge-gated**: the work agent cannot verify
  it (the merge does not yet exist at NEEDS-REVIEW), so the live-effect DoD is
  carried by a **successor check task** the runner spawns at merge. Parsed into
  `plan.Task.VerifyAfterMerge`.
- **`check=none` header token** — an explicit, justified declaration that this
  task has no machine-checkable DoD. The justification is the adjacent body prose
  (review-enforced). Parsed into `plan.Task.CheckNone`. Mutually exclusive with a
  `Check:` field (a plan carrying both is a parse defect).
- **`not_before=<RFC3339>` header token** — *already exists* — the wall-clock
  eligibility gate the self-defer transition sets/refreshes.

`Task.CheckDeclared()` = `Check != "" || CheckNone`. A task that reaches a state
requiring a decision without one is a defect (parse-rejected / lint-flagged /
gate-refused, per surface).

## Runner behavior

1. **Selection eligibility** (already present for `not_before`): a TODO task with
   a future `not_before` is not selectable; a task with a dep that is not DONE is
   not selectable. **New:** a dep whose target *does not exist* is a dangling dep
   — see (5).
2. **Post-select context injection**: the runner runs the task's `Check` once at
   pass start (`checkGroundTruth`) and injects the result (exit + output tail) into
   the agent brief as a "Ground truth" section, so the agent starts from reality
   instead of re-deriving state. Same command and execution surface as the DONE
   gate — no new environment coupling beyond the gate that already runs the check.
3. **DONE gate** (in `verifyGate`, invariant 5): when the accepted terminal
   handoff **enters DONE**, the runner runs `Check` via the `runVerify` seam. Pass
   (exit 0) → DONE stands. Non-zero → the handoff is refused and the agent gets a
   commit-forward prompt (same mechanism as the other gate invariants). `CheckNone`
   → the gate is inapplicable (the absence was declared). Infra failure to run the
   check → fail-closed (block completion).
4. **Disallow `TODO → DONE`**: `workChecklist` no longer accepts `Done` as a work
   terminal, and `gatedHandoff` no longer lists Work→Done. A work pass that left
   the task DONE does not complete and is told to set NEEDS-REVIEW instead.
5. **Dangling-dep refusal** — at two layers, because writers differ:
   - **Parse / lint**: `Plan.DanglingDeps` reports any task whose local dep names
     no task in the plan. `beehive plan lint` surfaces cross-submodule dangles via
     the link graph (the same existence check `beehive task block` already does).
     This catches reconcile-authored phantoms (the actual jellyfin dep).
   - **Yield-completion**: `taskYieldedBlocked` accepts a yield only if every
     blocking dep is a **real, existing** task that is simply not-yet-DONE; a
     phantom dep makes the yield invalid and the pass fails loudly rather than
     silently spinning to idle-timeout.
6. **Self-defer**: `taskYieldedBlocked` accepts "TODO gated by a future
   `not_before`" as a legitimate yield, bounded by a defer count (`defers=N`
   header token) against `plan.MaxDefers` — past the bound the yield fails loudly so
   the next pass escalates to NEEDS-HUMAN rather than deferring forever. The
   `beehive task defer` verb is its sanctioned atomic form.
7. **Successor spawn**: when a task carrying `Verify-After-Merge` reaches DONE,
   the runner AUTO-spawns a successor CHECK task (`spawnMergeVerifySuccessor`):
   id `<taskid>-verify-after-merge`, a normal TODO whose `Check:` IS that command,
   depending on the now-DONE original and carrying no `Verify-After-Merge` (so it
   never recurses). It is committed to the submodule's PLAN.md on the completing
   pass's working-tree HEAD, so the DONE flip and the successor publish to main
   together; if that publish conflicts, `publishWithResolution` routes it back to
   the agent like any other merge. Deterministic + idempotent — the merge-gated
   DoD can never be forgotten. (Operator ruling: runner-auto, not agent-filed.)

## Merge ordering and blue/green

If an effect requires the merge, no in-session check can pass (the work agent ends
at NEEDS-REVIEW, before the merge exists). Two cases:

- **Effect is live the moment the agent acts** (it directly restarted a pod,
  triggered a build): self-defer on the same task (TODO + `not_before`), re-check
  on re-selection.
- **Effect requires the merge**: the work task's DoD shrinks to "correct +
  merges"; the live-effect DoD (`Verify-After-Merge`) moves to a runner-spawned
  successor check task, eligible only after the merge.

Blue/green makes merge-gated verification tractable: a change to the **inactive**
color merges freely (not load-bearing), the successor check verifies the inactive
color (with `not_before` for convergence), and a separate cutover task — gated on
that check — promotes it. Any strategy with a staging surface generalizes the
same way; without one, the check is a post-cutover gate whose failure branch is a
rollback task.

## CLI surface

*(Landed)*

- `beehive task add --check '<cmd>' [--verify-after-merge '<cmd>'] [--check-none] [--not-before <t>]`
  — attach a definition of done (or a justified absence) when filing a task.
- `beehive task defer <sm> <id> --until <t> [--reason ...]` — the sanctioned
  atomic convergence-wait self-defer (sets `not_before`, increments the bounded
  `defers` counter, releases the claim). `<t>` is RFC3339 or a duration (`30m`, `2h`).
- `beehive task check <sm> <id>` — run a task's `Check:` ad hoc (author/debug; let a
  reviewer run it without a full pass). Read-only; exits non-zero on failure.
- `beehive plan lint <sm>` — report tasks missing a check where `check=none` is not
  declared (warn; `--strict` to error), dangling local deps (error), and defer-cap
  breaches (error). The deterministic replacement for a "periodic efficacy-
  evaluation pass".

## Review contract

Review must catch two distinct failures:

1. **Missing** — a live-effect task with `check=none` and no honest reason → reject.
2. **Weak / wrong** — a check that passes on a 404 page, greps the wrong string,
   or hits the wrong host. This is the check *lying about the definition of done*
   — the same disease one layer down. The reviewer must **run the check and read
   what it asserts**, not merely confirm a `Check:` line exists.

Consider making "the reviewer recorded the check's live result" part of the review
completion predicate, so a reviewer cannot approve a check they never executed.

## The intent→check chain

Agents translate intent, they do not invent DoDs:

`operator/editor writes ROI success criteria → reconcile renders them into
per-task Check:/Verify-After-Merge: → work executes → runner gates DONE → review
scrutinizes fidelity`.

## Migration

Existing tasks carry no checks. Grandfather already-DONE tasks (do not retro-gate
history). A reconcile pass backfills `Check:` / `check=none` onto open tasks;
`beehive plan lint` reports the backlog so coverage is visible as it climbs.

## Rollout

- **Landed:** the state-transition model above; schema fields (`Check`,
  `VerifyAfterMerge`, `CheckNone`, `defers`) with parse/serialize/validate;
  disallow `TODO → DONE`; the DONE check gate in `verifyGate`; dangling-dep refusal
  (`Plan.DanglingDeps` + yield-completion existence check); doc-required-on-yield;
  `not_before` self-defer acceptance in the yield branch + defer-cap (`MaxDefers`);
  the CLI verbs (`task add --check/--verify-after-merge/--check-none/--not-before`,
  `task defer`, `task check`, `plan lint`); the generated-prompt edits
  (HONEYBEE / review / reconcile / bootstrap); **post-select check context
  injection** (`checkGroundTruth`); **runner auto-spawn of the `Verify-After-Merge`
  successor check task** (`spawnMergeVerifySuccessor`).
- **Planned (sequenced follow-ups):** none of the core mechanism remains; the one
  open runner refinement is the review-ran-check completion predicate below.

## Open decisions (operator to rule)

- **Review-ran-check as a hard completion predicate** — make "the reviewer recorded
  the check's live result in the review doc" a runner-checked review-completion
  predicate, so a reviewer cannot approve a check they never executed.
- **Probe sandbox** — which read-only verbs + which credentials a `Check:` may use
  when the runner runs it against the live environment (the DONE gate AND the
  pass-start injection execute task-authored commands with the runner's
  environment). Lives with LOCALS/ROI per install.

*Resolved:* successor authorship — **runner-auto** (operator ruling); if the
deterministic publish cannot merge the spawned successor, the existing
conflict-resolution path hands it to the agent.

## Enforcement-point map (keep current)

| Concern | Enforced at | Code |
|---------|-------------|------|
| DONE requires a passing check (or `check=none`) | handoff gate | `verifyGate` invariant 5, `internal/swarm/verify.go` |
| work may not set DONE | completion predicate + gate | `workChecklist`, `gatedHandoff` |
| no dangling dep (any writer) | parse / lint | `Plan.DanglingDeps`, `beehive plan lint`, `beehive task block` |
| no dangling dep (yield) | yield-completion | `taskYieldedBlocked` |
| yield ships a doc | completion predicate | `workChecklist` |
| DoD check run at pass start (ground truth) | brief build | `checkGroundTruth` (`internal/swarm/verify.go`) |
| Verify-After-Merge successor auto-spawn | completion, pre-publish | `spawnMergeVerifySuccessor` (`internal/swarm/swarm.go`) |
| check / verify_after_merge / check=none schema | parse / serialize | `internal/plan/plan.go` |
| convergence-wait self-defer + bound | yield-completion + counter | `taskYieldedBlocked`, `plan.MaxDefers`, `Task.Defer` |
| DoD/dep hygiene surfaced deterministically | CLI | `beehive plan lint` (`cmd/beehive/cmd_plan.go`) |
| author a task's DoD | CLI | `beehive task add --check/--verify-after-merge/--check-none`, `beehive task defer`, `beehive task check` (`cmd/beehive/cmd_task.go`) |
