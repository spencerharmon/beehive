# Formal spec ↔ code ↔ test mapping

Companion to the TLA+ specs under `specs/`. TLA+ verifies the *design*; it does
**not** verify that the Go implementation conforms to the design (there is no
refinement checker wired to the source). Closing that gap is a **structured manual
discipline**, and this file is its home: for every spec action, the code function
that implements it and the guard clause enforcing its precondition; for every spec
invariant, the runtime check / regression test that locks it. When any of these
three drift apart, the spec has gone stale — fix it or delete it (a contract that
no longer describes the system is worse than none).

Keep this table current when the protocol changes. `specs/run-tlc.sh` guards the
spec side; code review guards this mapping.

## The actor model

Every one of these bugs came from a *writer that broke the protocol* — so the
spec's confidence is only as good as its coverage of **all** writers. "Strict
adherence by every process" means each actor below either has no forbidden raw
action in the model, or has its forbidden action *refused* by a guard actor:

| Actor | Real component | Modeled in | Its rule |
|-------|----------------|-----------|----------|
| honeybee (work/review/arbitrate/reconcile) | `internal/swarm`, `internal/agent` | L1 as a writer/byzantine agent; L2 (planned) | never writes gitlink; never force-pushes; publishes by committing |
| runner | `internal/swarm/swarm.go` | L1 pin; L2 (planned) | owns the gitlink; pins to tracked tip; verifies protocol adherence (`docs/runner-protocol-vs-correctness.md`) |
| beehived pullMain | `internal/web/sessions.go` `pullMain` | `MainConvergence.PullMainFFOnly` | ff-only; records divergence, never merges |
| beehived editor / resolve / bootstrap | `internal/editor`, `internal/web/resolveagent.go`, `chatedit.go` | L3 (planned) | own only the `hive-edit-` namespace; never reclaim a live/foreign session |
| direct-on-primary CLI verbs | `beehive submodule/plan/task/...` | `MainConvergence.PublishConverging` (fixed) vs `PublishDirectStale` (buggy) | `SyncMainFromRemote` before author, `PublishPrimaryMain` after |
| external push | any peer/operator push | `MainConvergence.ExternalPush` | fetch-merge first (pure ff of remote) |
| external force-rewrite | a rogue/mis-force push | `MainConvergence.ExternalForceRewind` | refused by the pre-receive guard |
| outside agent (worktree skill) | operator-directed hive edits | `MainConvergence` publish actions | never `push HEAD:main` letting local main lag; publish through a convergence path |

## Layer 1 — `MainConvergence.tla`

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `PublishConverging` | action | `git.go:753 SyncMainFromRemote` (before author) + `git.go:777 PublishPrimaryMain` / `git.go:682 PublishToMain` + `git.go:1004 UpdateLocalMain` (after) | `git_test.go:1117 TestSyncMainFromRemoteHealsFork`, `:1188 TestPublishPrimaryMainPushesAndMergesRace` |
| `PublishDirectStale` (buggy) | action | the pre-fix `CommitPaths` on primary without sync (the seam `f152b9b` closed) | reproduced by `MainConvergence_buggy.cfg` |
| `PullMainFFOnly` | action | `sessions.go:764 pullMain` → `git.go:276 Pull` (`git pull --ff-only`) | `git_test.go TestPullFFOnlyDivergence` (ff-only cannot cross a fork) |
| `PushPrimary` | action | `git.go:777 PublishPrimaryMain` (non-ff push refused, never force) | `git_test.go:1188` |
| `ExternalForceRewind` | action | refused by `config/hook.go:153 preReceiveHook` (`refs/heads/main`, `hook.go:189`) | end-to-end regression in the `f8e7828` change |
| `MutatePrimaryTree` | action | staging a write into the live primary working tree before it is committed: `git.go` `submodule add` / `CommitPaths` index+worktree write, an operator edit (StagedAtomic=TRUE fuses stage+convergent-publish; FALSE leaves it uncommitted) | `MainConvergence_stagedheal_buggy.cfg` reproduces the uncommitted window |
| `CommitStaged` | action | the commit that promotes a staged primary-tree write into committed local history (`git.go` `CommitPaths` commit step) | `MainConvergence_stagedheal_fixed.cfg` |
| `HealResetHard` | action | `main.go`/`internal/swarm` preflight dirty-tree heal — `healLocalMain` `git reset --hard HEAD` (see `docs/sharing-modes.md` reset-dirty-with-WARNING guard) | `MainConvergence_stagedheal_buggy.cfg` proves it eats an uncommitted staged write |
| `Reconcilable` | invariant | the whole convergence protocol; `docs/main-convergence-protocol.md` "two anchors that must stay reconcilable" | `MainConvergence_buggy.cfg` proves the fork is reachable without the fix |
| `NoSilentLoss` | invariant | pre-receive guard (`hook.go`) + `SyncMainFromRemote` merge | `MainConvergence_forcerewind.cfg` proves loss reachable without the guard |
| `NoStagedLoss` | invariant | the reset-dirty-tree heal may only discard content some writer will re-stage — a begun primary-tree write MUST be committed (atomically, or authored in a worktree the heal never touches) before the heal runs; the 2026-07-29 `submodule add` orphan-gitlink loss is a begun write eaten uncommitted | `MainConvergence_stagedheal_buggy.cfg` reproduces the loss (MutatePrimaryTree→HealResetHard); `MainConvergence_stagedheal_fixed.cfg` (StagedAtomic) passes |
| `EventuallyConverged` | liveness | `PublishToMain` + `UpdateLocalMain` at every publish site (honeybee/editor/resolve) | `MainConvergence_fixed.cfg` |

