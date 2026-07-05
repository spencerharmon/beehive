# root-instruction-file-links: discoverable view/edit/create links for the repo-ROOT instruction files

## Problem

The frontend surfaced view/edit links for per-submodule files (`optional-file-links`)
and for the root `INFRASTRUCTURE.md`, but the four repo-ROOT *instruction* files had
no uniform affordance. Three of them ship as beehive-managed defaults — `AGENTS.md`
(the generic operating guide), `HONEYBEE.md` (the runtime protocol), `BOOTSTRAP.md`
(the install walkthrough) — and one is site-authored, `LOCALS.md`. An absent member
(most often an unwritten `LOCALS.md`) was **invisible**: no way to discover or create
it from the UI, and no exposed signal of which files are managed. Discoverability
would have been driven by the directory listing rather than by the set of files that
*should* exist.

## Design

Render links UNIFORMLY from a DECLARED root set, present or absent, exactly like
`optional-file-links` but for repo-ROOT instruction files, and carry a per-file
`managed` flag for the downstream `instruction-update-drift`.

### 1. `internal/repo` — the declared root set

`repo.RootInstructionFiles` is the single source of truth for membership and the
`managed` flag. Each entry is a `RootInstructionFile{File, Managed}`, keyed off the
layout name constants (a new `HoneybeeFile = "HONEYBEE.md"` joins the existing
`AgentsFile`/`BootstrapFile`/`LocalsFile`):

```
{AGENTS.md, managed}  {HONEYBEE.md, managed}  {BOOTSTRAP.md, managed}  {LOCALS.md, site-authored}
```

`Managed` is true for the three files `internal/instruct` ships/refreshes and false
for the site-authored `LOCALS.md`. The root `AGENTS.md` here is the GENERIC operating
guide — deliberately NOT the per-submodule `submodules/<sm>/AGENTS.md` overlay, which
rides `OptionalFiles`.

### 2. `internal/web/web.go` — the dashboard index

`(*Server).rootFileLinks()` builds one `rootFileLink{Label, File, Present, Managed}`
row per member of `repo.RootInstructionFiles`. Membership comes from the SET;
`Present` is a plain `os.Stat` at the repo root that only decides how a row renders,
never whether it renders; `Managed` is copied from the set (never from disk). It is
read fresh on every dashboard render, so a plain manual commit that lands a root file
on disk flips its row to present on the next load — no special write path. The
dashboard handler passes `RootFiles` alongside the existing data.

### 3. `internal/web/templates/dashboard.html` — uniform links

A `nav.file-index` (the same component the explorer uses) lists every row. A
**present** file renders a `view / edit` link; an **absent** file is dimmed and
renders `not created · create`. Both point at `/edit?path=<File>` — the repo-ROOT
path, no `submodules/<name>/` prefix. Each row also renders a `managed` or
`site-authored` `.badge`, exposing the flag in the UI.

### 4. Routing through the chat-diff editor (chat-diff-editor-core)

`/edit?path=<File>` is the generic chat-diff editor; it already handles both cases
with no new code:

- **present** → opens on the file's current contents (view + AI edit).
- **absent** → `chatSession.base` returns `""` at HEAD, so the editor opens on an
  **empty base** (create). The proposal is written+committed ONLY on human approval,
  so a create never auto-generates a file. A plain manual `git` commit of the file is
  an equally valid write path — the disk-driven `Present` picks it up on next render.

### 5. Per-file seeding (chat-diff-file-context)

`resolveFileContext(path)` gains a repo-ROOT branch: for a target at the repo root
(`path.Dir == "/"`) it first consults `rootFileContexts`, a table keyed off the same
`repo.*` constants, before the basename table. This is the ONE path-qualified
exception to the otherwise basename-driven resolver, and it is what keeps the generic
root `AGENTS.md` (operating-guide + beehive-managed rules) from being conflated with a
per-submodule `submodules/<sm>/AGENTS.md` overlay (which still resolves to the overlay
rule in the basename table). `HONEYBEE.md`/`BOOTSTRAP.md` seed their protocol/install
purpose + managed rules; `LOCALS.md` seeds the site-authored, NEVER-auto-generated
rules so a create "seeds a template + rules, it does not write the file itself."

