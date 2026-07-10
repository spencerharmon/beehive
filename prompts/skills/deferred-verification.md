# Skill: Deferred verification

> Use when: a work task's change only takes EFFECT after an external system
> converges out-of-band — a GitOps controller (e.g. Flux) must reconcile the
> committed manifest, a CI pipeline must fire and pass, a cache/TTL must expire.
> This is the NARROW case where the code is correct the instant you commit but the
> observable result lags. It is NOT for ordinary "I'm unsure it works" — that is
> just testing.

## The decision rule

Verify **in-pass by polling** unless you genuinely can't, then **land + defer**.
Escalate to a human ONLY for real human input.

### Default — verify in-pass by polling for convergence

Land the change, then poll the external system for convergence within the same
pass while keeping the watchdog alive. Real progress re-stamps the heartbeat, and
deliberately tickling it while you wait on a *bounded* external process is fine and
expected — you are making genuine progress (confirming the deliverable), not
faking activity. Concretely:

- Commit/push the change so the external system can pick it up.
- Poll its status on an interval (the controller's reconcile status, the CI run
  result, the cache read after the TTL) until it converges or you hit a sane bound.
- On convergence: finish the task normally (`NEEDS-REVIEW`, doc, etc.).
- Prefer this whenever the expected wait fits comfortably inside the per-turn
  watchdog budget.

### Fallback — land the change, file a follow-on validation task

Use this ONLY when the wait may exceed the watchdog AND there is no real work to
interleave while waiting (you'd just be blocking on an unbounded external process).
Then:

- Land the change now.
- File a DISTINCT follow-on validation task in `PLAN.md` that `deps=` the landing
  task (so it can't run until the change is in), optionally `not_before=` a
  timestamp past expected convergence once that stamp is available in your install.
- Write that task to **re-defer rather than block** if it runs and the system still
  hasn't converged: it checks, and if not yet converged it re-files/re-defers
  instead of failing or spinning. Verification is thus decoupled from the landing
  pass without ever wedging a pass on an async wait.

This companions the `not_before` runner stamp but does not depend on it: if that
stamp isn't shipped yet, drop the `not_before=` and rely on `deps=` plus the
re-defer behavior — it degrades gracefully.

### NEVER escalate merely because verification is async

`NEEDS-HUMAN` is reserved for genuine human input: a missing credential, an
out-of-GitOps operator action, an intent/scope decision. "The result is only
observable after Flux reconciles / CI runs / the TTL expires" is NOT a human
blocker — it is exactly the in-pass-poll or land+defer case above.

## Worked anti-pattern

`flux:zuul-beehive-tenant-config-stale` escalated `NEEDS-HUMAN` with the reasoning
"flux-gated, not verifiable in-pass". That was wrong: a Flux reconcile is a
bounded, pollable convergence, so the correct move was to land the manifest and
poll Flux's reconcile status in-pass (default), or — if the reconcile window
exceeded the watchdog — land it and file a follow-on validation task that
re-defers (fallback). Reaching for a human there stranded a task that needed no
human input at all.
