# chat-skills: named, invocable maintenance skills on the frontend

## Problem

The beehive frontend surfaced maintenance state but gave the operator no way to
*act* on it. The hive-hygiene panel (`internal/web/hygiene.go`) scanned for git
cruft (stale worktrees, orphan gitlinks, drifted checkouts, unexpected remotes)
and pointed at a `beehive-hygiene` cleanup skill, but that skill was a doc the
operator ran by hand — the UI never invoked anything. The editor already knew how
to reclaim abandoned edit worktrees at startup (`editor.Manager.Reload`), and the
typed artifacts model (`internal/artifacts`) already knew the blue/green deploy
markers, but neither was reachable as a deliberate, on-demand action. There was no
common contract for "named action with a safe dry-run and a gated apply".

## Design

A single skill registry exposes named maintenance actions, each with a
deterministic read-only **dry-run** (a plan of exactly what it would change) and a
separate **apply** that performs precisely that. The registry — not each skill —
enforces the invocation contract, so the four acceptance guarantees hold
structurally rather than per-skill.

### 1. `internal/web/skills.go` — the registry and contract

`skillRegistry{order, byName}` is rebuilt per request (cheap: just closures over
`*Server`) so every plan is a live scan. A `skill` carries identity
(Name/Title/Summary), two flags (`Destructive`, `ReportOnly`), and `plan`/`apply`
closures. The registry is the single lookup/dispatch point:

- `lookup(name)` → `errUnknownSkill` (wrapped with the name) when absent.
- `plan(ctx, name)` runs the dry-run and stamps identity/flags onto the result, so
  a plan closure computes only Report/Actions/Diff and never mutates.
- `apply(ctx, name, confirm)` enforces the contract BEFORE any mutation: unknown →
  `errUnknownSkill`; report-only → `errReportOnly`; destructive && !confirm →
  `errConfirmRequired`. Each guard returns its sentinel with no side effect, so "no
  destructive action without approval" is a property of the dispatch path, not of
  each skill.

`skillPlan` is the read-only dry-run: `Report` (findings), `Actions`
(`{Op,Target,Detail}` mutations it would perform), and `Diffs` (each
`{Path,Before,After}`) previewing the per-file rewrites it would make. `Empty()` (no
actions and no changed diff) drives the "nothing to do" state and suppresses the
apply control.

### 2. The four skills

- **cleanup-stale** (destructive): removes the unregistered `edit-*`/`beehive-*`
  worktree directories under `.worktrees` that dead editor sessions and capped
  passes leak. Plan lists them from the existing `staleWorktrees` scan (reused from
  hygiene.go, unchanged). Apply takes `s.gitMu`, RECOMPUTES the stale set under the
  lock (never acting on a plan that raced a publish), guards each name is a bare
  basename, and `os.RemoveAll`s it.
- **gc** (destructive): reclaims abandoned editor worktrees — the `edit-*` trees
  both stale (no fresh session) and clean (no pending change). Plan lists
  `editor.Manager.Reclaimable(ctx)`, a new read-only mirror of `Reload`'s
  keep/reclaim decision. Apply takes `s.gitMu`, snapshots the reclaim set, then
  runs `Reload` — reusing the editor's own tested reclaim (which also re-registers
  the fresh/pending sessions it keeps) rather than a second divergent walk.
- **resources** (report-only): a read-only inventory of each submodule — its own
  active blue/green deploy env (`INFRASTRUCTURE.md` via `artifacts.LoadInfra`) and
  produced artifacts (`ARTIFACTS.md` via `artifacts.LoadArtifacts`). Blue/green is a
  per-submodule property, so every deploy-env line is scoped to a named submodule and
  the coordination root (not a deployable target) is never reported. No apply; the
  registry refuses it.
- **infra-conventions** (non-destructive, diff): iterates `s.repo.Submodules()` and
  normalizes each submodule's own `submodules/<name>/INFRASTRUCTURE.md` so it declares
  that submodule's blue/green markers, filling the conventional defaults only for
  markers that are ABSENT (never rewriting an existing one). Blue/green is a
  per-submodule property, so it acts on every submodule independently and never on the
  coordination root. `normalizeInfraConventions` routes one file through the typed
  model (`ParseInfra` → `Deployment` for defaults → `SetActive`/`SetEnvs` → `String`),
  so the rest of the document round-trips verbatim and the op is idempotent. Plan
  previews one unified diff per changed submodule; apply writes each file and publishes
  once via the shared `publishMain` path.

### 3. `internal/editor` — `Reclaimable` (gc's read-only half)