## Layer 1 — `SubmodulePointer.tla`

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `StartWork` | action | agent commits + pushes `bee-<taskid>` to submodule origin | — |
| `AgentBumpsBeeTip` (buggy) | action | the removed "bump the submodule pointer" instruction (pre-`1a9bcea`) | reproduced by `SubmodulePointer_buggy.cfg` |
| `MergeToTracked` + runner pin | action | `swarm.go:2241 pinPointerToTrackedTip` (sole gitlink writer, at work-start AND completion) | `swarm_test.go TestWorkPinsPointerToTrackedTipDespiteAgentBeeBump` |
| pin refuses non-durable target | guard | `git.go:352 RemoteContainsCommit` + `git.go:542 BumpGitlink` (refuse bump to a sha not on origin); the WORK handoff gate now enforces the same durable-on-origin check via `RemoteContainsCommit` in `verify.go` (`72e2b4a`) | `git_test.go:1239 TestRemoteContainsCommit`, `swarm_test.go TestVerifyGateRefusesLocalOnlyUnpushedCommit` |
| `ReclaimBranch` GCs bee tip | action | runner branch reclaim after merge | — |
| `PointerDurable` | invariant | `pinPointerToTrackedTip` + `BumpGitlink` guard; `docs/submodule-pointer-invariant.md` | `SubmodulePointer_buggy.cfg` proves dangling reachable without the fix |
| `PointerIsTrackedTip` | invariant | `pinPointerToTrackedTip` | `TestWorkPinsPointerToTrackedTipDespiteAgentBeeBump` |

**Not yet modeled here (belongs to Layer 2 — and now IS, in `TaskStatus.tla`):**
the ambient-pointer false-DONE race (`743b1c6`, `bafd386`) — `swarm.go:2525
recordReviewedCommit` and `:2651 finalizeIfAlreadyMerged` must read the task's OWN
`bee-<taskid>` tip, never the ambient `HEAD:submodules/<sm>/repo` gitlink. That
crosses task-status + review, so it is the Layer-2 `NoFalseDone` property.

## Layer 1 — `BootstrapRace.tla`

Two concurrent bootstrap passes, each observing "no `PLAN.md` exists" for the same
submodule, each attempting to create + publish the plan's first version. Unlike the
other Layer 1 modules, this one is **spec-first and optional** — the Go level
already gates this race today (`TestRunBootstrapGatesOnUnpublishedPlan`), so this
module documents an already-defended candidate rather than closing an observed
bug. `Gated` toggles the fixed protocol (re-check the fresh "plan exists" state
immediately before publish; first-publish-wins) vs the buggy counterfactual (a
session publishes unconditionally from its stale "no plan yet" snapshot).

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `Observe` | action | the selector's "no `PLAN.md` → pick bootstrap kind" check (`swarm.go` task selection) reading main at pass start | `swarm_test.go` bootstrap-selection tests |
| `Build` | action | the bootstrap agent turn loop writing `PLAN.md` into its private worktree, uncommitted until publish | `TestRunBootstrapGatesOnUnpublishedPlan` (the local write, deliberately never committed) |
| `Publish` (gated branch) | action + guard | the publish-time gate on the unpublished-plan check — a bootstrap pass's local completion check must observe its OWN commit landed, not merely the on-disk file, so a stale second session's publish attempt is refused / no-ops | `TestRunBootstrapGatesOnUnpublishedPlan` — asserts the run is NOT reported `Completed` and the task is left for GC/re-drive when the write is never committed |
| `Publish` (ungated/buggy branch) | action | the counterfactual of no re-check: a session publishes unconditionally from its stale snapshot | reproduced by `BootstrapRace_buggy.cfg` only — no such code path exists today |
| `SinglePlanCreated` | invariant | the "gates on unpublished plan" check keeping a second, stale session's publish from ever landing a redundant plan | `BootstrapRace_buggy.cfg` proves two publishes can land without the gate |
| `SecondBootstrapNoOps` | invariant | first-publish-wins semantics — a losing session's publish never overwrites the winner's recorded plan | `BootstrapRace_buggy.cfg` proves the second session's publish can clobber the first session's recorded owner without the gate |

**Conformance note:** `TestRunBootstrapGatesOnUnpublishedPlan` covers the single-
session shape (one bootstrap pass whose write never lands a commit is gated); no
Go regression test yet exercises the TWO-session race this module models end to
end. Per the task card, this is acceptable — land only if a concrete bootstrap
race is ever actually observed in the wild; until then it stays a documented,
already-gated candidate.

## Layer 2 — `TaskStatus.tla`

