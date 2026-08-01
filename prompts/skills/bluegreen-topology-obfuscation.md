# Skill: Blue/green (rollout topology) is an orchestration concern, not a chart concern

> Use when: a target needs a blue/green (or canary, or any multi-instance rollout /
> cutover) deployment, and you are tempted to bake colors, roles, or "active/idle"
> into the workload's Helm chart. Don't. Keep the chart a generic single-instance
> workload; let the **controller repo** (Flux/Argo/etc.) and the **beehive layer**
> own the topology and the cutover. This skill is the division of responsibility.

## The trap

A workload needs zero-downtime cutover, so the obvious move is to teach its Helm
chart about blue/green: a `color` value, a `colorLabel`, a `roles.active`/`.idle`
block, a "shared" grouping that one color owns, hostnames that swap on a flip. Now
the chart:

- can't be reused for any workload that isn't blue/green,
- can't be `helm template`-tested in isolation without inventing a color,
- accumulates deployment-topology logic (which color owns the apex host, which is
  idle) that has nothing to do with *running the workload*,
- forces every rollout-strategy change to be a **chart version bump** — the slowest,
  most blast-radius-heavy way to change a deployment.

Blue/green is a property of **how you roll the thing out**, not of **the thing**.
The workload is identical in blue and green. So the chart must not know its color.

## The obfuscation: three layers, three jobs

| Layer | Owns | Knows about blue/green? |
|-------|------|-------------------------|
| **Workload chart** (generic Helm chart, its own repo) | One single-instance workload: Deployment/Service/Ingress/PVCs/Config, each an independent `components.*` enable/disable toggle. Instance identity via a `nameSuffix`. Cross-instance singletons referenced **by fixed name**. | **No.** No color, no role, no active/idle. |
| **Controller repo** (Flux/Argo GitOps) | N releases of that ONE chart — one per instance — each supplying `nameSuffix`, its toggles, and its hostnames. Which instance carries the *active* (apex) host vs the *idle* (dev) host. The cutover = editing this layer. | **Yes** — this is where "blue" and "green" are real. |
| **Beehive layer** (orchestration: scripts, Actions, ROI/PLAN) | The flip *mechanism* and its safety (quiesce/sync/promote/verify), which target maps to which role, when a cutover is allowed (schema/DB gates). | **Yes** — drives the cutover, reviewed. |

The chart is published and version-pinned; the controller and beehive layers move
fast and independently. A rollout change (flip, add a color, change which host is
active) touches only the controller/beehive layers — **never a chart release**.

## Chart contract (what makes a chart "generic enough")

1. **Every component is an independent toggle.** `components.{workload, storage,
   state, warmupCache, config, sidecarX, ...}`, all defaulting `true`. A single
   default install renders the whole stack; a split install turns some off. No
   grouping like `components.shared` that secretly means "three unrelated things."
2. **Instance identity is a single `nameSuffix`.** Suffixed resources
   (`foo-<suffix>`) are per-instance; the workload selector is
   `app.kubernetes.io/instance`-scoped so two instances never cross-serve.
3. **Singletons shared across instances are referenced by a FIXED name**
   (`warmup.pvcName`, `config.configMapName`), not derived from the suffix — so
   instance A's Deployment and instance B's Deployment both mount the one PVC/ConfigMap
   that exactly one release *renders*. Ownership of a singleton belongs to exactly one
   release (see gotcha 1).
4. **No role, no color, no hostname-swap logic.** The chart takes `hostname` +
   `ingress.extraHosts` as plain values. *Which* host is the active apex is the
   controller's business: it just lists that host under whichever instance is active.
5. **The chart renders and lints with zero orchestration inputs.** If `helm template`
   needs a color to work, the abstraction has leaked.

## Controller layer (the composition)

- One HelmRelease/Application **per instance** (e.g. `-blue`, `-green`), same chart +
  version, differing only in `nameSuffix`, `components.*`, and hostnames.
- The **singletons** (shared cache PVC, tuning ConfigMap, a shared auxiliary
  Deployment) are turned ON in exactly ONE release and OFF in the others; the others
  reference them by the fixed name. A common shape is a third "singletons" release
  that owns only the shared pieces.
- **The flip is a controller-layer edit**: move the active hostname (and any
  `extraHosts`/role-CNAME) from the outgoing instance's release to the incoming one,
  and repoint the role CNAME. No chart change, no data motion in the common case.
- Select instances by `app.kubernetes.io/instance=<release>`, never by a `color`
  label (the chart no longer emits one).

## Beehive layer (the mechanism)

- The flip **script/Action** is a thin, reviewed executor over the controller edit:
  resolve roles → (optionally quiesce/sync — mark these DESTRUCTIVE, never
  "reversible") → promote (rewrite the active host) → verify. Keep it in the
  orchestration repo, not the chart.
- Encode *policy* here: when a cutover is allowed (DB/schema gates), which credential
  it reuses, which target is prod. ROI/PLAN carry intent; the script carries steps.

## Gotchas learned the hard way

1. **A singleton owned by one release but consumed by others + a lazily-bound volume
   = a readiness deadlock.** If the singletons release renders a
   `WaitForFirstConsumer` PVC (e.g. local-path) that only the *workload* releases
   mount, the controller's helm readiness wait on that release blocks forever whenever
   the workload instances are momentarily down during the singleton release's
   up(grade). Fix: `disableWait: true` on the owning release (its real readiness is
   its own workload's probe; the PVC binds on first consumer). Alternatively give the
   singleton a consumer of its own.
2. **Splitting a mono-release into per-instance releases churns helm ownership.**
   Helm keys resources by kind+name+namespace; changing which release "owns" a name,
   or removing a name from a release's manifest, makes helm **delete** it on upgrade.
   Deleting a shared ConfigMap/PVC mid-upgrade breaks the consumers and can wedge on
   volume finalizers. Expect it, sequence the cutover, verify prune results.
3. **Deployment selectors are immutable.** Keep the chart's workload selector stable
   (`app.kubernetes.io/name`+`instance`+`component`) across the refactor; do not fold
   a removed `color` into it or the upgrade fails on an immutable-field conflict.
4. **Never hardcode which color is prod in the chart.** Role is data in the
   controller layer (which host each release carries). A chart that assumes
   blue=active will fight the operator the day they cut over.

## The one-line test

If deleting the word "blue" from the chart repo requires changing anything but a
comment, the topology has leaked into the workload. Push it up to the controller and
beehive layers where it belongs.
