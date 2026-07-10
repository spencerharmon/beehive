# deferred-verification-skill

Author `prompts/skills/deferred-verification.md` (renders to
`skills/deferred-verification.md`) and index it in `prompts/AGENTS.md`'s skill
table.

## Why

Passes hit a recurring anti-pattern: a work task whose change is correct at commit
time but only observable after an external system converges out-of-band (a GitOps
controller reconciling, a CI pipeline firing, a cache/TTL expiring). Agents wrongly
escalated `NEEDS-HUMAN` ("not verifiable in-pass") when no human input was needed.

## Decision rule shipped in the skill

1. **Default — verify in-pass by polling** for convergence while keeping the
   watchdog alive (real progress re-stamps the heartbeat; tickling it while waiting
   on a bounded external process is fine).
2. **Fallback — land + defer** ONLY when the wait may exceed the watchdog and
   there's no real work to interleave: land the change, file a distinct follow-on
   validation task (`deps=` the landing task, optionally `not_before=` past expected
   convergence once that stamp exists) that re-defers rather than blocks if still
   unconverged.
3. **NEEDS-HUMAN reserved** for genuine human input (credential, out-of-GitOps
   operator action, intent/scope decision) — never merely because verification is
   async.

## Relationship to other work

Companion to `runner-not-before-stamp` but NOT gated on it: degrades gracefully
(drop `not_before=`, rely on `deps=` + re-defer) if that stamp isn't shipped yet.

## Motivating anti-pattern

`flux:zuul-beehive-tenant-config-stale` — escalated `NEEDS-HUMAN` on "flux-gated,
not verifiable in-pass"; cited in the skill as the worked wrong example.

## Files

- `prompts/skills/deferred-verification.md` (new, auto-embedded via `skills/*.md`).
- `prompts/AGENTS.md` — one new skill-table row indexing it (honeybee audience).