Status machine faithful to `internal/plan/state.go` (edges in `state.go:13
transitions` + the recovery/escalation methods).

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `DoWork` → `HandoffToReview` gate | action + guard | `swarm/verify.go verifyGate` (committed doc `2573066`; durable-on-origin `RemoteContainsCommit` `72e2b4a`; uncommitted-work gate `fe6da39`) | `swarm_test.go TestVerifyGateRefusesLocalOnlyUnpushedCommit`, `TestVerifyGateAllowsPushedCommit` |
| `HandoffToReview`/`ReviewApprove`/`ReviewReject`/`ArbSideImpl`/`ArbSideReviewer` | actions | `plan/state.go:22 Transition` (edge table `state.go:13`) | `plan` state tests |
| `ArbSideReviewer` attempts/limit → HUMAN | action | `plan/state.go:85 Reject` | `plan_test.go TestRejectAttempts`; `invariant_conformance_test.go TestInvariant_EscalationTerminates` |
| `RecoverLostWork` | action | `plan/state.go:215 RecoverLostWork`; dispatch guards `swarm.go:2788 bounceIfUnreachable`, `:2878 recoverIfLost` | `swarm` recover-lost-work tests |
| `FinalizeAlreadyMerged` | action | `plan/state.go FinalizeAlreadyMerged`; `swarm.go:2651 finalizeIfAlreadyMerged` (own bee tip, not ambient) | `swarm_test.go TestReviewDispatchDoesNotFinalizeOnAmbientPointerAncestry{Remote,LocalSharing}` |
| `RequestHuman` | action | `plan/state.go:255 RequestHuman` (+ `EscalationReady`) | `plan_test.go TestRequestHuman` |
| `Resolve` (operator reopen `NEEDS-HUMAN → TODO`) + `ResetAttemptsOnResolve` fix contract | action + guard | `plan/state.go:294 Resolve` — **GAP: does NOT reset `t.Attempts`** (sets Status=TODO/clears claim only) | `TaskStatus_resolveloop_buggy.cfg` reproduces `AttemptsBounded` violated (escalate→resolve→escalate grows the counter unbounded); `TaskStatus_resolveloop_fixed.cfg` passes with attempts reset per reopen |
| `PassCheck` + DoD gate on DONE-ward edges | action + guard | `swarm/verify.go:266 verifyGate` invariant 5 + `:282 checkGate` (run `Check` via `runVerify`, refuse DONE unless exit 0; `check=none` not gated; fail-closed on infra) (`92d2ed1`) | `swarm_test.go TestVerifyGateChecksDefinitionOfDone`, `TestVerifyGateCheckNoneNotGated` |
| no `TODO → DONE` (work pass) | guard | `swarm.go:2071 workChecklist` refuses a work-set DONE; `gatedHandoff` drops Work→Done (`92d2ed1`) | `swarm_test.go TestWorkDoneFlipRefused` |
| `LegalTransitionsOnly` | invariant | `plan/state.go:12 transitions` + `:19 CanTransition` (single source of truth) | `invariant_conformance_test.go TestInvariant_LegalTransitionsOnly` (exhaustive 5×5 matrix, independent of the code map) + `TestInvariant_HumanIsTerminalForAgent` |
| `NoFalseDone` (durable ∧ merged ∧ DoD-check) | invariant | `verify.go verifyGate` (durability + invariant-5 check gate) + `finalizeIfAlreadyMerged`/`recordReviewedCommit` own-tip fix | `TaskStatus_buggy.cfg` (durability defect) + `TaskStatus_buggy_check.cfg` (jellyfin DoD defect) both prove false-DONE reachable when the respective gate is off |
| `Terminates`, `LostWorkRecovers` | liveness | attempts/limit escalation + `recoverIfLost` dispatch guard | `invariant_conformance_test.go TestInvariant_EscalationTerminates` (deterministic escalation to terminal NEEDS-HUMAN) + `TaskStatus_fixed.cfg` |

**Modeling note (verified against the code, not assumed):** the operator `Resolve`
edge `NEEDS-HUMAN → TODO` (`plan/state.go:294 Resolve`) does **not** reset `Attempts`
— it sets `Status = TODO` and clears the claim only. Previously `TaskStatus.tla`
*scoped this edge out* and treated `NEEDS-HUMAN` as terminal, on the reasoning that
the reopen loop is out-of-band; the cost was that the diagnosed unbounded-attempts
bug was invisible to the model. It is now modeled explicitly as a reproduce-then-lock
counterexample (mirroring the handoff-terminal-leak `_leak_*.cfg` pattern):

- `Resolve` action, guarded by `CONSTANT ModelResolve` (the other cfgs keep
  `ModelResolve = FALSE`, so `NEEDS-HUMAN` stays terminal and their behaviour is
  byte-identical to before — the existing 12 TaskStatus cases are unchanged).
- `CONSTANT ResetAttemptsOnResolve` pins the fix contract: `FALSE` = `state.go`
  Resolve *as written* (no reset) → `AttemptsBounded` violated with a counterexample
  trace (`TaskStatus_resolveloop_buggy.cfg`); `TRUE` = Resolve resets `attempts` to 0
  per operator reopen → `AttemptsBounded` holds and the task still terminates
  (`TaskStatus_resolveloop_fixed.cfg`).

**Fix contract (what the Go `Resolve` must satisfy before operator reopen is wired
into the autonomous loop):** either `Resolve` resets `t.Attempts = 0` (a fresh rework
budget per reopen — the modeled fix), or attempts become per-cycle so a reopen starts
a new budget. Until then `plan/state.go:294 Resolve` is a **known conformance gap**:
the fixed cfg models a `Resolve` the code does not yet implement. The buggy cfg is the
faithful model of today's code.

Counterexample trace (`TaskStatus_resolveloop_buggy.cfg`, `Limit = 1`):

```
State 1  <Init>          attempts = 0  status = "TODO"
State 4  ReviewApprove   attempts = 0  status = "DONE"
State 5  GateCheck       attempts = 1  status = "REVIEW"   (unearned terminal reverted, attempts++)
State 6  GateCheck       attempts = 2  status = "HUMAN"    (past Limit → escalate)
State 7  Resolve         attempts = 2  status = "TODO"     (operator reopen — attempts NOT reset)
State 8  HandoffToReview attempts = 2  status = "REVIEW"
State 9  GateCheck       attempts = 3  status = "HUMAN"    → AttemptsBounded (attempts ≤ Limit+1 = 2) VIOLATED
```

Run-tlc.sh output (both new cases, wired into the CASES list):

```
OK   TaskStatus_resolveloop_fixed.cfg (expected pass)
OK   TaskStatus_resolveloop_buggy.cfg (expected fail)
```

## Layer 2 — `ClaimRace.tla`

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `Tick` keepalive (fixed) | action | runner mid-turn heartbeat re-stamp; `claim` heartbeat model | `301964d` change |
| `ClaimFresh` / `ClaimStale` | actions | `claim.Claimer.Claim` (pre-dispatch re-confirm pull + `PreClaimJitter`); selection staleness window `plan.Plan.Candidates(now, ttl)` driven by the Selector `TTL`/`TurnTimeout` (`select/select.go`) | `301964d` change |
| `Finish` / `LoseRace` | actions | `claim` publish-conflict → `ErrLost` (`claim.go`) | `claim` lost-race tests |
| `AtMostOneLands` | invariant | the single-owner publish conflict (`claim.go ErrLost`) — the definitive guard | `ClaimRace_buggy.cfg` shows it survives the dispatch bug |
| `NoDuplicateDispatch` | invariant | mid-turn keepalive + decoupled liveness window (`plan.Plan.Candidates`) + pre-dispatch re-confirm (`301964d`) | `ClaimRace_buggy.cfg` proves duplicate dispatch reachable without them |
| `EventuallyLanded` | liveness | `claim` + selection fairness | `ClaimRace_fixed.cfg` |

