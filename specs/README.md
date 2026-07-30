# beehive protocol — TLA+ formal specifications

Formal, machine-checked models of the beehive coordination protocol. The goal is
**not** an executable artifact: TLA+ verifies that the *design* is correct
(exhaustively, over every reachable interleaving), and leaves behind a precise
reference contract you compare the Go implementation against line by line. See
`docs/formal-spec-mapping.md` for the `spec-action ↔ code-func ↔ test` mapping and
the actor model.

## Why this exists

beehive coordinates entirely through git state transitions between many
uncoordinated writers (honeybee passes, the runner, beehived's editor / resolve /
bootstrap subsystems, direct-on-primary CLI verbs, external pushes, and
operator-directed agents following the worktree skill). That is exactly the shape
TLC checks well, and most historical protocol bugs were **invariant violations on
shared git state** that only manifest under specific concurrent interleavings —
the cases unit tests miss. Each module below reproduces one or more *already-fixed*
defects as a counterexample trace (the bug cfg), and proves the fix holds across
every reachable state (the fixed cfg).

## Layering

Three layers, kept as separate modules (different refs, different state; keeping
them apart bounds the state space):

| Layer | Module(s) | Models | Status |
|-------|-----------|--------|--------|
| **1. Shared git refs** | `MainConvergence.tla`, `SubmodulePointer.tla` | `main` fast-forward convergence + submodule gitlink durability/tracked-tip | **done** |
| **2. Task lifecycle** | `TaskStatus.tla`, `ClaimRace.tla`, `DependencyReadiness.tla`, `CheckPolicy.tla` | status state machine + the DONE-gates (durability + definition-of-done check) + the DoD `Check:` command-layer policy (denylist) + on-disk-vs-published status/change-doc handoff-terminal-leak gating + claim/heartbeat/TTL concurrent-dispatch mutual exclusion + lost-work self-heal + dangling-dependency refusal | **done** |
| **3. beehived dances** | `EditorSessionNamespace.tla` *(planned)* | the three `edit-*` subsystems sharing `.worktrees/`, the reclaim/gc dance, remote session durability | planned |

## Layer 1 modules (this delivery)

### `MainConvergence.tla`
The two `main` anchors (local primary + remote) as fast-forward-only refs, with
every writer that touches them. Commits abstracted to the SET of artifacts they
contain; fast-forward = superset; a fork = two incomparable sets.

- `Reconcilable` — the two anchors always stay one-an-ancestor-of-the-other (no
  fork). **Leading safety property.**
- `NoSilentLoss` — no committed artifact ever drops from both anchors.
- `EventuallyConverged` — liveness: every writer's work reaches both anchors.

Configs:
- `MainConvergence_fixed.cfg` — idealized protocol (SyncMainFromRemote before
  author; pre-receive guard on). No error; converges.
- `MainConvergence_buggy.cfg` — direct-on-primary author without sync. Reproduces
  the **`b48b927`** fork: `Reconcilable` violated.
- `MainConvergence_forcerewind.cfg` — pre-receive guard off. Reproduces the
  **`f8e7828`** gap: an external non-ff force-rewind drops work, `NoSilentLoss`
  violated.

### `SubmodulePointer.tla`
The submodule gitlink as a shared, durable pointer.

