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
| **2. Task lifecycle** | `TaskLifecycle.tla` *(planned)* | status state machine + claim/heartbeat/TTL + selection re-confirm + lost-work self-heal + the DONE-gates (incl. ambient-pointer false-DONE) | planned |
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
