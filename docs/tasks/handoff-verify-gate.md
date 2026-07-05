# swarm: runner-owned mechanical verify gate before the NEEDS-REVIEW handoff

## Problem

Audit `session-audit-001` (F3) found whole review / arbitration / rework sessions
spent rejecting **mechanical** regressions — unformatted code, a `go vet` break, a
failing `go test` — that a deterministic check catches without an LLM. The retry
trend was 1.50 retries/delivered task (e.g. `session-metrics-extract` 5 sessions/245
turns, `links-graph-enforcement` 3 sessions/218 turns). Nothing ran between a Work
task's `TODO -> NEEDS-REVIEW` flip and publish, so a mechanically-broken worktree
reached a reviewer whose tokens should judge **design**, not re-run mechanics.

## Fix

Add a runner-owned gate the code worktree must pass BEFORE a Work task's
`NEEDS-REVIEW` flip may publish (`internal/swarm/verifygate.go`):

- **`Runner.VerifyGate func(ctx, dir, env) (ok bool, report string, err error)`** — an
  injectable seam. `cmd/honeybee` wires `DefaultVerifyGate`; **nil is INERT** (the
  historical handoff path is byte-identical, so every pre-existing completion test
  holds; tests inject stubs).
- **`DefaultVerifyGate`** runs `gateSteps()` — `gofmt -l .` (a non-empty listing is a
  failure), `go vet ./...`, then `go test ./...` — in order, in the worktree dir, with
  `Runner.BuildEnv` (the mandated static host env — `CGO_ENABLED=0` + a redirected
  `GOCACHE`/`TMPDIR`, from dep `runner-build-env`) layered over `os.Environ()`. A
  worktree with **no `go.mod` passes trivially** so a non-Go target is never blocked by
  a check it cannot run.
- **Never `go test -race`.** The static `CGO_ENABLED=0` host cannot run the race
  detector, so a bare `-race` would fail the gate for an *environment* reason, not a
  code one. The static list is pinned by `TestGateStepsAreStaticNoRace`.
- **`gateHandoff`** is invoked at BOTH Work completion sites (the main per-turn path
  and the defensive `ErrResolved` heartbeat path) once `complete()` reports done. It
  is inert (pass, "") unless ALL hold: the seam is wired, `sel.Kind == Work`, a code
  worktree exists, and the local PLAN status is exactly `NEEDS-REVIEW`. A direct
  `DONE` / `NEEDS-ARBITRATION` / `NEEDS-HUMAN` is not the mechanical-handoff case and
  is never gated (`NEEDS-HUMAN` especially is the blocker escape hatch).
- **GREEN** -> `recordVerifyPass` writes a durable marker under the submodule `docs/`
  (so a reviewer sees mechanics already passed) and normal completion/publish proceeds.
- **RED** -> `revertHandoff` rewrites `NEEDS-REVIEW -> TODO` and commits **only**
  `PLAN.md`, preserving the task's `session`+`heartbeat` claim (so the per-turn
  heartbeat keeps the task held and the red flip never reaches `main`); the failing
  output is handed back as the next prompt so the **same session fixes forward**.

### Fail-open on infra, fail-closed on a real red

The `error` return is reserved for an INFRASTRUCTURE fault *running* the gate (a step
that could not START — a missing toolchain — or an unreadable worktree), distinct from
a clean red (a vet/test regression is `(false, report, nil)`). An infra fault fails
**OPEN** (allow the handoff, WARN to the journal) so a runner-side toolchain fault
never wedges an otherwise-complete handoff. A clean red fails **CLOSED**. If the
revert itself fails, the handoff is refused anyway (never publish an ungated
`NEEDS-REVIEW`) and the stale claim -> GC re-drives.

### Why gate both completion sites

`complete()` is reached from the main per-turn path (primary) and, defensively, from
the `ErrResolved` heartbeat path (a flip that landed last turn with its doc arriving
only now). Both must gate so neither can publish an ungated `NEEDS-REVIEW`; on the
`ErrResolved` path a red `break`s out of the switch and falls through to this turn's
`Prompt`, so the same session fixes forward immediately.

## Tests (`internal/swarm/swarm_test.go`)

- `TestGateStepsAreStaticNoRace` — the command list is gofmt/vet/test and never `-race`.
- `TestVerifyGateGreenAllowsHandoff` — a green stub lets the Work task publish and reach
  `NEEDS-REVIEW`.
- `TestVerifyGateRedWithholdsAndFixesForward` — a red stub reverts `NEEDS-REVIEW -> TODO`
  (claim preserved), hands the failure back as the next prompt, and the same session
  re-completes once green.
- `TestVerifyGateInertWhenUnwired` — a nil seam is the byte-identical historical handoff.
- `TestRevertHandoffPreservesClaim` — the revert commits `PLAN.md` only and round-trips
  `session`+`heartbeat`.
- `TestDefaultVerifyGate{NoGoModPasses,UnformattedFails,FailingTestFails,CleanPasses}` —
  the production gate over real `go`/`gofmt` on temp modules (`CGO_ENABLED=0`, a `go
  1.21` module to avoid a toolchain fetch), skipped only when the toolchain is absent.