### Ownership: LOCALS.md is never auto-generated

`LOCALS.md` is site-authored (`Managed=false`). Its create/edit routes through the
SAME approval-gated chat-diff editor as the managed files, seeded with the
site-authored rules. Because the editor only writes on explicit human approval — and
no other write path exists — `LOCALS.md` is never auto-generated. The `managed` flag
is what `instruction-update-drift` keys off to scope its staleness check to the three
managed files and skip the site-authored one.

## Tests

- `internal/repo/repo_test.go` `TestRootInstructionFilesSet` — pins the set to exactly
  the four root files keyed off the constants, no duplicates/empties, the correct
  per-file `Managed` flag (AGENTS/HONEYBEE/BOOTSTRAP managed, LOCALS not), and that no
  per-submodule file (ROI/PLAN/RULES) leaks in.
- `internal/web/web_test.go` `TestDashboardRootInstructionFileLinks` — white-box: one
  row per declared member, `Present` following disk (AGENTS/HONEYBEE/BOOTSTRAP present,
  LOCALS absent in the `repo.Init` fixture) and `Managed` following the SET; rendered:
  a `/edit?path=<f>` link for EVERY member incl. the absent LOCALS, `view / edit` for
  present and `create`/`not created` for absent, and the managed/site-authored badges;
  and creating `LOCALS.md` (a manual-commit stand-in) flips its row to present.
- `internal/web/web_test.go` `TestRootAbsentFileEmptyBaseCreateSeeded` — the absent
  `LOCALS.md` opens the chat-diff editor on an EMPTY base, the opencode session is
  seeded with LOCALS' site-authored / never-auto-generated rules, nothing is on disk
  before approval, and approve creates+commits the new file.
- `internal/web/filecontext_test.go` `TestRootInstructionFileContextNotConflated` —
  each root file resolves to a distinct preamble; the managed ones note `beehive-MANAGED`
  and LOCALS does not; and the generic root `AGENTS.md` resolves to a DIFFERENT context
  than a per-submodule `submodules/<sm>/AGENTS.md` overlay (the anti-conflation lock).
- Pre-existing `TestResolveFileContextDistinct`, `TestRulesFileContextKeysOffConstant`,
  and the dashboard/bootstrap tests still pass unchanged — the submodule AGENTS.md
  overlay rule and the basename resolver are preserved; the root index is purely
  additive.

## Acceptance mapping

- *links render for the full declared root set INCLUDING absent members (set-driven,
  not disk-driven)* → `TestDashboardRootInstructionFileLinks` (+ `TestRootInstructionFilesSet`).
- *present → view+edit; absent → empty-base create seeded with purpose/ownership* →
  present rows link to the chat-diff editor on current contents; `TestRootAbsentFileEmptyBaseCreateSeeded`.
- *the managed flag is exposed* → `rootFileLink.Managed` + the dashboard badge;
  `TestDashboardRootInstructionFileLinks` + `TestRootInstructionFilesSet`.
- *edits write byte-exact on approval and a manual root-file commit shows on next
  render* → the approval commit path (chat-diff-editor-core) + disk-driven `Present`;
  `TestRootAbsentFileEmptyBaseCreateSeeded` + the flip-to-present check.
- *LOCALS.md never auto-generated* → approval-gated editor + no other write path;
  `TestRootAbsentFileEmptyBaseCreateSeeded` (nothing on disk before approval) and the
  `Managed=false` seeding in `TestRootInstructionFileContextNotConflated`.
- *do not conflate root AGENTS.md with a per-submodule overlay* →
  `TestRootInstructionFileContextNotConflated`.
- *gates instruction-update-drift* → the exposed `managed` flag on `repo.RootInstructionFiles`.
