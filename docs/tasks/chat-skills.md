# chat-skills: named, invocable maintenance skills on the chat surface

## Problem

`beehived` had read-only diagnostics (the hive-hygiene scan) and a per-file
chat-diff editor, but no surface for *repo-global maintenance actions* an operator
could invoke by name: reclaim leaked worktrees, revert config/checkout drift,
report the deploy rigs, check the INFRASTRUCTURE.md conventions. Hygiene could
only *point* at a cleanup skill ("run `beehive-hygiene`"); there was nothing in
the frontend that actually ran a bounded, previewed, approval-gated fix. The
maintenance procedures lived only in the CLI/skills docs, disconnected from the
operator's primary UI.

## Design

`internal/web/skills.go` adds a **skill registry** and a **manager** that mirror
the chat-diff editor's propose→approve loop, but for repo-global maintenance
instead of a single-file edit:

- **Dry-run = "propose".** Each skill's `Plan(ctx)` runs a deterministic,
  READ-ONLY scan and returns a `SkillPlan`: a human `Report` (always safe to
  show) plus the concrete typed `Actions` applying it WOULD perform. Building a
  plan mutates nothing.
- **Apply = "approve".** The manager holds the actionable plan in memory
  (`pending[name]`) and applies exactly it, so what the operator approves is
  exactly what runs — there is no re-scan between preview and apply. A plan
  consumed by a successful apply is dropped.
- **Confirm gate.** A `Destructive` plan's apply is refused
  (`errSkillNeedsConfirm`) unless the caller passes `confirm=true`. This is the
  "no destructive action without approval" invariant, enforced in ONE place
  (`applySkillActions`) and again defensively in the manager.

### The four shipped skills

Two report-only (never actionable), two destructive:

| Skill | Kind | What it does |
|-------|------|--------------|
| `resources` | report | Reports each INFRASTRUCTURE.md deploy rig (active env + environments) for the hive root and every submodule, via the typed `artifacts` model. |
| `infra-conventions` | report | Checks each present INFRASTRUCTURE.md against the blue/green conventions (an `Active:` marker is set; `Active` is one of `Environments`) and reports breaches. |
| `gc` | destructive | Reclaims leaked `.worktrees/<edit-*\|beehive-*>` dirs git no longer tracks (the abandoned-worktree leak). |
| `cleanup-stale` | destructive | Reverts git drift: removes unexpected (non-origin) remotes that leaked into the shared config, and resets drifted submodule checkouts back to their recorded gitlink. |

The skills are deliberately **non-overlapping** and reuse the existing read-only
hive-hygiene scans (`staleWorktrees`, `unexpectedRemotes`, `trackedGitlinks`,
`declaredGitlinkPaths`) as their dry-run — the scan the hygiene page already
surfaces is now the plan a skill can act on, so the diagnostic and the remedy
share one implementation.

### Bounded, auditable effects

Every mutation a skill can perform is one typed `SkillAction.Kind`
(`actRemoveWorktreeDir` / `actRevertRemote` / `actResyncCheckout`), dispatched in
`applyOne`. Each branch is guarded so a malformed target can never escape scope: a
worktree removal must be a single path segment under `.worktrees`; `origin` is
never removable as a "stray" remote; a resync path is cleaned and rejected if it
escapes the root. A resync carries the FULL recorded gitlink SHA so it resets to
an exact commit. A failing action is recorded in the result and does not abort the
rest (best-effort cleanup); its error is surfaced, never swallowed.

### Web surface (`web.go` + templates)

- `Server` gains a `skills *skillManager`, built in `New` over the repo root.
- Routes: `GET /skills` (index), `POST /skills/{name}/plan` (dry-run preview),
  `POST /skills/{name}/apply` (gated apply). A `skills` nav link joins the header.
- `skills.html` lists each skill (title, name, summary, a destructive/read-only
  badge) with a **Dry-run** button that HTMX-swaps the preview into a per-skill
  panel. `skill_panel.html` renders either the plan (report + actions + an Apply
  control) or the apply result. A destructive Apply carries the explicit
  `hx-vals='{"confirm":"yes"}'` authorization plus a browser `hx-confirm` prompt —
  the same double-gate the editor's delete guard uses.
- `skillError` maps errors to statuses the htmx toast surfaces: unknown skill →
  404, no pending dry-run → 409, missing confirmation → 400, else 500.

### Why in-memory pending (no persistence)

A plan is a cheap, read-only preview; a `beehived` restart simply drops any
un-applied preview, which is safe because a preview mutated nothing. Persisting it
would risk applying a stale plan against changed repo state — the opposite of the
"apply exactly what was previewed" guarantee.

## Tests (`internal/web/skills_test.go`)

- `TestSkillRegistryAndUnknown` — the four skills are registered; an unknown name
  errors on both plan and apply (**unknown skill errors**).
- `TestResourcesSkillReportOnly` / `TestInfraConventionsSkill` — the report-only
  skills produce a read-only report, are neither actionable nor destructive, and
  have nothing to apply; the conventions checker flags a real membership breach
  and reports conformance once fixed.
- `TestGCSkillReclaimsStaleWorktree` — the destructive path end to end: a
  **deterministic** dry-run (two runs `DeepEqual`) that **mutates nothing** (dir
  survives), a **confirm gate** (refused without `confirm`, dir survives), and a
  confirmed apply that performs **exactly** the proposed removal and consumes the
  plan.
- `TestCleanupStaleRevertsRemote` — same shape for the stray-remote path; asserts
  `origin` is never a target.
- `TestCleanupStaleResyncsDriftedCheckout` — a declared submodule whose checkout
  drifted off its recorded gitlink is previewed with the full recorded SHA,
  survives the dry-run, and is reset to the recorded commit by the confirmed apply.
- `TestSkillsHTTPSurface` — the web surface: the index lists every skill + the nav
  link; a dry-run POST returns the preview with a confirm-gated Apply and mutates
  nothing; an unknown skill is 404; a destructive apply without confirmation is
  400 and mutates nothing; a confirmed apply is 200 and performs the change.

## Acceptance mapping

- *registry lookup + dry-run returns a deterministic plan without mutating* →
  `TestSkillRegistryAndUnknown` + the deterministic/non-mutating dry-run assertions
  in the gc/cleanup-stale tests (`DeepEqual` two runs; state unchanged after plan).
- *applying performs exactly the proposed change* → gc removes exactly the
  previewed dir; cleanup-stale removes exactly the stray remote / resets exactly to
  the recorded gitlink (held-pending plan applied verbatim).
- *unknown skill errors* → `TestSkillRegistryAndUnknown` + the 404 in
  `TestSkillsHTTPSurface`.
- *no destructive action without approval* → the `errSkillNeedsConfirm` gate in
  `applySkillActions`/`skillManager.apply`, asserted at the manager level (gc,
  cleanup-stale) and over HTTP (400) in `TestSkillsHTTPSurface`; report-only skills
  carry no actions at all.