## Layer 2 — `ClaimGC.tla`

The heartbeat-vs-TTL claim-GC TIMING invariant `ClaimRace.tla` leaves over-approximated
(its `Fixed` toggle just decides whether a dispatched owner's heartbeat tracks the clock
at all): this module pins the actual PARAMETER-ORDERING the two mechanisms rest on — the
runner's mid-turn keepalive period `K` vs. selection's claim-GC threshold `TTL` — the
honeybee-claim analogue of the editor's `LiveGuard` guarantee.

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `K` (keepalive period) | constant | `swarm.Runner`'s mid-turn heartbeat re-stamp, fired roughly every `TTL/3` for the whole duration of a live turn | `internal/select/select.go` (`"heartbeat re-stamp every ~TTL/3"` comment) |
| `TTL` (GC reclaim threshold) | constant | the claim-GC staleness window a claim's heartbeat is compared against | `internal/config/config.go TTLMinutes`, `plan.Plan.Candidates(now, ttl)` |
| `Tick` | action | one logical tick: while live, the keepalive restamps (`age` resets to 0) whenever `age` would reach `K`; while dead, `age` grows unboundedly (capped at `MaxAge` for a finite state space) | `select/select.go` keepalive loop |
| `Die` | action | the owning pass's turn ends / the process dies — no further keepalive restamps | a crash, OOM-kill, wall-clock/idle-timeout abort, or worktree teardown mid-turn |
| `GC` | action | selection's claim-GC reclaiming a claim whose heartbeat has gone stale past `TTL` | `plan.Plan.Candidates(now, ttl)` |
| `NoLiveReclaim` | invariant | a live pass's claim is never reclaimed out from under it when `K < TTL` | `ClaimGC_buggy.cfg` (`K >= TTL`) reproduces a live claim getting reclaimed |
| `EventuallyReclaimDead` | liveness | a dead pass's claim is always eventually reclaimed — no permanent wedge | `ClaimGC_fixed.cfg` |

## Layer 2 — `PlanConvergence.tla`

The reconcile-vs-status-flip merge race on structured PLAN.md CONTENT (as opposed
to `MainConvergence.tla`'s raw `main` ref, or `TaskStatus.tla`'s single-task legal
edges): a reconcile pass rewrites PLAN.md wholesale from ROI.md while work/review
passes concurrently flip individual task STATUS fields and an operator/maintenance
pass leans a DONE task's narrative (`ArchiveDone`). `DedupGuard` models the
already-implemented reconcile-dedup-skip mitigation (`swarm.go` ~:604
`Runner.reconciled`); `ThreeWayMerge` models whether the reconcile publish is a
true per-field git merge (only ever changes lines its own ROI delta owns) vs a
buggy whole-file overwrite from a (possibly stale) snapshot.

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `WorkFlip` | action | a work/review pass's single-task PLAN.md status edit landing on `main` independent of any reconcile fold (`internal/plan` `Transition` + the runner's publish path) | `plan` state tests (Layer 2a `TaskStatus.tla`) |
| `ArchiveLean` | action | `internal/plan/archive.go:42 ArchiveDone` — leans a DONE task's narrative out of its PLAN.md body, invoked from `cmd/beehive/cmd_plan.go:199` | `plan/archive_test.go` (round-trip / idempotence of `ArchiveDone`) |
| `ReconcileSnapshot` | action | `swarm.go` reconcile pass reading `PLAN.md` after `refreshMain` (pull) at pass start | `swarm_test.go` reconcile-pass tests |
| `ReconcilePublish` (dedup-skip branch) | action + guard | `swarm.go:604 Runner.reconciled` — re-pulls main and prefix-compares the FRESH `Beehive-ROI` stamp against ROI head immediately before publishing; short-circuits (no session, no publish) when already applied | `swarm_test.go` tests covering the reconcile pre-check pull + `reconciled()` short-circuit |
| `ReconcilePublish` (fold branch) | action | the reconcile fold's actual `git`-level publish of the rewritten `PLAN.md` — a real git 3-way text merge is per-line, so a reconcile diff and a concurrent status-flip diff on distinct lines merge without conflict; `ThreeWayMerge = FALSE` models the counterfactual of a publish that instead force-writes the ENTIRE file from the reconcile session's own (possibly stale) snapshot | none yet — no Go conformance test asserts the reconcile publish path preserves concurrently-landed fields on a real 3-way merge; **gap**, see below |
| `NoLostStatus` | invariant | relies on git's line-based 3-way merge never being replaced by a whole-file overwrite in the reconcile publish path | `PlanConvergence_stale_overwrite_buggy.cfg` proves a committed status transition can be silently reverted without it |
| `NoResurrect` | invariant | relies on the same line-based merge preserving an `ArchiveDone` lean against a concurrent, stale-based reconcile rewrite | `PlanConvergence_stale_overwrite_buggy.cfg` proves an archived task can resurrect without it |
| `NoRedundantReconcile` | invariant | `swarm.go:604 Runner.reconciled` dedup-skip guard | `PlanConvergence_dedup_buggy.cfg` proves a superseded reconcile session redundantly re-publishes (the "zero-progress reconcile pass" defect) without the guard |

