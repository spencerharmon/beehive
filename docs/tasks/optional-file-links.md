# optional-file-links: discoverable view/edit/create links for every optional per-submodule file

## Problem

The submodule explorer rendered a section only for optional per-submodule files
that already existed on disk (`internal/web/web.go`: the `docs` map is populated by
`os.ReadFile`/typed-artifacts loads that only add a label on success). So a file
that did not exist yet — `INFRASTRUCTURE.md`, `RULES.md`, `ARTIFACTS.md`,
`AGENTS.md`, `ROI.md` — was **invisible**: there was no affordance to discover it
or create it from the UI. Discoverability was driven by the directory listing
rather than by the set of files that *could* exist.

## Design

Render view/edit links UNIFORMLY from a DECLARED optional-file set, present or
absent, so a missing member is discoverable and offers a create path.

### 1. `internal/repo` — the declared set

`repo.OptionalFiles` enumerates the known optional per-submodule files, keyed off
the existing layout name constants (no stray literals):

```
InfraFile, RulesFile, Artifacts, AgentsFile, ROIFile
```

It mirrors the constants (RULES.md rides `submodule-rules-md`). `PLAN.md` is
deliberately excluded — it is honeybee-owned, produced by bootstrap rather than
authored ad hoc, and has its own dedicated `/submodule/<name>/plan` view, so a
"create PLAN.md" affordance would be wrong. This is the single source of truth for
membership; the frontend drives its index off it, never off the disk listing.

### 2. `internal/web/web.go` — the explorer index

`optionalFileLinks(sm)` builds one `fileLink{Label, File, Present}` row per member
of `repo.OptionalFiles`. Membership comes from the SET; `Present` is a plain
`os.Stat` existence check that only decides how a row renders, never whether it
renders. The explorer passes `Files` alongside the existing `Docs` map (the
present-file rendered content is untouched, so the existing view panes and their
tests are preserved — the index is purely additive).

### 3. `internal/web/templates/explorer.html` — uniform links

A `nav.file-index` lists every row. A **present** file renders a `view / edit`
link; an **absent** file is dimmed and renders `not created · create`. Both point
at the same editor URL `/edit?path=submodules/<name>/<File>`, composed with the
static submodule path in the template (only the slash-free basename is
interpolated in URL-query context, so the href stays literal — the same pattern
the dashboard's ROI/infrastructure links already use).

### 4. Routing through the chat-diff editor (chat-diff-editor-core)

`/edit?path=...` is the generic chat-diff editor. It already handles both cases
with no new code:

- **present** → opens on the file's current contents: the panel shows the file as
  the diff base (view) and the chat proposes edits (view+edit).
- **absent** → `chatSession.base` returns `""` at HEAD, so the editor opens on an
  **empty base** (create). The proposal is written+committed ONLY on human
  approval, so a create never auto-generates a file.

### 5. Per-file seeding (chat-diff-file-context)

`chatSystemPrompt(path)` seeds `resolveFileContext(path)` into the opencode
session, so the create/edit surface for each file carries that file's
purpose/ownership rules — `INFRASTRUCTURE.md`/`ARTIFACTS.md` get the typed-model
rules, `RULES.md` the additive-overlay rules, `AGENTS.md` the operating-guide
rules, and `ROI.md` the human-owned/honeybee-FORBIDDEN rules — reused verbatim,
no new context tables.

### Ownership: ROI.md is never auto-generated

`ROI.md` is human-owned. Its create/edit routes through the SAME approval-gated
chat-diff editor as the others, seeded with the human-owned ownership rules
(`roiFileContext`: "human-owned record of INTENT", honeybee edits "FORBIDDEN").
Because the editor only writes on explicit human approval, the create path "seeds
a template + rules for authoring, it does not write the file itself" — ROI is
never auto-generated. No auto-write path for ROI is added anywhere.

## Tests

- `internal/repo/repo_test.go` `TestOptionalFilesSet` — pins the set to exactly
  the five constants, no duplicates/empties, and asserts `PLAN.md` is excluded.
- `internal/web/web_test.go` `TestExplorerOptionalFileLinks` — white-box: one row
  per declared member with `Present` following disk (ROI present, the other four
  absent), membership set-driven; rendered: a `/edit?path=submodules/alpha/<f>`
  link for EVERY member incl. the four absent ones, `view / edit` for present ROI
  and `create`/`not created` for the absent members; and creating `RULES.md` flips
  its row to present.
- `internal/web/web_test.go` `TestExplorerAbsentFileEmptyBaseCreate` — an absent
  optional file opens the chat-diff editor on an EMPTY base, the opencode session
  is seeded with that file's rules (INFRASTRUCTURE → `internal/artifacts`), nothing
  is on disk before approval, and approve creates+commits the new file.
- `internal/web/web_test.go` `TestExplorerROICreateThroughEditorNoAutogen` — the
  explorer routes ROI create/edit through the editor, the seeded context marks it
  human-owned/FORBIDDEN, and a proposed ROI change is NOT written until approval.
- Pre-existing `TestExplorerRendersMarkdown`, `TestExplorerShowsAgentsAndRules`,
  and `TestExplorerRulesAbsentNoOp` still pass unchanged: the present-file content
  sections are untouched, and an absent member's index link is not the omitted
  `<h2>...</h2>` content section, so the "absence no-op" for content still holds.

## Acceptance mapping

- *links render for the full known set incl. absent, driven by the set not the
  disk listing* → `TestExplorerOptionalFileLinks` (+ `TestOptionalFilesSet`).
- *an absent file's link opens the chat-diff editor on an empty base seeded with
  the file's purpose/ownership rules* → `TestExplorerAbsentFileEmptyBaseCreate`.
- *a present file's link opens view+edit* → present rows link to the chat-diff
  editor (current contents as diff base + chat edit); `TestExplorerOptionalFileLinks`.
- *the ROI.md create/edit path routes through the editor and never auto-generates*
  → `TestExplorerROICreateThroughEditorNoAutogen`.
- *tests assert links for both present and absent members and the empty-base create
  path* → the two web tests above.
