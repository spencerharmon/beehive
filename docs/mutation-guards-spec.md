# Diff-scoped mutation guards (GUARDS.md)

> Package: `internal/guards` · gate: `internal/swarm/guard.go` (`guardGate`, wired
> into `verifyGate` step 4.6) · CLI: `beehive guards lint <submodule>`.

A **mutation guard** refuses a *change* a task tries to land, based on **live
release state the runner cannot itself model**. Where a `Check:` (CHECKS.md) gates
whether the work is *done*, a guard gates whether the change is *allowed at all*.

## Why the runner stays strategy-agnostic

Observed 2026-08: a honeybee edited the ACTIVE blue/green color's deploy manifest —
upgrading prod in lockstep with dev — because it resolved "which color is active"
from a stale prose comment. The general lesson: what is "protected right now"
depends on the deployment strategy (blue/green, canary, rolling, waves) and on live
state (which color serves prod, whether a canary step is mid-flight). If the runner
hardcoded any of that it would support exactly one strategy.

So the runner encodes exactly one sentence — *"a proposed mutation, judged from a
trusted baseline against live release-state by strategy-owned policy code"* — and
nothing strategy-specific. Each strategy is a `GUARDS.md` stub + a `guards/` script
that fills three slots: the **protected path-glob**, the **live authority** to read,
and the **forbidden condition**. Adding a new strategy is a new registry + script
with ZERO runner changes — the same inversion CHECKS.md uses for test frameworks.

## Where it lives

`GUARDS.md` and the `guards/` scripts live **in the submodule repo** (versioned with
the code they protect), at the repo root. So the guard, the protected files, the
diff, and the tamper anchor are all one git history.

## Format

```
# Guards — <submodule>
<free-form header prose>

## <stub-id>
<description>
Protects: <glob>[, <glob> ...]
Command: <shell command, run from the baseline; exit != 0 = refuse>
```

Globs match repo-relative paths: `*`/`?` stay within a path segment, `**` spans
segments (`guards/**` matches everything under `guards/`). A stub missing a
`Protects:` glob or a `Command:`, or whose `Command:` is a trivially-passing no-op
(`true`/`:`/`echo`/`exit 0`), is a parse error — the analogue of CHECKS.md refusing
an empty-matching regex.

## When it runs

At the uniform handoff gate (`verifyGate`), for a **Work** pass, on the turn a
terminal flip is committed — behind the existing per-turn completion check, NOT a
per-turn watcher. The runner computes the committed diff of the pass branch vs the
merge-base it forked from and fires **only** the guards whose `Protects:` glob
intersects a changed file. A pass that touches no protected path runs no guard; a
submodule with no `GUARDS.md` has zero overhead. A violating Work change is blocked
here, before it can ever reach review.

## Tamper anchor — merge-base; trusted code / untrusted data

`GUARDS.md` and the guard `Command` are materialized from the **merge-base** (the
reviewed baseline the pass forked from), never the bee branch. A bee's edit to guard
code is therefore **inert for its own pass**; a changed/weakened guard governs only
later passes, after it lands and is reviewed. You never grade your own exam with a
key you edited mid-exam. (Pair this with a `guard-the-guards` stub whose `Protects:`
is `GUARDS.md, guards/**`, forcing guard-code changes into review.)

## The ABI the runner hands a guard

Environment (the guard is read-only; exit 0 = allow, non-zero + a message = refuse):

- `BEEHIVE_HONEYBEE=1` — a gate evaluation is by definition a honeybee pass. The
  sanctioned non-honeybee actor (e.g. an operator-gated flip) does NOT run through
  this gate, so it is never blocked.
- `BEEHIVE_GUARD_SUBMODULE`, `BEEHIVE_GUARD_ID`
- `BEEHIVE_GUARD_PROTECTS` — the stub's globs, newline-joined
- `BEEHIVE_GUARD_DIFF_FILES` — all changed files, newline-joined
- `BEEHIVE_GUARD_MATCHED_FILES` — the changed files THIS guard protects
- `BEEHIVE_GUARD_DIFF_PATCH` — path to the full patch (for hunk inspection)

The command runs in the `internal/checkpolicy` sandbox (denylist + bwrap + `~/.kube`
bound), from the baseline workspace.

## Authoring obligation — prove it refuses

A guard is enforcement, so its definition of done is *"provably rejects,"* not
*"compiles."* Every guard-authoring task's `Check:` must drive the guard four ways
against the LIVE authority and all four must hold:

1. a violating diff **under honeybee identity** → **refuse**
2. a clean / non-protected diff → allow
3. the same violating diff **without** honeybee identity (the sanctioned actor) → allow
4. verdict **flips when the resolved live release-state flips** — forbids hardcoding,
   `exit 0`, or ignoring the diff

A guard that cannot resolve its live authority must **refuse** (fail closed), never
allow. Register that `Check:` framework in CHECKS.md like any other.