**Conformance gap (honest, not closed by this task):** the reconcile publish path's
reliance on git's line-based 3-way merge (rather than any explicit whole-file
overwrite) has not been asserted by a targeted Go regression test that lands a
concurrent status flip and a reconcile fold against the same base and checks the
merged result preserves both. `ThreeWayMerge = FALSE` in this spec is therefore a
reproduce-then-lock counterfactual for a defect class the current code likely
avoids structurally (real `git merge` is always line-based, there is no whole-file
overwrite in the reconcile publish call path as read) rather than a defect
observed in production. Filing and closing that test is future work; this task's
Accept bar is the spec + the invariants + the wiring, per its Check command.

## Layer 2 — `DependencyReadiness.tla`

The N-task dependency graph with cross-submodule links (`internal/links/links.go`,
`internal/select/graph.go`) and the two silent-wedge failure classes it forbids: a
dangling/phantom dep (`92d2ed1`) and a dependency cycle. The module models N tasks
with `DepEdges` (a `to` outside `Tasks` is phantom; `t<->t'` is a cycle), toggled by
`DepGuard` (phantom → escalate vs silent HELD) and `CycleGuard` (cycle → escalate vs
silent HELD). Scenario graphs (`Tasks3/5`, `EdgesFixed/Phantom/Cycle`) are overridden
into each cfg via `<-` because TLC cfgs cannot express `<<>>` tuple literals.

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `Yield` (real dep → HELD; phantom → escalate; cycle → escalate) | action + guards | `swarm.go taskYieldedBlocked` (accept a blocked yield only if every dep is a real task — local via `plan.Blocked`, cross via `selectt.LoadEdges`; fail-loud on a phantom); `select/graph.go InCycle` + `select/select.go graphGate` exclude a cyclic task; `links.CyclicNodes` (Tarjan SCC) | `swarm_test.go TestWorkYieldOnPhantomDepFailsLoud`; `select` `TestCyclicTasksNotSelected`; `links` `TestCyclicNodes`/`TestCycleExported` |
| `HasPhantom` / `RealDeps` / `DepsAllDone` | operators | dangling local dep (`plan.Plan.DanglingDeps`, `plan.go`); cross-link resolution (`select/graph.go crossDepSatisfied`) | `plan_test.go TestDanglingDeps` |
| `OnCycle` (reachability over real edges) | operator | `links.Cycle` / `links.CyclicNodes` over the combined graph; `Graph.Validate` (pre-commit / `beehive lint` cycle refusal); `links.AddDep` cycle-rejecting write | `links` `TestCycle*`; `TestPreCommitDepCycleGuardE2E` |
| `PhantomNeverHeld` | invariant | phantom-dep refusal in `taskYieldedBlocked` | `DependencyReadiness_buggy.cfg` proves a phantom dep is silently held without `DepGuard` |
| `CycleNeverHeld` | invariant | cyclic-task exclusion → escalation rather than silent hold (`graphGate` + `InCycle`) | `DependencyReadiness_cycle_buggy.cfg` proves a cycle is silently held without `CycleGuard` |
| `EventuallyResolved` | liveness | real dep completes → DONE; phantom or cycle → NEEDS-HUMAN | `DependencyReadiness_fixed.cfg` |

## Layer 2 — `Selection.tla`

The deterministic task-selection layer (`internal/select/select.go` `Select` +
`weightedOrder` + `pickTask`, and the `not_before` wall-clock gate
`plan.Task.NotBeforeReached` / `plan.Plan.Candidates`). It extends the selection
dimension `ClaimRace.tla` touches from the claim side, covering the selector's two
OTHER obligations, each a distinct failure class: STARVATION (a continuously-ready
task never selected — a liveness failure the weighted-random-over-positive-weights
guarantee forbids) and PREMATURE DISPATCH (a future-`not_before` task selected before
its gate — a safety failure). Toggled by `FairSelect` (per-task selection fairness =
positive weight, on vs off) and `GateHonored` (honor `not_before` vs ignore it).
`clock` is a logical wall clock; `Release` models the selection-claim TTL requeuing a
task into the continuous work stream (the ready pool over which no-starvation must
hold). Scenario `Tasks`/`NotBefore` are `<-`-substituted (`Tasks3`, `NBNone`/`NBGate`)
since cfgs cannot express function literals.

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `Select(t)` (weighted-random over ready candidates; gate-honoring) | action + guard | `select/select.go Select` + `weightedOrder` + `pickTask` over `plan.Plan.Candidates`; the gate clause `t.NotBeforeReached(now)` (`plan/compat.go:42`) | `select` `TestSelect*`; `plan_test.go TestNotBeforeReached` |
| `Ready(t)` (not_before reached) | operator | `plan.Task.NotBeforeReached` (`plan/state.go:75`); `Candidates` main-tier gate (`plan/compat.go:42`) | `plan_test.go TestNotBeforeReached` |
| `Release(t)` (claim TTL requeue) | action | `plan.Task.Active`/`Stale` staleness window driving `Candidates(now, ttl)`; `plan.Task.Release` | `plan_test.go` claim/TTL tests |
| `NoPrematureDispatch` | invariant | `Candidates` holds a TODO task out of the main tier until `NotBeforeReached(now)` | `Selection_premature_buggy.cfg` proves early dispatch reachable when the gate is ignored (`GateHonored=FALSE`) |
| `EventuallySelected` / `NoStarvation` | liveness | weighted-random with positive weight for every ready candidate (`weightedOrder`/`pickTask`) — every ready task eventually selected | `Selection_fixed.cfg` (holds); `Selection_starvation_buggy.cfg` reproduces the starvation lasso when fairness is dropped (`FairSelect=FALSE`) |

## Layer 2 capstone — `CrossLayer.tla`

