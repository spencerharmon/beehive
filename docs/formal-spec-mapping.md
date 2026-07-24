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
| `Reconcilable` | invariant | the whole convergence protocol; `docs/main-convergence-protocol.md` "two anchors that must stay reconcilable" | `MainConvergence_buggy.cfg` proves the fork is reachable without the fix |
| `NoSilentLoss` | invariant | pre-receive guard (`hook.go`) + `SyncMainFromRemote` merge | `MainConvergence_forcerewind.cfg` proves loss reachable without the guard |
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

## Layer 2 — `TaskStatus.tla`

Status machine faithful to `internal/plan/state.go` (edges in `state.go:13
transitions` + the recovery/escalation methods).

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `DoWork` → `HandoffToReview` gate | action + guard | `swarm/verify.go verifyGate` (committed doc `2573066`; durable-on-origin `RemoteContainsCommit` `72e2b4a`; uncommitted-work gate `fe6da39`) | `swarm_test.go TestVerifyGateRefusesLocalOnlyUnpushedCommit`, `TestVerifyGateAllowsPushedCommit` |
| `HandoffToReview`/`ReviewApprove`/`ReviewReject`/`ArbSideImpl`/`ArbSideReviewer` | actions | `plan/state.go:22 Transition` (edge table `state.go:13`) | `plan` state tests |
| `ArbSideReviewer` attempts/limit → HUMAN | action | `plan/state.go:85 Reject` | `plan` reject-overflow test |
| `RecoverLostWork` | action | `plan/state.go:215 RecoverLostWork`; dispatch guards `swarm.go:2788 bounceIfUnreachable`, `:2878 recoverIfLost` | `swarm` recover-lost-work tests |
| `FinalizeAlreadyMerged` | action | `plan/state.go FinalizeAlreadyMerged`; `swarm.go:2651 finalizeIfAlreadyMerged` (own bee tip, not ambient) | `swarm_test.go TestReviewDispatchDoesNotFinalizeOnAmbientPointerAncestry{Remote,LocalSharing}` |
| `RequestHuman` | action | `plan/state.go RequestHuman` (+ `EscalationReady`) | `plan` human-request test |
| `LegalTransitionsOnly` | invariant | `plan/state.go:18 CanTransition` (edge table is the single source of truth) | `plan` transition tests |
| `NoFalseDone` | invariant | `verify.go verifyGate` + `finalizeIfAlreadyMerged`/`recordReviewedCommit` own-tip fix | `TaskStatus_buggy.cfg` proves false-DONE reachable when ungated |
| `Terminates`, `LostWorkRecovers` | liveness | attempts/limit escalation + `recoverIfLost` dispatch guard | `TaskStatus_fixed.cfg` |

**Modeling note (verified against the code, not assumed):** the operator `Resolve`
edge `NEEDS-HUMAN → TODO` (`plan/state.go Resolve`) does **not** reset `Attempts`.
So across resolve/retry cycles the counter is unbounded — `AttemptsBounded` holds
only for the *autonomous* machine (`NEEDS-HUMAN` terminal, matching the selector's
own exclusion of `NEEDS-HUMAN` from selection). `TaskStatus.tla` therefore omits
the `Resolve` action and treats `NEEDS-HUMAN` as terminal; this was surfaced by TLC
(the fixed cfg failed `AttemptsBounded` until the reopen loop was scoped out).

## Layer 2 — `ClaimRace.tla`

| Spec element | Kind | Code (`internal/…`) | Test / guard |
|---|---|---|---|
| `Tick` keepalive (fixed) | action | runner mid-turn heartbeat re-stamp; `claim` heartbeat model | `301964d` change |
| `ClaimFresh` / `ClaimStale` | actions | `claim.Claimer.Claim` (pre-dispatch re-confirm pull + `PreClaimJitter`); selection staleness window `plan.Plan.Candidates(now, ttl)` driven by the Selector `TTL`/`TurnTimeout` (`select/select.go`) | `301964d` change |
| `Finish` / `LoseRace` | actions | `claim` publish-conflict → `ErrLost` (`claim.go`) | `claim` lost-race tests |
| `AtMostOneLands` | invariant | the single-owner publish conflict (`claim.go ErrLost`) — the definitive guard | `ClaimRace_buggy.cfg` shows it survives the dispatch bug |
| `NoDuplicateDispatch` | invariant | mid-turn keepalive + decoupled liveness window (`plan.Plan.Candidates`) + pre-dispatch re-confirm (`301964d`) | `ClaimRace_buggy.cfg` proves duplicate dispatch reachable without them |
| `EventuallyLanded` | liveness | `claim` + selection fairness | `ClaimRace_fixed.cfg` |

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

## Roadmap

None — Layers 1–3 cover every catalogued bug. Future targets are DEEPENING (not
new layers): richer multi-task / multi-submodule interaction, and the cross-layer
composition where a Layer-2 false-DONE would corrupt the Layer-1 gitlink.

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
