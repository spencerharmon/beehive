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
| **2. Task lifecycle** | `TaskStatus.tla`, `ClaimRace.tla`, `DependencyReadiness.tla` | status state machine + the DONE-gates (durability + definition-of-done check) + claim/heartbeat/TTL concurrent-dispatch mutual exclusion + lost-work self-heal + dangling-dependency refusal | **done** |
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

### `DependencyReadiness.tla`
Dependency readiness + the dangling-dependency refusal (`92d2ed1`, faithful to
`internal/swarm/swarm.go taskYieldedBlocked`). A work pass may yield its task TODO
held on a blocking dep, but only if the dep is a **real, existing task**.

- `HeldImpliesRealDep` — a held (accepted-yield) task always has a real dep; a
  phantom dep is never silently held.
- `EventuallyResolved` — liveness: the task never wedges forever (real dep →
  `DONE`; phantom dep → escalated `NEEDS-HUMAN`).

Configs:
- `DependencyReadiness_fixed.cfg` — phantom-dep refusal on. No error.
- `DependencyReadiness_buggy.cfg` — any blocked yield accepted (pre-**`92d2ed1`**):
  a task yielded on a phantom dep is silently held forever —
  `flux:phantom-…-repin` wedged on the nonexistent `jellyfin:jellyfin-image-build`
  — `HeldImpliesRealDep` (and `EventuallyResolved`) violated.

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
