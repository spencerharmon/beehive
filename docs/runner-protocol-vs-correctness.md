# The runner verifies protocol; honeybees verify correctness

This is the load-bearing separation of concerns for the whole swarm. Every gate,
check, and prompt in the runtime must respect it. Read this before adding any
"the runner should also check…" logic — most such additions are the wrong layer.

## The rule

**The deterministic runner verifies adherence to the honeybee PROTOCOL. It never
judges whether the code a honeybee produced is CORRECT.**

- The **runner** owns the mechanical, language-agnostic facts of the protocol:
  a task was selected and its kind fixed; a claim is held and heartbeated; a code
  worktree was created; the agent left a terminal PLAN.md status; a change doc
  exists at the expected path; the agent's work is actually **committed** (the
  worktree is clean) so the merge to `main` carries it; the pointer is pinned to
  the tracked tip; the session transcript streamed. These are true or false by
  inspecting git and the beehive layer — no toolchain, no test run, no opinion.

- **Correctness** — does the code compile, do the tests pass, does the pipeline
  go green, does the change actually do what the task asked — is owned by the
  **honeybees**:
  - a **work** agent makes the change and PROVES it (writes and runs a regression
    test that fails without the change and passes with it, or records the manual
    verification when no automated test is possible), citing the evidence in its
    change doc;
  - a **review** agent re-verifies that evidence and the change against the task
    card and ROI, approving only on carried proof;
  - an **arbitration** agent breaks a review tie.

  Honeybees determine correctness using whatever the target actually needs —
  unit tests, integration tests, build-pipeline results — as described in the
  target's `INFRASTRUCTURE.md`, the install's `LOCALS.md`, and the submodule's
  own `AGENTS.md`/`RULES.md`. The runner does not and cannot know a submodule's
  language or toolchain, and must not assume one.

## Why

1. **The runner is polyglot by necessity.** Targets under the swarm are Go, zuul
   YAML + ansible (flux), more zuul config (gostream), Nix, shell, whatever comes
   next. A correctness check the runner runs would have to special-case each
   toolchain. Any such branch (`if go.mod { gofmt; go vet; go test }`) silently
   does NOTHING for every target that is not that language — which is exactly how
   a broken/empty change can sail through: the check that was supposed to catch it
   never ran. A protocol check (`is the worktree clean?`, `does the doc exist?`)
   is meaningful for every target identically.

2. **Correctness is a judgment; the protocol is a fact.** "These tests are the
   right tests and they pass on live inputs" is a judgment that needs the task
   context, the ROI, and the domain — that is what the work/review/arbitration
   agents are for. "There is an uncommitted file in the worktree" is a fact. The
   runner is deterministic infrastructure; it deals only in facts.

3. **Putting correctness in the runner creates a false floor.** If the runner
   appears to vouch for correctness, reviewers relax — and the runner's guarantee
   is only ever as good as the one language it happened to hardcode. Keeping
   correctness entirely with the honeybees keeps the responsibility where the
   context is.

## What this means in code

- The handoff gate (`internal/swarm/verify.go`) runs exactly one thing on the
  `TODO -> NEEDS-REVIEW` handoff: `git status --porcelain` in the code worktree,
  and blocks the handoff if it is dirty (uncommitted work would be dropped by the
  merge). It runs **no** `gofmt`/`go vet`/`go test` and inspects **no** go.mod —
  those were removed precisely because they were a language special-case doing the
  honeybee's job. See that file's header for the full rationale.

- The completion checklists (`workChecklist`, and the review/arbitrate/bootstrap/
  reconcile predicates in `completionChecklist`) read only the beehive layer —
  PLAN.md status, the change-doc dir, the ROI stamp. None runs target code.

- `BuildEnv` (config `build_env`) is a generic `KEY=VALUE` map the runner passes
  through to the agent so its build/test commands work on a host with a broken
  `/tmp` or a cgo-linker quirk (see `LOCALS.md`). It is host-environment
  passthrough, not a toolchain assumption: the runner sets the env and states it
  once in the prompt; it never decides what the agent builds or whether the build
  is right.

## History

An earlier iteration (audit `session-audit-001` F3) added a `gofmt`/`go vet`/
`go test` gate to the runner to stop reviewers burning sessions on mechanical Go
regressions. It was gated behind a `go.mod` stat, so it ran for the self-hosting
`beehive` submodule and silently did nothing for flux/gostream/every non-Go
target. On 2026-07-22 the operator ruled that correctness verification does not
belong in the runner at all, and the toolchain gate was removed, leaving only the
language-agnostic uncommitted-work protocol check. The review agents own the
correctness responsibility the gate had been partially and unevenly duplicating.