- `PointerDurable` — the recorded gitlink is always resolvable on origin (never a
  GC'd sha).
- `PointerIsTrackedTip` — the gitlink is always exactly the tracked-branch tip.

Configs:
- `SubmodulePointer_fixed.cfg` — runner owns the gitlink, pins it to the tracked
  tip. No error.
- `SubmodulePointer_buggy.cfg` — agent bumps the gitlink to its bee tip; after
  merge + branch reclaim the sha is GC'd. Reproduces the **`1a9bcea` / `35442f4`**
  dangling pointer: `PointerDurable` violated.

## Layer 2 modules (this delivery)

### `TaskStatus.tla`
The task-lifecycle status state machine (faithful to `internal/plan/state.go`): the
legal edges, the attempts/limit escalation to `NEEDS-HUMAN`, the runner recovery
edges (`RecoverLostWork`, `FinalizeAlreadyMerged`), and the honeybee escalation
edge (`RequestHuman`).

- `LegalTransitionsOnly` — status only ever changes along a sanctioned edge.
- `NoFalseDone` — a task is `DONE` only when its own work is durable on origin
  **and** merged into the tracked branch **and** its declared definition-of-done
  `Check` is satisfied (`92d2ed1`). **Leading safety property.**
- `AttemptsBounded` — the rework counter never runs past the escalation point.
- `Terminates` / `LostWorkRecovers` — liveness: the task always reaches `DONE` or
  `NEEDS-HUMAN`, and lost work never strands (leads back to `TODO`/`NEEDS-HUMAN`).

Configs:
- `TaskStatus_fixed.cfg` — both DONE gates (durable-on-origin AND DoD check)
  enforced. No error; both liveness properties hold.
- `TaskStatus_buggy.cfg` — ungated handoff. Reproduces the silent false-DONE
  family (**`fe6da39` / `2573066` / `72e2b4a` / `743b1c6`**): a task reaches `DONE`
  on work that is not durable on origin — `NoFalseDone` violated.
- `TaskStatus_buggy_check.cfg` — durability gate on, DoD-check gate **off**
  (pre-**`92d2ed1`**): a task reaches `DONE` on real, durable, merged work whose
  declared acceptance `Check` is **not** satisfied — the
  `jellyfin:zuul-image-build-publish` false-DONE (reviewed config commit, image
  never pullable) — `NoFalseDone` violated.

**The handoff-terminal-leak** (2026-07-26 session mining, 31/68 gate-hitting
passes deadlocked to `MaxTurns`): `status` above is the AGENT-WRITTEN on-disk
status only (the worktree's `PLAN.md`); it is not what the selector/peers
observe. `published` is the separate status the runner's per-turn heartbeat
(`internal/claim` `Heartbeat` -> `CommitPaths(planRel())` -> `Publish`) actually
carries onto `main`, **every turn, independent of the handoff gate and of
whether the change doc exists anywhere**. `docWritten`/`docOnMain` model the
change-doc artifact the same way `workDurable`/`merged` model the code commit.
`CONSTANT RevertOverPin` selects the fix (`TRUE`: on gate-fail the runner
reverts the on-disk status back to its pre-handoff value instead of pinning it,
*and* the heartbeat's publish of a terminal status is itself gated on the
backing artifacts being present — "runner-owned doc/commit synthesis") vs the
pre-fix bug (`FALSE`: pin-to-terminal, `swarm.go` ~1205/~1401, plus an
unconditional heartbeat publish).

- `NoFalseHandoff` — a `published` `NEEDS-REVIEW` always has its change doc
  durably recorded on `main` too (extends the anti-false-DONE contract to the
  handoff itself, phrased on the sticky `docOnMain` latch so an adversarial
  `LoseWork` after a legitimate handoff doesn't retroactively violate it).
- `NoDoclessTerminal` — no terminal `published` status (`NEEDS-REVIEW` or
  `DONE`) ever reaches `main` without its change doc also being on `main`. This
  is the exact shape of the two real false-DONE leaks found in the
  2026-07-26 session mining: `beehive:active-state-live-poll` and
  `chris-agent:spec-lifecycle-state-machine` reached `DONE` on `main` with **no
  change doc**.
- `PublishConverges` — liveness: `published` eventually catches up with an
  earned on-disk terminal, so the revert loop (`GateCheck` reverting a pinned,
  unearned terminal) never livelocks — a task whose substance never actually
  appears still terminates (`DONE`-with-substance or `NEEDS-HUMAN` past
  `Limit`), it never spins forever re-flipping.

Configs:
- `TaskStatus_leak_fixed.cfg` — `RevertOverPin = TRUE` (durability + DoD-check
  gates also on). No error; `NoFalseHandoff`/`NoDoclessTerminal` and all prior
  invariants hold, and `PublishConverges` holds alongside `Terminates` /
  `LostWorkRecovers`.
- `TaskStatus_leak_buggy.cfg` — `RevertOverPin = FALSE` (durability + DoD-check
  gates left **on**, isolating the leak from the older false-DONE defects
  above): a terminal status reaches `published` with no change doc ever
  recorded on `main` — `NoDoclessTerminal` violated, with trace.

### `DependencyReadiness.tla`
The N-task dependency graph with cross-submodule links (faithful to
`internal/links/links.go`, `internal/select/graph.go`, and
`internal/swarm/swarm.go taskYieldedBlocked`). Tasks depend on each other via
`DepEdges`; a `to` id outside `Tasks` is a **phantom/dangling** dep
(`plan.Plan.DanglingDeps`) and a `t <-> t'` pair is a **wait cycle**
(`links.CyclicNodes`). Two independent guards toggle the broken vs fixed protocol:
`DepGuard` (phantom dep → escalate vs silent hold) and `CycleGuard` (cycle →
escalate vs silent hold).

- `PhantomNeverHeld` — a held (accepted-yield) task never has a phantom dep; a
  dangling/cross-link dep is refused exactly like a local one.
- `CycleNeverHeld` — a held task never lies on a dependency cycle; a cycle
  escalates to `NEEDS-HUMAN` rather than silently deadlocking.
- `EventuallyResolved` — liveness: every task reaches a terminal (real deps →
  `DONE`; phantom or cycle → escalated `NEEDS-HUMAN`); the graph never wedges.

Scenario graphs (`Tasks3/5`, `EdgesFixed/Phantom/Cycle`) are overridden into each
cfg via `<-` because TLC cfgs cannot express `<<>>` tuple literals.

Configs:
- `DependencyReadiness_fixed.cfg` — both guards on. A normal chain resolves, a
  phantom dep and a cycle each escalate. No error.
- `DependencyReadiness_buggy.cfg` — phantom class, `DepGuard=FALSE` (pre-**`92d2ed1`**):
  a task yielded on a phantom dep is silently held forever —
  `flux:phantom-…-repin` wedged on the nonexistent `jellyfin:jellyfin-image-build`
  — `PhantomNeverHeld` (and `EventuallyResolved`) violated.
- `DependencyReadiness_cycle_buggy.cfg` — cycle class, `CycleGuard=FALSE`: two
  mutually-dependent tasks are silently held instead of escalating —
  `CycleNeverHeld` (and `EventuallyResolved`) violated.

### `ClaimRace.tla`
The commit-race claim protocol between two concurrent passes (faithful to
`internal/claim` + `internal/swarm` selection/dispatch).

- `AtMostOneLands` — the **correctness backstop**: two sessions never both complete
  the task (rests on the single-owner publish conflict). Holds in **every** cfg.
- `NoDuplicateDispatch` — the **efficiency** property the fix adds: two sessions
  never both get dispatched onto the same live task.
- `EventuallyLanded` — liveness: the task eventually lands.

Configs:
- `ClaimRace_fixed.cfg` — mid-turn heartbeat keepalive + decoupled selection
  staleness. No error.
- `ClaimRace_buggy.cfg` — no keepalive. Reproduces **`301964d`**: a live owner's
  heartbeat goes stale to selection and a second session dispatches on top of it —
  `NoDuplicateDispatch` violated. `AtMostOneLands` still holds (checked here to
  show the publish-conflict backstop survives the bug).

### `CheckPolicy.tla`
The DoD `Check:` command-layer policy (`internal/checkpolicy`), modeled as a
DENYLIST: the universe of runnable commands is owned by the agent runtime
(opencode) permission config; a check is a SUBSET of that universe, admitted iff
no command word is on the denylist AND the check is statically analyzable (else
refused, fail-closed). A check is a nonempty set of command words; TLC enumerates
every check over a small command universe.

- `NoDeniedAdmitted` — an admitted check never invokes a denied command.
- `SubsetOpencodeAllowed` — an admitted check's commands are a subset of what
  opencode permits.
- `FailClosed` — a check that cannot be statically analyzed is never admitted (no
  variable/eval/command-subst smuggles a denied command past the gate).
- `NoFakeOnlyAdmitted` — **anti-abuse, leading property:** an admitted check
  invokes at least one real framework; a check built only from source-inspection /
  no-op tools (grep/find/test/true) is refused.
- `RealFrameworkUsable` — **the denylist win:** a real-framework check opencode
  permits and that is not denied is admitted with NO positive per-tool allowlist
  entry (`go test` / `dotnet test` / `pytest` / `nix build` all work by default).

Configs:
- `CheckPolicy_fixed.cfg` — the denylist design; the denylist contains both the
  anti-abuse group (grep) and the code-smuggling/destructive backstop (rm). No
  error.
- `CheckPolicy_allowlist_buggy.cfg` — the pre-change ALLOWLIST design (admit only
  an enumerated positive set). `go test` is not enumerated, so a real, opencode-
  permitted, non-abusive check is refused — `RealFrameworkUsable` violated (the
  usability defect that forced per-tool config widening).
- `CheckPolicy_abusehole_buggy.cfg` — a denylist with a HOLE (the fake-test tools
  omitted): a bare source-`grep` check is admitted as a fake definition of done —
  `NoFakeOnlyAdmitted` violated (why `DefaultDeniedCommands` must carry the
  grep/find/test/true group).

### `EditorSessionNamespace.tla`
The beehived chat-diff editor Manager and its reclaim/gc dance over the shared
beehive-root `.worktrees/` dir (faithful to `internal/editor/editor.go`). Three
protections, each a CONSTANT toggle guarding one invariant.

- `NoForeignReclaim` — the Manager never reclaims/adopts a foreign subsystem's
  bare `edit-*` worktree (it owns only the private `hive-edit-` prefix).
- `LiveSessionNeverReclaimed` — a session an operator has open (in `byID`) is
  never reclaimed, whatever its record age or worktree cleanliness.
- `SessionDurable` — a session with an unpublished pending edit is always
  recoverable (local ref preferred, else the trusted-remote copy).

Configs:
- `EditorSessionNamespace_fixed.cfg` — all three protections on. No error.
- `EditorSessionNamespace_buggy_namespace.cfg` — bare `edit-*` namespace
  (**`b08c995`**, capture half): reclaims a foreign worktree — `NoForeignReclaim`
  violated.
- `EditorSessionNamespace_buggy_liveguard.cfg` — no live guard (**`b08c995`**,
  wipe half): the gc dance deletes an open-but-idle, stale-record session —
  `LiveSessionNeverReclaimed` violated (the 404-next-turn symptom).
- `EditorSessionNamespace_buggy_remote.cfg` — no trusted-remote push
  (**`c64efe7`** pre-fix): a pending session that loses its local worktree is
  unrecoverable — `SessionDurable` violated.

## Running

Needs Java and `tla2tools.jar` (https://github.com/tlaplus/tlaplus/releases).

```sh
# assert every fixed cfg passes and every bug cfg reproduces its defect:
TLA2TOOLS=/path/to/tla2tools.jar specs/run-tlc.sh

# or a single case:
java -cp tla2tools.jar tlc2.TLC -config specs/MainConvergence_buggy.cfg \
    specs/MainConvergence.tla
```

`run-tlc.sh` encodes the contract: fixed cfgs MUST report "No error has been
found", bug cfgs MUST report an invariant "is violated". It exits non-zero if any
spec stops behaving as declared — wire it into CI so the spec cannot silently rot
away from the invariants it claims to lock.

## Scope (deliberate)

**In:** shared-git-state races between uncoordinated writers — fork/silent-loss,
gitlink durability, and (Layers 2–3) claim races, lost-work recovery, task-status
legality, and the beehived editor-namespace reclaim dance.

**Out, on purpose:** the LLM agent's interior reasoning (modeled only by its
worst-case git *effects* — a byzantine writer); git tree/merge internals and
byte-level content (artifacts are opaque; merge is set-union; ff is containment);
`PLAN.md` parsing; selection RNG; systemd/opencode scheduling; and content-level
*merge correctness* / conflict-resolution *quality* (that is agent correctness,
which the honeybees own — see `docs/runner-protocol-vs-correctness.md` — not
protocol).

See `docs/formal-spec-mapping.md` for the caveats that bound how much confidence
these specs actually buy, and the code-to-spec mapping.
