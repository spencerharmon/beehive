# route every edit-with-AI request through a publish-capable flow

## Problem

Two AI-edit surfaces coexisted in `internal/web`:

- **OLD** `chatManager` (`chatedit.go`): a generic chat-diff editor over ANY repo path.
  Approve wrote the proposal and committed it on a throwaway `edit-<slug>-<unix>`
  branch cut off local main — and stopped. Its routes (`GET/POST /edit`, `GET
  /edit/{id}`) had no merge/publish step of any kind.
- **NEW** `internal/editor` (`editor.go` + `internal/editor/*`): a restricted-allowlist
  editor whose `Session.Merge` (wired by `merge-button-wire`/`publish-main-writes`)
  actually lands an approved change on main — `PublishToMain` + `UpdateLocalMain`,
  with the base-validation and human-owned whole-file-delete guards
  (`editor-safety-guards`).

`editEntry` (`GET /edit`) dispatched on the query string: any request carrying
`?path=` — which is **every** real edit-with-AI link (`dashboard.html`'s "edit
infrastructure with AI" / "edit roi (AI)", `explorer.html`'s per-file view/edit/
create links, `roi_editor.html`'s "edit with AI chat") — went to the OLD
`chatOpen`/chatManager. Only a path-LESS `/edit` (nothing in the UI sends one)
reached the NEW, publish-capable `editNew`.

The result: an operator approves an AI-proposed edit, sees "applied and
committed", and the change **never reaches main**. For a submodule ROI.md this
also means the PLAN's `Beehive-ROI` stamp never advances past it, so no
reconcile task is ever created and the editor's own startup GC eventually
reclaims the stale, clean-looking worktree — the edit is gone with no error at
any point. Live evidence at task-open time: operator ROI edits to `beehive`
(+407/-362) and `flux` (+15) plus three older `chat-edit:` commits, all
stranded on `edit-*` branches main never merged.

## Fix

Consolidate on ONE AI-edit surface for coordination files: `internal/editor`.

1. **`internal/editor/editor.go`** — `editableBasenames` used to allow only
   `ROI.md`/`INFRASTRUCTURE.md`/`SUBMODULE-LINKS.yaml`, far narrower than what the
   UI actually links (`repo.OptionalFiles`: `INFRASTRUCTURE.md`, `RULES.md`,
   `ARTIFACTS.md`, `AGENTS.md`, `ROI.md`; `repo.RootInstructionFiles`: `AGENTS.md`,
   `HONEYBEE.md`, `BOOTSTRAP.md`, `LOCALS.md`). The allowlist is now *built* from
   those two canonical declarations (+ `SUBMODULE-LINKS.yaml`) instead of
   hand-maintained, so it can never again drift behind what a real link targets.
   `PLAN.md`/secrets/code stay categorically excluded (not in either set).
2. **`internal/web/editor.go`** — `editNew` (and its `requestedFile` helper) now
   reads the file from `?path=` first (what every template link actually sends),
   falling back to the older `?file=` and to form values, so the one handler
   serves every caller.
3. **`internal/web/web.go`** — `editEntry` (`GET /edit`) now unconditionally opens
   through `editNew`. The route table drops `POST /edit` (`chatOpen`) and `GET
   /edit/{id}` (`chatPage`) entirely — the generic, never-published per-path HTTP
   entry is gone, not merely unreachable. `/edit/{id}/panel|message|approve|reject`
   stay registered because they ALSO back the bootstrap wizard's embedded agent
   (`bootstrap.go`'s `openBootstrap`, a singleton session fixed to `LOCALS.md`,
   opened only via `GET /bootstrap` — never through `editEntry`).
4. **`internal/web/chatedit.go`** — `chatOpen`/`chatPage` removed along with the
   now-dead `templates/chatedit.html` full-page view; `chatManager`'s core
   (`open`/`openWith`/`chatSession`) is untouched and keeps backing the bootstrap
   wizard, still exercised directly by the existing `chatedit_test.go` unit tests.

`internal/editor`'s existing safety guards (base validation against a foreign/
wrong remote, the human-owned whole-file-delete default-block requiring an
explicit `confirm=delete`, repo-own-remote-only publish target) are unchanged —
they now simply apply to every coordination file instead of three.

## Tests

`internal/editor/diff_test.go` — `TestValidateFile` extended: `AGENTS.md`,
`HONEYBEE.md`, `BOOTSTRAP.md`, `LOCALS.md`, `RULES.md`, `ARTIFACTS.md` move from
"must reject" to "must accept"; `PLAN.md` (root and submodule-qualified) stays
rejected.

`internal/web/web_test.go`:
- `TestEditEntryOpensPublishCapableEditor` — `GET /edit?path=submodules/alpha/ROI.md`
  (the exact link the dashboard/explorer/roi_editor render) redirects into
  `/editor/{id}`, and that id resolves in `internal/editor`'s own session table.
- `TestChatManagerEditRoutesRetired` — `POST /edit` is gone (405) and `GET
  /edit/{id}` is gone (404): the publish-less approve path is not merely
  unreachable from the UI, the routes do not exist.
- `TestEditEntryRejectsNonCoordinationFile` — an arbitrary repo file 400s instead
  of silently opening a session that could never publish anyway (proves the
  request reaches `internal/editor`, not chatManager's any-file surface).
- `TestEditWithAIMergePublishesToOrigin` — opens a session exactly as a real link
  would, writes a change into its worktree (standing in for an agent turn), posts
  `/editor/{id}/merge`, and asserts BOTH local main's working tree and a
  temp bare repo-own origin's main carry the change (not a dangling `edit-*`
  branch). This is a genuine negative control: it was run once against a
  deliberately reintroduced "merge that returns nil without publishing" and
  failed as expected before the fix was restored.
- `TestEditWithAIMergeDeleteGuardHolds` — the same HTTP path, but the "edit"
  wipes ROI.md; plain merge is blocked (panel shows the `human-owned` warning,
  main/origin untouched) and only `confirm=delete` publishes the deletion.

`CGO_ENABLED=0 go build/vet/test ./...` green across every package.

## Caveats / follow-ups

- The bootstrap wizard's LOCALS.md session (`chatManager`/`openBootstrap`) still
  has no merge/publish step of its own — out of scope here (no `?path=` link
  reaches it, and the ROI card's stranded-branch evidence was all path-carrying
  edits), but it is the same defect class and a plausible follow-up task.
- `internal/editor`'s system prompt stays generic (it does not carry the
  per-file rules `internal/web/filecontext.go` seeds for chatManager, e.g. ROI's
  "human-owned/FORBIDDEN" framing). The CODE-level guards (delete-block, base
  validation) are unaffected either way; this only affects how well an agent's
  own proposal already respects a file's conventions before the guard ever has
  to fire.