The cross-layer COMPOSITION of `TaskStatus.tla`'s false-DONE gates with
`DependencyReadiness.tla`'s dependency-edge readiness. Each lower spec proves its
property in ISOLATION — `TaskStatus.NoFalseDone` (a task reaches DONE only with
durable+merged work whose DoD `Check` passes, `72e2b4a`/`92d2ed1`) and
`DependencyReadiness` (a dependent unblocks when its dep is DONE) — but neither says
what happens when they MEET: a false-DONE is exactly the STATUS a downstream selector
reads to unblock a dependent, so it PROPAGATES (the dependent builds on phantom /
unverified upstream work). This module models one edge — a producer dep `P` running a
distilled TaskStatus lifecycle (`PDoWork`→`PHandoff`→`PApprove`, each DONE-ward edge
carrying the same `Gated` durability toggle and `CheckGated` DoD toggle) and a
consumer dependent `C` (`CUnblock` keyed on the OBSERVED `pStatus="DONE"`, the
realistic `crossDepSatisfied` selector behaviour, then `CProgress`). The composed
invariant proves the two gates keep the observed-DONE unlock honest ACROSS the edge.

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `PHandoff` / `PApprove` DONE gates | action + guards | `swarm/verify.go verifyGate` (durable-on-origin `RemoteContainsCommit` `72e2b4a`; DoD `Check` inv 5 `92d2ed1`) — the same gates `TaskStatus.HandoffToReview`/`ReviewApprove` model, here on the dep of an edge | `swarm_test.go TestVerifyGateRefusesLocalOnlyUnpushedCommit`; DoD-check regression (`92d2ed1`) |
| `CUnblock` (unlock on observed dep DONE) | action | `select/graph.go crossDepSatisfied` / `select.go graphGate` — a dependent's cross/local dep is satisfied only when the dep task's status is DONE | `select` cross-dep readiness tests |
| `PTrulyDone` (durable ∧ merged ∧ DoD) | operator | the composition of `verify.go` durability + inv-5 DoD — `TaskStatus.NoFalseDone`'s antecedent, reused as the honest-unlock predicate | — |
| `NoPrematureUnlock` | invariant | the false-DONE gates enforced on a dep BEFORE any dependent unblocks (`verifyGate` + `crossDepSatisfied` composed) | `CrossLayer_buggy.cfg` proves a non-durable false-DONE prematurely unlocks the dependent (durability gate off); `CrossLayer_buggy_check.cfg` proves a DoD false-DONE does too (check gate off) — both gates must compose across the edge |
| `ConsumerNeverPhantom` | invariant | the dependent never finishes built on vanished upstream work | `CrossLayer_buggy.cfg` |
| `EventuallyBothDone` | liveness | gates on → dep earns DONE, dependent then unblocks and completes; the edge never wedges | `CrossLayer_fixed.cfg` |

## Layer 3 (delivered) — `EditorSessionNamespace.tla`

The beehived chat-diff editor Manager reclaim/gc dance (faithful to
`internal/editor/editor.go`).

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `OpenSession` / `TurnEdit` / `CloseSession` | actions | `editor/editor.go` Open + `runTurn` (commit sidecar `transcriptSidecarPath` :84 + `git.go:419 PushBranchReconciled`, never force) | `editor/durability_test.go:41 TestSessionDurabilityPushesEditBranchAfterTurnAndIsFetchable` |
| `Reclaim` KEEP-or-RECLAIM decision | action + guards | `editor/editor.go:716 evaluateWorktree`, `:811 Reclaimable`, `:889 reclaim` (delete worktree+local+remote) | `editor/reclaimable_test.go:21 TestReclaimableListsStaleCleanOnly`, `:382 TestReclaimDeletesRemoteBranchPreventingResurrection` |
| `RecoverReload` | action | `editor/editor.go:635 Reload`, `:774 recoverMissingWorktree` (prefer local ref, else trusted remote) | `durability_test.go:150 TestReloadRebuildsWorktreeWhenOnlyCheckoutDirWiped`, `:206 TestReloadRecoversAfterLocalWorktreeAndBranchLost` |
| `NamespaceScoped` guard | guard | `editor/editor.go:75 editBranchPrefix="hive-edit-"`, `:598 isEditBranch`, remote glob `editBranchPrefix+"*"` | `editor_test.go:405 TestReloadNeverTouchesForeignEditWorktrees` |
| `LiveGuard` | guard | `evaluateWorktree` live-`byID` skip (`editor.go` INVARIANT block :52) | `editor_test.go:472 TestReloadNeverReclaimsLiveRegisteredSession` |
| `RemoteDurable` | toggle | `trustedRemote` gate (`editor.go:343`) + `git.go:468 ListRemoteBranches` remote-recovery scan | `durability_test.go:105 TestNoRemoteSkipsDurabilityAsNoOp` |
| `NoForeignReclaim` | invariant | namespace scoping (every enumeration bound to `hive-edit-`) | `buggy_namespace.cfg` proves foreign reclaim reachable when unscoped |
| `LiveSessionNeverReclaimed` | invariant | live-`byID` skip in `evaluateWorktree` + `Reclaimable` | `buggy_liveguard.cfg` proves live reclaim reachable without the guard |
| `SessionDurable` | invariant | per-turn push + local-ref-preferred recovery | `buggy_remote.cfg` proves a pending session is lost without remote push |

**Modeling note:** b08c995's SECONDARY symptom (the Manager *adopting* a foreign
worktree into its store as a bogus session) is not separately encoded — the
destructive `Reclaim`-of-foreign it also caused is the strictly worse effect and
is what `NoForeignReclaim` catches; the same namespace scoping fixes both.
`recoverMissingWorktree`'s "prefer the surviving local ref over an older remote
tip" is captured by `Recoverable` counting `localBranch` first.

## Layer 2 — `DeferBound.tla`

