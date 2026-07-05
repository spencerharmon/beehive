# instruction-update-drift: surface managed-file drift + a run-instruction-update action

## Problem

The three MANAGED repo-ROOT instruction files — `AGENTS.md` (generic operating
guide), `HONEYBEE.md` (runtime protocol), `BOOTSTRAP.md` (install walkthrough) — are
shipped as binary-embedded defaults and refreshed by `beehive instruction update`.
`root-instruction-file-links` made each file a discoverable dashboard row carrying a
`Managed` flag, but the UI gave no signal of whether an on-disk managed file had
**drifted** from the binary's current default, and the only way to refresh a stale
file was the CLI. The site-authored `LOCALS.md` (`Managed=false`) must stay
categorically OUT of this: never drift-checked, never offered an update, never
written by this path.

## Design

Reuse the EXISTING shared installer `internal/instruct` — the single source both the
CLI (`cmd/beehive/cmd_instruct.go`) and `repo.Init` already call — for both the
read-only drift scan and the write path, so there is no second hardcoded copy of the
defaults and the web action runs byte-for-byte the same logic as `beehive
instruction update`.

> Deviation from the task card's `Files:` hint. The card names
> `internal/config (embedded-default installer, shared fn)`. In this codebase that
> shared installer already exists as its OWN package, `internal/instruct` (embeds
> `prompts.Agents/Honeybee/BootstrapGuide`, exposes `Files/Scan/Update/Install`), and
> is consumed by BOTH the CLI and `repo.Init`. Adding a copy under `internal/config`
> would VIOLATE the "reuses the CLI default source (no second hardcoded copy)"
> acceptance criterion, so the shared fn is extended in `internal/instruct` instead.
> `internal/config` is untouched.

### 1. `internal/instruct` — a per-file drift query

`Scan(root)` already reports every managed file's `Status` (`Missing | Clean |
Modified`). The frontend iterates its OWN declared set (`repo.RootInstructionFiles`)
and needs the status ONE file at a time, so add:

```
func StatusOf(root, name string) (st Status, ok bool, err error)
```

It reuses the same `stat` helper (byte-compare against the embedded default) and
returns `ok=false` when `name` is not a managed file — which is exactly what keeps
`LOCALS.md` (never in `Files()`) out of the check: an unmanaged name yields
`(Missing, false, nil)` and the caller shows no badge. No second default copy; the
embedded source stays solely in `instruct`.

### 2. `internal/web/web.go` — drift on the dashboard rows

- `rootFileLink` gains `Drift string` (`"" | "clean" | "drift" | "missing"`).
- `driftLabel(instruct.Status) string` maps the installer vocabulary to the badge
  vocabulary — `Clean→"clean"`, `Modified→"drift"`, `Missing→"missing"`. The CLI
  keeps its own `"modified"` word; `"drift"` is a PRESENTATION mapping only.
- `rootFileLinks()` attaches `Drift` for MANAGED members only, via
  `instruct.StatusOf`; a scan read error leaves `Drift` empty (best-effort overview,
  never a dashboard-wide failure — mirrors `Present`'s tolerance). `LOCALS.md`
  (`Managed=false`) is skipped, so its `Drift` stays `""` even when it exists.
- `rootFilesDrift([]rootFileLink) bool` reports whether ANY managed file is `"drift"`
  or `"missing"` — i.e. whether the update action would change anything.

### 3. `internal/web/templates/dashboard.html` + `assets/style.css`

Each row renders a `.badge.drift.drift-<state>` when `Drift` is non-empty (so
`LOCALS.md` never gets one). A `<form method="post" action="/instruction/update">`
carries the "run instruction update" button; its emphasis and helper copy key off
`RootFilesDrift` ("managed files have drifted…" vs "…match the shipped default").
`style.css` gets `.badge.drift-clean/-drift/-missing` hues.

### 4. `POST /instruction/update` — the write action

`(*Server).instructionUpdate` invokes the SAME `instruct.Update(ctx, root, true,
nil)` the CLI uses (clobber path): a clean file is a no-op, a missing one is created,
a MODIFIED one is backed up to `<name>.<epoch>.bak` and both the backup and the
refreshed copy are committed. `LOCALS.md` is not in `instruct.Files()`, so it is
never written, backed up, or reported. Redirects `303` back to `/`.

**Atomic under one lock.** The installer both WRITES and COMMITS. To keep its commit
from racing the follow-the-remote pull, `publishMain` is split into a thin lock
wrapper and a `publishMainLocked` body; the handler takes `gitMu` ONCE, runs
`instruct.Update` then `publishMainLocked`, and releases — so the update commit and
the publish/push happen without dropping the lock between them. Idempotent: a second
run over an already-clean set writes nothing and produces no new backup.

## Tests

- `internal/instruct/instruct_test.go` `TestStatusOfReportsPerFileDrift` — `StatusOf`
  returns `Clean` for a byte-identical file, `Modified` after an edit, `Missing` when
  absent, and `ok=false` for an unmanaged name (`LOCALS.md`).
- `internal/web/web_test.go`:
  - `TestManagedRootFilesAreInstructManaged` — guards that every `Managed` member of
    `repo.RootInstructionFiles` is in `instruct.Files()` (a managed badge can never
    promise an update the installer cannot perform) and that `LOCALS.md` is in
    NEITHER set.
  - `TestRootFileLinksDriftStatus` — white-box: a fresh fixture reads all three
    managed files `clean` and `LOCALS.md` empty; an edited managed file →`drift`, a
    removed one →`missing`, and `rootFilesDrift` flips accordingly; `LOCALS.md` stays
    empty even when present with custom content.
  - `TestDashboardDriftBadgeAndUpdateAction` — rendered: the clean dashboard shows
    `drift-clean` badges + the `/instruction/update` action + the up-to-date copy and
    NO drift/missing badge; a drifted managed file surfaces a `drift-drift` badge and
    the "have drifted" emphasis.
  - `TestInstructionUpdateRestoresManagedFiles` — the POST restores a drifted
    `AGENTS.md` to `prompts.Agents` leaving EXACTLY one `AGENTS.md.<epoch>.bak` with
    the prior contents, recreates a deleted `BOOTSTRAP.md`, never touches or backs up
    a custom `LOCALS.md`, and a second run adds no new backup (idempotent).

## Acceptance mapping

- *byte-identical→clean, modified→drift, absent→missing* → `StatusOf`/`driftLabel`;
  `TestStatusOfReportsPerFileDrift`, `TestRootFileLinksDriftStatus`.
- *update writes a clean/missing file in place; a modified file via clobber produces
  exactly one `<name>.<epoch>.bak` + installs the default; idempotent* →
  `instruct.Update(…, true, nil)`; `TestInstructionUpdateRestoresManagedFiles`.
- *LOCALS.md never written/backed-up/reported* → excluded from `instruct.Files()`,
  `Managed=false` skips the scan; `TestRootFileLinksDriftStatus`,
  `TestInstructionUpdateRestoresManagedFiles`, `TestManagedRootFilesAreInstructManaged`.
- *reuses the CLI default source (no second hardcoded copy)* → both CLI and web call
  `instruct.Update`; `internal/config` untouched; `TestManagedRootFilesAreInstructManaged`.
- *no swallowed errors* → `StatusOf`/`Update` return errors; the handler `500`s on
  failure; a per-file scan error only degrades that ONE badge, never hides a write
  failure.
