# Sharing modes & the startup preflight guard

Beehive components (`honeybee`, `beehived`) converge through git. How they
converge depends primarily on **the beehive repo they are pointed at** — a
component discovers its mode at runtime from the repo's git remotes, and must
behave correctly and tolerantly in either. The one knob that tunes this is
`abort_on_remote_failure` (below): whether an unreachable remote at preflight is
fatal or degrades the pass to local-only.

## The two modes

### Local sharing (no remote configured)
Multiple components may run on the **same filesystem, sharing one checkout**.
There is no remote to push to or pull from; convergence happens in-place by
publishing to the checked-out `main` (`receive.denyCurrentBranch=updateInstead`).

**Invariant: `main` must stay a clean projection of committed history.** Every
component authors only inside its own private worktree and converges by
merging/pushing to `main`; nothing writes the shared checkout directly. Because
the tree is pure projection, **any drift in it is a bug** — in the honeybee
protocol (agent instructions), in the honeybee or beehived process, or a rogue
model writing outside its worktree.

Resetting the checkout to `HEAD` is **always safe and always allowed**, but it is
**always warned**, because a reset only ever happens when something upstream
already misbehaved. A silent reset would hide a real defect.

### Remote sharing (one or more remotes configured)
A component has its **own private checkout** and converges only through git
remotes; no other component is assumed to share its filesystem. A swarm may be
**hybrid**: some components local-sharing, some remote-sharing, simultaneously.

- **Push to *all* configured remotes.** A change is not durably shared until every
  target has it.
- **Pull only from the default target** (remote name `origin` unless the repo
  names another). One source of truth for "what main is"; the other remotes are
  publish mirrors.
- **Push and pull failures are fatal by default.** Any work an agent does while
  it cannot pull (catch up to main) or cannot push (share its result) is
  *invalid*. On such a failure the component **ends all LLM activity immediately**
  — it does not keep spending tokens on work that cannot land. This default is
  overridable for the startup-preflight case via `abort_on_remote_failure: false`
  (see below), the deliberate escape hatch for a remote outage.

> Implementation status: pull-from-default and single-remote push are implemented
> and their failures are fatal (startup fetch, and the per-turn `taskRemoved`
> check). **Push to *all* remotes is specified here but not yet implemented** —
> today a single default remote is used. Tracked as follow-up
> (`multi-remote-push`); until then, remote sharing assumes one remote.

## The startup preflight guard

Before a honeybee starts its (token-costly) agent, it runs a preflight that would
have turned the 2‑day silent wedge into an immediate, repeating, free error:

1. **Detect mode** from `git remote` — no config. Remote present ⇒ remote sharing;
   none ⇒ local sharing.
2. **Ensure a clean, publishable checkout** (`Repo.EnsureCleanCheckout`):
   - clean tree ⇒ proceed (cost: one `git status`);
   - dirty but resettable ⇒ reset to `HEAD` + resync submodules, then **WARN**
     (the drift is a bug signal, per the local-sharing invariant — the warning
     applies in both modes);
   - **cannot be made clean** (filesystem error; or drift that survives a reset,
     e.g. an orphan gitlink that wedges `git submodule update`, or stray untracked
     files) ⇒ **abort before starting the agent**. No tokens are spent, and the
     error is loud in the logs.
3. **Validate the pull target** (remote sharing): the startup `fetch` of `main`
   must succeed; a failure aborts the run before the agent starts — **unless**
   `abort_on_remote_failure: false` (see next section), which turns that abort
   into a local-only degrade.

## `abort_on_remote_failure` — outage escape hatch

A configured remote (e.g. a gitea backup) that is **unreachable at a honeybee's
startup preflight** is, by default, fatal: with a persistent outage *every* pass
aborts, so the whole swarm halts until the remote is healthy. That is correct
when the remote is the shared source of truth, but wrong when it is only
corruption-insurance for an otherwise single-host hive — there, a remote outage
should not stop honeybees from working locally.

The `abort_on_remote_failure` config knob (default `true`) governs this one
decision:

- **`true` (default):** unreachable-at-preflight remote ⇒ **abort** the pass
  before any tokens are spent (the historical behavior above).
- **`false`:** unreachable-at-preflight remote ⇒ **degrade** the pass to
  local-only convergence for its entire lifetime. It treats the remote as absent
  (publishes to the local checked-out `main` via `updateInstead`, does no remote
  fetch/push), logs a `WARNING`, and proceeds. Honeybees keep working while the
  remote is down.

Only the **startup preflight** consults the flag. A remote that is reachable at
preflight but fails **mid-pass** still fails that individual pass (which is then
retried by stale-claim GC), independent of this flag.

**Operating it:** flip `abort_on_remote_failure: false` during a remote OUTAGE,
flip it back `true` once the remote is healthy. During a *full* outage every pass
degrades uniformly, so convergence is pure local-sharing (the documented
single-host mode) with no divergence; the remote's replica is re-synced by the
backup push (or the next healthy pass) on recovery.

> **Caveat — partial/flaky connectivity.** The clean, no-divergence guarantee
> holds only when the remote is uniformly reachable or uniformly down. If it is
> *intermittently* reachable while the flag is `false`, some passes degrade to
> local-only (advancing local `main`) while others still converge through the
> remote — local `main` and the remote can then diverge until a force-push backup
> re-reconciles them. Treat the flag as a deliberate outage toggle, not a
> permanent flaky-tolerant mode.

Why this matters: the wedge that stalled the swarm for two days was a dirty shared
checkout (an orphan worktree gitlink) that every pass ran a full LLM turn against
and only discovered at the final publish. The preflight makes that class of
failure **fail fast, for free, and visibly** — a honeybee refuses to start and
says why, within one 7‑minute cycle, instead of burning tokens invisibly for days.

## What operators/agents should take from this

- Never author in the live shared checkout; use a worktree and converge to `main`.
  (See the "Edit on a shared checkout" skill.)
- A `preflight reset ... to HEAD` **WARNING** in the logs is not noise — it means a
  component or the protocol left drift behind. Investigate it.
- A `preflight: ... cannot be reset` or `cannot pull ... main` **ERROR** means a
  honeybee refused to start; the swarm is intentionally not making (invalid)
  progress until the checkout/remote is healthy. If the remote is only a backup
  (not the shared source of truth) and you need honeybees to keep working through
  its outage, set `abort_on_remote_failure: false` to degrade to local-only
  instead of aborting — then restore it to `true` once the remote is healthy.