The self-defer convergence-wait termination behind `internal/plan/mutate.go`
`Defer` (bumps `not_before` + increments `Defers`, gated by `plan.MaxDefers`)
and `Reopen` (`Defers = 0`). This is the mechanism behind `beehive task defer`
and the runner's own re-check scheduling (HONEYBEE.md's "TODO -> TODO
(self-defer)"): a work pass that did the work but finds the world has not
converged yet re-checks after `until`, used for GitOps reconcile, CI runs, and
the `Verify-After-Merge` successor-check's merge-gated live effect (the
follow-up flagged in the Roadmap below). Failure class = LIVELOCK: a task
defers forever on a convergence that never arrives (never escalates), or gives
up before the effect actually lands. The external "converged" effect is
modeled as a deliberately UNFAIR (adversarial) environment action — real
convergence is never assumed to eventually happen, so termination must hold
even if it never does. CONSTANT `Bounded` toggles the fix (`Defer` refuses
past `MaxDefers` and the task escalates NEEDS-HUMAN instead, matching
`swarm.go`'s defer-cap refusal) vs the buggy unbounded behavior (`Defer` stays
enabled forever, no escalation edge exists).

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `Defer` (bump not_before, increment Defers, capped) | action + guard | `plan/mutate.go Defer`; the cap enforcement is `swarm.go:2214` "work pass self-deferred task past the defer cap" refusal | `mutate_test.go` Defer tests; `swarm_test.go` defer-cap regression |
| `Escalate` (bound exhausted, unconverged -> NEEDS-HUMAN) | action | the only remaining move once `swarm.go:2214-2215` refuses another `Defer` past `plan.MaxDefers` | `swarm_test.go` defer-cap refusal test |
| `Reopen` (`Defers = 0` reset) | operator | `plan/mutate.go Reopen` — a fresh operator reopen gets a fresh defer budget | `mutate_test.go TestReopen*` |
| `NoDoneWhileUnconverged` | invariant | `Progress` (leaving the defer loop) is gated on the real external effect, never merely on having deferred enough times | holds in both cfgs — the bug is a liveness failure, not a false-progress safety one |
| `Terminates` | liveness | `plan.MaxDefers` bound + `swarm.go`'s defer-cap refusal together guarantee the task always reaches DONE or NEEDS-HUMAN, regardless of whether the awaited effect ever converges | `DeferBound_fixed.cfg` (holds); `DeferBound_buggy.cfg` reproduces the livelock — Defer fires forever, Converge/Escalate never taken, Terminates violated with a counterexample trace |

## Adjacent-path audit — the handoff terminal-leak shape

`TaskStatus.tla`'s `NoDoclessTerminal` (cfgs `TaskStatus_leak_{buggy,fixed}.cfg`)
models ONE leak: the per-turn `internal/claim` `Heartbeat` republishes PLAN.md's
on-disk status to `main` unconditionally, so when the runner PINS an unearned
terminal (`NEEDS-REVIEW`/`DONE`) in place on a gate-fail, later heartbeats leak a
**forward terminal status ahead of its change doc**. The terminal-leak blocker's
"audit every adjacent path with the same ordering shape" clause requires proving
that shape exists *nowhere else*. Two shapes are candidates:

- **(a) commit-PLAN-then-publish independent of the backing artifact** — any
  `internal/claim` verb doing `CommitPaths(planRel())` + `Publish`;
- **(b) reset/clean a git working tree** — could a heal drop a not-yet-published
  artifact?

The leak's *essential precondition* is a **forward** terminal (`NEEDS-REVIEW` or
`DONE`) published while its change doc is NOT yet durable on `main`. A path is
CLEARED when it either never publishes a forward terminal, or only publishes one
whose doc is already proven durable. Verdicts below; every path was evaluated and
**all are cleared** — no adjacent path reproduces the leak, so no new buggy/fixed
cfg is warranted (the one real instance is already covered by
`TaskStatus_leak_*.cfg`).

### Shape (a) — `internal/claim` verbs (`CommitPaths(planRel())` + publish)

| Verb (`internal/claim/claim.go`) | Status effect | Publishes? | Verdict | Why cleared |
|---|---|---|---|---|
| `Heartbeat` (:182) | re-stamp, status unchanged | yes | **the leak itself** | Republishes on-disk status unconditionally — the exact vehicle; MODELED by `NoDoclessTerminal` + revert-over-pin fix (`TaskStatus_leak_*.cfg`). Not a *new* gap. |
| `Release` (:227) | clears claim only, status unchanged | yes | cleared | Never sets a terminal; only unclaims whatever phase the agent already committed. No status moves ahead of any artifact. |
| `Reject` (:266) | `NEEDS-REVIEW → NEEDS-ARBITRATION`/`TODO` | no (commit only) | cleared | Moves BACKWARD to rework/dispute; the leak needs a FORWARD terminal. `NEEDS-ARBITRATION`/`TODO` are not in the terminal set and carry no forward-doc obligation. Doesn't publish (piggybacks `finish()`). |
| `Strand` (:295) | `NEEDS-REVIEW → TODO`/`NEEDS-HUMAN` | no (commit only) | cleared | Backward demotion of an unreachable-commit task — the OPPOSITE of leaking a terminal (it REMOVES an unearned `NEEDS-REVIEW`). No publish. |
| `BounceUnreachable` (:324) | `NEEDS-REVIEW → NEEDS-ARBITRATION` | yes | cleared | Deterministic dispute escalation; `NEEDS-ARBITRATION` is a rework status, not a doc-bearing forward terminal. The implementer doc that reached `NEEDS-REVIEW` is already durable; the arbiter reads it. |
| `RecoverLostWork` (:359) | `→ TODO`/`NEEDS-HUMAN` | yes | cleared | Backward recovery of confirmed-lost work; no forward terminal. |
| `RecordReviewCommit` (:394) | sets `review=<sha>` stamp, status unchanged | yes | cleared | Records the agent's ALREADY-pushed bee tip as PLAN metadata; the sha IS its own backing artifact (durability re-checked by the verify gate). No status flip at all. |
| `FinalizeAlreadyMerged` (:435) | `→ DONE` | yes | cleared | The one forward-to-terminal publish, but GATED on the recorded pointer being a proven ANCESTOR of tracked `main` (the merge is durable) — and the work-pass doc that drove the task to `NEEDS-REVIEW` is already on `main` (the handoff gate required it). This is exactly `NoFalseDone`'s (durable ∧ merged) conjunct, already modeled in `TaskStatus.tla`. |
| `ClaimLock`/`ReleaseLock` (:506/:546) | lock-file only, no task status | yes | cleared | Operates on `.bee-lock-<name>`, never a task status or a change doc. Out of the terminal-leak shape entirely. |

