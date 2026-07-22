# Skill: Deferred verification

> Use when: a work task's change only takes EFFECT after something else happens —
> a GitOps controller (e.g. Flux) reconciles the committed manifest, a CI pipeline
> fires and passes, a cache/TTL expires, OR a consumer must be restarted/reloaded
> to pick the change up. The code is correct the instant you commit; the observable
> result lags. It is NOT for ordinary "I'm unsure it works" — that is just testing.

## FIRST: is the wait PASSIVE, or must you ACT?

Before you defer anything, answer one question: **will the effect appear on its
own, or is the change applied but sitting inert until something is triggered?**

- **ACTIVE convergence — you must trigger it (do this IN-PASS, never defer/escalate).**
  The change is committed/applied but a consumer has not, and will NOT on its own,
  pick it up. The classic trap: a process that reads its config only at START-UP.
  Zuul reads `[connection ...]` blocks and tenant sources from `zuul.conf` **only
  when the scheduler boots** — add a connection, apply the ConfigMap, and it stays
  invisible FOREVER (no config-error, the projects simply never load) until the
  scheduler restarts. A Deployment reads a ConfigMap/Secret only at pod start; a
  reconciler may need a nudge. **No amount of polling converges these.** You have
  in-cluster kubectl and this IS your job (NOT an operator escalation — see the
  in-cluster-remediation rule in the relevant submodule's `INFRASTRUCTURE.md`):
  - DIAGNOSE the live consumer, don't assume. Query it: `curl .../api/connections`,
    `.../api/tenant/<t>/projects`, `kubectl -n <ns> get pod` (compare pod
    `startTime` against when the config landed — a consumer older than the change
    has never loaded it).
  - If the applied change is absent from the live consumer because it is stale,
    PERFORM the remediation: `kubectl -n <ns> rollout restart deploy/<name>` (for
    zuul, restart the whole stack — scheduler + web + merger + executor — so no
    surviving component serves a cached layout), force a reconcile, or trigger the
    pipeline. Then re-query and CONFIRM the effect is now live.
  - DOCUMENT the requirement: record the restart/trigger you ran in the change doc,
    and add it to the submodule `INFRASTRUCTURE.md` so the next honeybee doesn't
    re-derive it. Best of all, file/annotate the durable fix (e.g. a ConfigMap-
    checksum annotation on the Deployment so the manifest auto-rolls the consumer
    on config change) so the manual restart is never needed again.
  - Only if the triggering action is genuinely out of reach (host-root, a secret
    only the operator holds) does this become a real escalation — in-cluster
    kubectl never is.

- **PASSIVE convergence — poll and wait (the rest of this skill).** The change is
  applied AND the consumer will pick it up on its own within a bounded window: a
  Flux reconcile you triggered by committing, a CI run you enqueued, a TTL that
  will expire. Nothing more for you to DO but watch. Proceed to the decision rule.

When in doubt, look before you defer: a five-second `curl`/`kubectl get` that shows
the effect STILL absent long after the change landed is the signal it is ACTIVE,
not passive. Deferring an active case is the loop that stranded
`gostream-image-build-verify` for a dozen passes — every pass re-assumed "Zuul is
still loading the project" when the 2-day-old scheduler was never going to load a
connection added 13h after it booted.

## The decision rule (PASSIVE case)

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

`NEEDS-HUMAN` is reserved for genuine human input and MUST carry one of the four
`beehive task human --category` values: `secret` (a credential only the operator
holds), `external-permission` (an out-of-GitOps / out-of-cluster / host-root
action the swarm cannot perform — in-cluster kubectl is NOT this), `contradiction`
(irreconcilable ROI/PLAN intent), or `architecture` (a hard-to-reverse user-
visible decision). "The result is only observable after Flux reconciles / CI runs
/ the TTL expires" is NONE of these — it is exactly the in-pass-poll or land+defer
case above.

## Worked anti-patterns

`flux:zuul-beehive-tenant-config-stale` escalated `NEEDS-HUMAN` with the reasoning
"flux-gated, not verifiable in-pass". That was wrong: a Flux reconcile is a
bounded, pollable convergence, so the correct move was to land the manifest and
poll Flux's reconcile status in-pass (default), or — if the reconcile window
exceeded the watchdog — land it and file a follow-on validation task that
re-defers (fallback). Reaching for a human there stranded a task that needed no
human input at all. (Caveat: some zuul config changes ALSO need the ACTIVE step
above — a Flux reconcile applies the ConfigMap, but the scheduler must still
restart to read a new `zuul.conf` connection. Poll the LIVE consumer, not just the
Flux status.)

`gostream-image-build-verify` did the opposite mistake: it treated an ACTIVE case
as passive. It confirmed the registry had no image, reasoned "the GitHub connection
was just added and Zuul is still loading the project," then deferred `NEEDS-REVIEW`
and waited. Zuul was never going to load it — the scheduler predated the connection
and had to be restarted. The right move was to check `.../api/tenant/<t>/projects`,
see the project ABSENT, restart the zuul stack, confirm it loaded, then verify the
build — all in-pass. Deferring instead produced a dozen no-progress passes.
