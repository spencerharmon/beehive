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
| `MergeToTracked` + runner pin | action | `swarm.go:2282 pinPointerToTrackedTip` (sole gitlink writer, at work-start AND completion) | `swarm_test.go TestWorkPinsPointerToTrackedTipDespiteAgentBeeBump` |
| pin refuses non-durable target | guard | `git.go:352 RemoteContainsCommit` + `git.go:542 BumpGitlink` (refuse bump to a sha not on origin) | `git_test.go:1239 TestRemoteContainsCommit` |
| `ReclaimBranch` GCs bee tip | action | runner branch reclaim after merge | — |
| `PointerDurable` | invariant | `pinPointerToTrackedTip` + `BumpGitlink` guard; `docs/submodule-pointer-invariant.md` | `SubmodulePointer_buggy.cfg` proves dangling reachable without the fix |
| `PointerIsTrackedTip` | invariant | `pinPointerToTrackedTip` | `TestWorkPinsPointerToTrackedTipDespiteAgentBeeBump` |

**Not yet modeled here (belongs to Layer 2):** the ambient-pointer false-DONE race
(`743b1c6`, `bafd386`) — `swarm.go:2566 recordReviewedCommit` and `:2692
finalizeIfAlreadyMerged` must read the task's OWN `bee-<taskid>` tip, never the
ambient `HEAD:submodules/<sm>/repo` gitlink. That crosses task-status + review, so
it is a Layer-2 property (`NoFalseDone`), noted so the gap is explicit rather than
silently absent.

## Planned layers (roadmap)

### Layer 2 — `TaskLifecycle.tla`
Status state machine (the exhaustive edge list in `HONEYBEE.md`), claim /
heartbeat / TTL, selection re-confirm, lost-work self-heal, DONE-gates.
- `LegalTransitionsOnly` — only the sanctioned edges fire.
- `AtMostOneLiveClaim`, `NoDuplicateDispatch` — `301964d` (mid-turn heartbeat
  keepalive + decoupled selection staleness + pre-dispatch re-confirm).
- `NoFalseDone` — DONE ⇒ the task's own bee-tip is committed, merged into tracked,
  and reviewed. Covers `fe6da39` (empty bee-branch), `743b1c6`/`bafd386` (ambient
  false-DONE).
- liveness `NoStuckDoomedTask` — a NEEDS-REVIEW/ARBITRATION task whose work is gone
  everywhere eventually returns to TODO. Covers `4fdd953`, `743c46f`.

### Layer 3 — `EditorSessionNamespace.tla`
The three `edit-*` subsystems sharing `.worktrees/` and the Manager's reclaim/gc
dance (`internal/editor/editor.go`: `editBranchPrefix = "hive-edit-"` at `:73`,
`isEditBranch` `:421`, `Reload` `:458`, `Reclaimable` `:634`).
- `NoForeignReclaim` — the Manager never adopts/deletes another subsystem's
  worktree. Covers `b08c995`.
- `LiveSessionNeverReclaimed`, `SessionDurable` — covers `b08c995`, `c64efe7`.

## Caveats (how much confidence this actually buys)

- **No auto Go↔spec refinement.** This mapping is manual; budget to maintain it or
  the spec silently diverges from the code (the classic "spec goes stale" failure).
- **Byzantine-agent modeling is a feature, not a limitation.** The LLM interior is
  unmodelable; it is modeled purely by worst-case git *effects* (may leave work
  uncommitted, may stage a bee-tip gitlink). The specs then prove the
  runner/hook/pin defends the invariant *regardless* — the general form of the
  existing regression tests.
- **TTL is wall-clock**, abstracted (in Layer 2) to a nondeterministic
  `DeclareStale` adversary action; this over-approximates timing and so is a safe
  (never-too-optimistic) model of the claim race.
- **State explosion is the ceiling.** Tiny constants (2 submodules, 2–3
  tasks/artifacts) already surface every catalogued Layer-1 bug; symmetry / small
  bounds keep Layers 2–3 tractable.
- **A spec only checks what you tell it.** If an invariant is missing or the model
  is unfaithful, TLC gives false confidence. Every invariant here traces to a real,
  historical, understood failure — that is the discipline that keeps the model
  honest.