Summary: only `Heartbeat` publishes a status that can be a leaked forward
terminal, and it is precisely what `TaskStatus_leak_*.cfg` reproduces and the
revert-over-pin + runner-owned-doc-synthesis fix closes. Every other verb is
either backward-only (rework/recovery), status-neutral (Release, RecordReviewCommit,
locks), or a durability-gated finalize (`FinalizeAlreadyMerged`, covered by
`NoFalseDone`).

### Shape (b) — git working-tree resets / config revert

| Path (`internal/…`) | What it resets | Verdict | Why cleared |
|---|---|---|---|
| `git.go:824 healLocalMain` | `reset --hard HEAD` + `submodule update --force` + `clean -ffd`, scoped to the MAIN worktree and `submodules/<name>/repo` projections | cleared | Discards only UNCOMMITTED drift in the projection; NEVER touches the agent's `submodules/<name>/worktrees/bee-*` worktree where a not-yet-published artifact lives as a real COMMIT. By definition a `reset --hard HEAD` drops nothing committed, so it cannot drop a durable artifact. |
| `git.go:907 EnsureCleanCheckout` | delegates to `healLocalMain` when the tree is dirty | cleared | Same scoping/argument as `healLocalMain`; heals the shared checkout to a pure HEAD projection before any tokens are spent, never the bee worktree. |
| `git.go:682 PublishToMain` / `:1004 UpdateLocalMain` dirty-tree heal | `reset --hard` on the main worktree before the push | cleared | The commits being published already exist on the source branch; the heal only clears drift that would block `updateInstead`. No artifact is uncommitted at this point. |
| `swarm.go:1147 RestoreConfig` (→ `git.go:228 RestoreRemotes`) | git-config `remote.*` sections only | cleared | Touches NO working tree and NO tracked artifact — it rewrites remote config keys and publishes nothing. Structurally outside both shapes; cannot leak a status or drop a doc. |
| `swarm.go:2459 landSourceBranch` / `:2486 demoteUnpushed` (conflict/land path) | no tree reset; on push-failure demotes via `claim.Strand` + publish | cleared | Pushes the source branch; on failure the correction is BACKWARD (`NEEDS-REVIEW → TODO`/`NEEDS-HUMAN`), i.e. it REMOVES an unearned terminal at an unreachable commit — the inverse of the leak. No reset drops an artifact. |

Conclusion: no working-tree reset can drop a durable (committed) artifact — every
reset is scoped to a HEAD projection and drops only uncommitted drift — and the
config revert touches no tree at all. No shape-(b) gap.

### Audit outcome

Every adjacent path enumerated by the blocker is **cleared**; the sole real
instance of the terminal-leak ordering shape is the handoff/`Heartbeat` pin path,
already covered by `TaskStatus.tla`'s `NoDoclessTerminal` invariant and its
`TaskStatus_leak_{buggy,fixed}.cfg`. No new buggy+fixed cfg was added because the
audit found no uncovered gap — `run-tlc.sh` is unchanged and all 18 cases still
behave as declared.

## Roadmap

Layers 1–3 cover every catalogued bug, including the DoD-check false-DONE
(`92d2ed1`) and the dangling-dependency wedge. Two follow-ups track code that is
*specified but not yet landed* (see `docs/dod-verification-spec.md` Rollout), so
they are deliberately NOT modeled yet:
- **`Verify-After-Merge` successor check task** — the merge-gated live-effect DoD
  carried by a runner-spawned successor task. When it lands, `TaskStatus.tla`
  gains a successor-check state and `NoFalseDone` extends to the merge-gated
  effect. (Only the pre-merge `Check` gate is landed and modeled today.) The
  self-defer convergence-wait TERMINATION this successor check will lean on —
  bounded re-check, escalate on exhaustion — is now modeled independently by
  `DeferBound.tla` (Layer 2), so this follow-up need only add the successor
  STATE, not re-derive its termination argument.
- **DoD authoring/teaching layer** — post-select check injection, CLI verbs,
  generated-prompt edits. Prompt-only; no protocol invariant to model.

Other DEEPENING targets (not new layers): richer multi-task / multi-submodule
interaction, and the cross-layer composition where a Layer-2 false-DONE would
corrupt the Layer-1 gitlink.

## Caveats (how much confidence this actually buys)

- **No auto Go↔spec refinement.** This mapping is manual; budget to maintain it or
  the spec silently diverges from the code (the classic "spec goes stale" failure).
- **Byzantine-agent modeling is a feature, not a limitation.** The LLM interior is
  unmodelable; it is modeled purely by worst-case git *effects* (may leave work
  uncommitted, may stage a bee-tip gitlink). The specs then prove the
  runner/hook/pin defends the invariant *regardless* — the general form of the
  existing regression tests.
- **TTL is wall-clock**, abstracted (in `ClaimRace.tla`) to a logical `clock` with
  the keepalive tracking it while a claim is dispatched; this over-approximates
  timing and so is a safe (never-too-optimistic) model of the claim race.
- **State explosion is the ceiling.** Tiny constants (2 submodules, 2–3
  tasks/artifacts) already surface every catalogued Layer-1 bug; symmetry / small
  bounds keep Layers 2–3 tractable.
- **A spec only checks what you tell it.** If an invariant is missing or the model
  is unfaithful, TLC gives false confidence. Every invariant here traces to a real,
  historical, understood failure — that is the discipline that keeps the model
  honest.
