# frontend: view/edit (create-if-absent) links for the root instruction files

## Problem

The four repo-ROOT instruction files carry the operating context of a beehive
install — `AGENTS.md` (generic operating guide), `HONEYBEE.md` (the runtime
protocol), `BOOTSTRAP.md` (the setup walkthrough), all three beehive-MANAGED, and
`LOCALS.md`, the SITE-authored operator record. The frontend surfaced none of them.
There was no way to read a root instruction file in the browser and no way to create
one that was missing.

Absence is the sharp edge. `repo.Init` scaffolds the three managed defaults but never
`LOCALS.md` (it is authored per install), so on a fresh install the single most
install-critical file is simply invisible — and a managed default that was deleted
would vanish just as silently. A dashboard driven by an `os.ReadDir` of the root would
only ever show what already exists, so the files that most need a create affordance are
exactly the ones it would hide.

## Fix

Drive the surface from a DECLARED set, never a directory listing, and render every
member uniformly whether or not it exists on disk.

- **`internal/repo/instructions.go` (new)** — `RootInstructionFile{Name,Title,Purpose,
  Managed}` and `RootInstructionFiles()`, a FIXED 4-member set in display order:
  `AGENTS.md`/`HONEYBEE.md`/`BOOTSTRAP.md` with `Managed=true` and `LOCALS.md` with
  `Managed=false`. `Managed` marks a beehive-shipped default that `beehive instruction
  update` owns; `LOCALS.md` is the sole site-authored member and must never be
  auto-generated. `AGENTS.md` here is the ROOT guide, deliberately NOT conflated with a
  per-submodule `submodules/<name>/AGENTS.md` overlay. Adds the `repo.HoneybeeFile =
  "HONEYBEE.md"` layout constant alongside the existing `AgentsFile`/`BootstrapFile`/
  `LocalsFile`.
- **Dashboard (`internal/web/web.go` `rootDocViews`, `templates/dashboard.html`)** — an
  "Instruction files" section projects the declared set into view models, statting each
  under the repo root for LIVE presence (`fileExists`, re-read every render so a manual
  root-file commit shows on the next load). Each row shows a managed/site ownership
  badge and a present/absent state badge. A PRESENT file links to a read-only view AND
  the AI editor; an ABSENT file links only to create. It never writes — `LOCALS.md` in
  particular is only ever stat'd.
- **View handler (`internal/web/web.go` `instruction`, route `GET /instruction/{file}`,
  `templates/instruction_view.html` new)** — renders ONE declared root file as
  read-only sanitized markdown (`renderMarkdown`). The `{file}` value is guarded against
  the declared set, never an arbitrary path, so it cannot traverse the tree or leak an
  undeclared file (an on-disk-but-undeclared `INFRASTRUCTURE.md` still 404s). An
  absent-but-declared file 404s; the dashboard offers its create link instead.
- **No special write path.** Both view+edit (present) and create (absent) ride the
  SAME generic chat-diff editor via `/edit?path=<name>`: the editor's base is the real
  committed content for a present file and empty for an absent one (a create), and the
  change lands only on human approval like any other chat-edit. A file created by a
  plain manual commit is picked up by the same live presence stat.
- **Seeded create context (`internal/web/filecontext.go`)** — dedicated per-file
  preambles for `HONEYBEE.md` (RUNTIME PROTOCOL, managed), `BOOTSTRAP.md` (SETUP
  WALKTHROUGH, managed) and `LOCALS.md` (SITE-SPECIFIC, site-authored, NEVER
  auto-generated) are registered in `fileContextRules`, so opening any of them in the
  chat-diff editor — including to CREATE an absent one — seeds the correct
  ownership/shape guidance. `AGENTS.md` keeps its existing generic (basename-shared)
  rule so the root guide and a submodule overlay resolve the same rule without being
  conflated in the set.

## Why this shape

- **Declared set, not `ReadDir`.** The whole point is to surface files that are ABSENT
  (a create flow); a listing can only show what exists, so the set is fixed in code and
  `Managed` is an explicit property of each member (tested in `internal/repo`), not
  inferred from disk.
- **One editor surface.** `chatSession.base` already returns "" when the target is
  absent at HEAD and the real content when present, so create and view+edit need no new
  code path — the absent case is just an empty base, exactly like creating any new file
  through the chat-diff editor.
- **Guarded view.** The read-only handler matches `{file}` against the declared set
  before reading, so it is a fixed, safe read surface (no path traversal, no reading
  undeclared root files) rather than a generic file server.

## Files

- `internal/repo/instructions.go` (new), `internal/repo/repo.go` (`HoneybeeFile` const)
- `internal/web/web.go` (`rootDocView`/`rootDocViews`/`instruction` + route + dashboard
  data), `internal/web/filecontext.go` (three preambles + rules)
- `internal/web/templates/dashboard.html`, `internal/web/templates/instruction_view.html`
  (new)
- Tests: `internal/repo/repo_test.go` (`TestRootInstructionFiles`),
  `internal/web/filecontext_test.go` (`TestRootInstructionFileContexts`),
  `internal/web/web_test.go` (`TestDashboardRootInstructionFiles`,
  `TestInstructionViewHandler`, `TestRootInstructionChatCreateAndView`)

## Tests

`TestRootInstructionFiles` pins the exact members, order and ownership (LOCALS.md is
the sole `Managed=false`). `TestRootInstructionFileContexts` proves the three new
preambles carry their file-appropriate tokens and are pairwise distinct from each
other, from `AGENTS.md`, and from the default. `TestDashboardRootInstructionFiles`
asserts all four render — including LOCALS.md ABSENT after `repo.Init` — with the
managed/site + present/absent badges, view+edit links for present files, a create link
for LOCALS.md, and that rendering never auto-generates LOCALS.md.
`TestInstructionViewHandler` covers present→200, absent→404, and undeclared→404 (an
existing-on-disk INFRASTRUCTURE.md still 404s). `TestRootInstructionChatCreateAndView`
drives the chat-diff surface end to end: absent LOCALS.md opens on an empty base seeded
with the site-authored guidance and approve creates it byte-exact, while present
HONEYBEE.md opens on its real committed base seeded with the managed runtime-protocol
guidance.

Build/test run with `CGO_ENABLED=0` (the host cgo linker is broken —
`-latomic_asneeded` / cgo net resolver — unrelated to this change): `gofmt -l` clean,
`go vet ./internal/repo/ ./internal/web/` clean, `go build ./...` OK, `go test
./internal/repo/ ./internal/web/` green.
