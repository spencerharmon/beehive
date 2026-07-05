# runner-owned handoff verify-gate

## Problem

A Work honeybee finishes by flipping its task `TODO → NEEDS-REVIEW` and writing a
change doc; the runner (`internal/swarm`) then accepts that as complete and publishes
it, and a later Review pass judges the change. Nothing mechanical stood between the
flip and the reviewer: a worktree that no longer `gofmt`s, fails `go vet`, or has a red
`go test ./...` was handed to review exactly like a clean one. Audit `session-audit-001`
(finding F3) recorded whole review sessions — and the arbitration/rework churn a reject
triggers — burned rejecting mechanical regressions a deterministic check catches in
seconds. That reviewer time is the scarce resource; spending it on `gofmt` is waste.

## Fix

Add a runner-owned **mechanical gate** the code worktree must pass BEFORE a Work task's
flip to `NEEDS-REVIEW` is accepted as complete. It is the runner's responsibility (the
deterministic wrapper owns completion checks), not the agent's role, so it lives in
`internal/swarm` beside `complete()`.

**`internal/swarm/verify.go`** (new):

- `verifyGate(ctx, sel, wtAbs) (string, error)` — returns `""` when the gate PASSES or
  is INAPPLICABLE, a non-empty fix-forward prompt on a RED, or an error when a check
  could not be run. Applicability, checked in order: the pass is `Work` with a code
  worktree; re-reads `PLAN.md` and the task is specifically `NEEDS-REVIEW` (a
  `NEEDS-HUMAN` escalation or `NEEDS-ARBITRATION` conflict is a different handoff the
  ROI does not target and must not be trapped; a direct-to-`DONE` Work pass is likewise
  out of scope); and `wtAbs/go.mod` exists (no go.mod ⇒ not a Go module ⇒ nothing to
  verify, so non-Go targets never false-red). It then runs, cheap→expensive, fail-fast:
  1. `gofmt -l .`  — red if it exits non-zero OR prints any path (a listed/unparseable file)
  2. `go vet ./...` — red on non-zero (also proves it compiles)
  3. `go test ./...` — red on non-zero (suite green on LIVE inputs)
  The checks run **in the worktree** and inherit the process env, into which
  `exportBuildEnv` has already applied the host-mandated static Go env
  (`CGO_ENABLED=0`, …) before the turn loop — so it is the static invocation, **never**
  `go test -race`, and buildenv.go stays the single source of that env (no re-plumb).
- `runVerify` / `realRunVerify` / `verifyOutcome` — a one-command exec seam.
  `realRunVerify` classifies: clean exit = green; ran-but-exited-non-zero =
  red (`exitErr`); could-not-execute = a returned infra error.
- `gateFailPrompt` — the fix-forward continue prompt: what failed, that the task stays
  claimed and is NOT handed to review, and the (tail-capped, `gateVerifyOutputCap`)
  command output to act on.

**`internal/swarm/swarm.go`** (completion path, ~4 edits):

- New `Runner.RunVerify` field — the injectable seam. `nil` ⇒ `realRunVerify`, so the
  gate is ON by default and cannot be silently disabled; tests set it to force
  red/green and assert the exact static invocation.
- After `complete()` reports `done` for a Work pass, call `verifyGate`. An infra error
  is **fail-closed** (`finish("")` + return the error — block completion). A RED sets
  `done = false`, stashes the failure in `gateHint`, and **pins `sel.Task.Status =
  plan.NeedsReview`** so the NEXT turn's heartbeat re-stamps `from == NEEDS-REVIEW`
  (on-disk status) instead of tripping `ErrResolved` on the terminal status we
  deliberately left in place. The claim — which is what keeps peer reviewers off a
  not-yet-ready `NEEDS-REVIEW` — thus stays fresh across the fix, and the ErrResolved
  publish path (Point A) is untouched.
- In prompt selection, a non-empty `gateHint` is the next prompt (one-shot; cleared
  after use so a later clean turn falls back to the normal lean/continue prompt).

Result: a red worktree keeps its claim and the agent fixes forward in the SAME session;
only a green (or inapplicable) gate lets the flip stand and publish. Orthogonal to the
publish-advance-guard (that guards the merge to main actually advanced; this guards the
worktree is mechanically clean before we ever get there).

## Tests (`internal/swarm/swarm_test.go`; `go test ./...` green, `CGO_ENABLED=0`)

- `TestVerifyGateGreenAllowsHandoffWithStaticInvocation` — acceptance: a clean worktree
  completes AND the gate ran exactly `gofmt -l .` / `go vet ./...` / `go test ./...`, in
  the code worktree, with **no `-race`**.
- `TestVerifyGateRedBlocksThenFixForwardCompletes` — a red `go test` blocks the handoff
  and the failure (incl. the `FAIL:` line) is fed back as the next prompt; once the gate
  goes green the same session completes.
- `TestVerifyGateRedNeverCompletes` — a persistently red gate never reports completion;
  it exhausts the turn cap, is GC-marked for retry, and each blocked turn re-feeds the
  failure.
- `TestVerifyGateSkipsNonReviewFlip` — a direct-to-`DONE` Work pass completes without
  the gate ever running (a red stub would block it if it did).
- `TestVerifyGateSkipsWithoutGoMod` — a worktree with no `go.mod` completes without the
  gate running.
- `TestRealRunVerifyClassifies` — the real exec path: clean exit green, ran-non-zero
  red, missing binary an infra error.