`Manager.Reclaimable(ctx)` walks the same edit-branch worktrees as `Reload` and
returns the sorted branch names it would reclaim (stale && clean), mutating
nothing. It shares `Reload`'s exact predicate (`fresh || pending` → keep), so the
gc dry-run and apply agree by construction.

### 4. `internal/artifacts` — `SetEnvs`

`Infra.SetEnvs([]string)` is the symmetric partner to the existing `SetActive`:
it rewrites every `Environments:` marker line in place (or appends one), copies its
argument, and marks the model present. `normalizeInfraConventions` needs it to fill
an absent environment list.

### 5. HTTP + templates

`GET /skills` renders the index (metadata cards, one dry-run control each);
`POST /skills/{name}/plan` runs the dry-run; `POST /skills/{name}/apply` applies.
Handlers map the sentinels to status: unknown → 404, report-only → 400,
confirm-required → 200 re-rendering the plan with a distinct "Confirm and apply"
control (a real two-step gate: the first apply mutates nothing and asks; only the
confirmed resubmit, carrying `confirm=on`, acts). Success renders the result plus a
fresh post-apply plan (now typically clean). `skills.html` is the page;
`skill_panel.html` is the htmx-swapped fragment (reused for the initial state), and
a `/skills` nav link is added in `layout.html`.

## Tests

- `internal/web/skills_test.go`
  - `TestSkillsPageListsSkills` — the index lists all four skills and a dry-run
    control.
  - `TestSkillUnknownIs404` — unknown skill errors on both plan and apply.
  - `TestSkillResourcesReportOnly` — a report-only dry-run returns the per-submodule
    inventory (a named `alpha` line) and never a hive-wide `root:` deploy line; apply
    is refused (400) with no mutation path.
  - `TestSkillCleanupStaleConfirmGateAndApply` — the destructive acceptance: the
    dry-run lists exactly the stale dirs without removing them; an UNCONFIRMED apply
    refuses (asks to confirm) and removes nothing; only a CONFIRMED apply removes
    exactly the stale dirs while sparing the non-matching directory.
  - `TestSkillInfraConventionsAppliesExactPlan` — with alpha missing the markers and
    bravo already declaring its own, the dry-run previews the markers only for alpha's
    `submodules/alpha/INFRASTRUCTURE.md`; apply (no confirm needed) writes EXACTLY that
    content, leaves bravo byte-for-byte untouched, and — the inversion of the old
    global behavior — never writes deploy markers to the hive root (which stays empty);
    a second dry-run is a no-op (idempotent).
- `internal/editor/reclaimable_test.go` `TestReclaimableListsStaleCleanOnly` —
  `Reclaimable` returns only the stale+clean edit worktree (sorted), never a fresh,
  pending, or honeybee worktree, and mutates nothing; after `Reload` reclaims it,
  `Reclaimable` is empty (consistent with apply).
- `internal/artifacts/setenvs_test.go` `TestInfraSetEnvs` — in-place rewrite,
  append-when-absent, synthesize-from-empty (marks present), and argument-copy
  (no aliasing).

## Acceptance mapping

- *registry lookup + dry-run returns a deterministic plan without mutating* →
  `reg.lookup`/`reg.plan`; every plan closure is a read-only scan;
  `TestSkillCleanupStaleConfirmGateAndApply`/`TestSkillInfraConventionsAppliesExactPlan`
  assert the dry-run changes nothing; `TestReclaimableListsStaleCleanOnly` asserts
  the underlying gc scan is read-only.
- *applying performs exactly the proposed change* → infra-conventions writes the
  exact previewed content; cleanup-stale removes exactly the planned dirs (and only
  those); both asserted.
- *unknown skill errors* → `errUnknownSkill` → 404; `TestSkillUnknownIs404`.
- *no destructive action without approval* → `errConfirmRequired` gate returns
  before any mutation; `TestSkillCleanupStaleConfirmGateAndApply` proves the
  unconfirmed apply removes nothing.

## Caveats / environment

- The browser gate is a real two-step server flow (apply → confirm-required panel →
  confirm), so the guarantee does not depend on client-side `hx-confirm`. The server
  sentinel is authoritative for programmatic callers too.
- cleanup-stale apply recomputes the stale set under `gitMu` rather than trusting
  the plan, so a concurrent publish between dry-run and apply cannot cause it to act
  on a stale target; the trade-off is that the applied set can legitimately differ
  from a much-earlier dry-run (it always reflects live state).
- Build/test under the mandated static mode: `CGO_ENABLED=0` with `TMPDIR`/`GOTMPDIR`
  on the root fs (host `/tmp` is a small tmpfs and the default cgo linker is broken
  on this host) — same environment caveat as prior tasks.
