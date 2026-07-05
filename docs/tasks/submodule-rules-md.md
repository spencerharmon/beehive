# submodule-rules-md: per-submodule beehive-owned RULES.md overlay

## Problem

A submodule can carry an `AGENTS.md` (a submodule-local rules overlay opencode
reads on its own), but there was no beehive-*owned* per-submodule rules file: a
single named path the frontend, the honeybee, and the chat-diff editor all agree
on. The shipped default `AGENTS.md` (`prompts/AGENTS.md`) already lists
`RULES.md` as an optional per-submodule file, but nothing in the code named it,
surfaced it, or fed it into the edit context — so it was documentation with no
wiring.

## Design

`submodules/<sm>/RULES.md` is a beehive-owned, per-submodule rules overlay that
sits **outside** `repo/` (a coordination file, not target source), **additive** to
any `AGENTS.md` for the same submodule. `AGENTS.md` is applied first, then
`RULES.md` — RULES states only the extra local rules, it does not restate AGENTS.
Its absence is a safe no-op everywhere (nothing requires it to exist).

The change is minimal and touches three seams plus tests:

### 1. `internal/repo` — the path constant

`RulesFile = "RULES.md"` joins `AgentsFile` / `PlanFile` / `ROIFile` /
`InfraFile` / `Artifacts` in the layout-name `const` block. This is the single
source of truth for the path; every reader keys off it instead of a stray
literal.

### 2. `internal/web/web.go` — the explorer

The explorer's raw-markdown docs map (previously `PLAN` + `ROI`) now also renders
`AGENTS` (`repo.AgentsFile`) and `RULES` (`repo.RulesFile`). Rendering reuses the
existing `os.ReadFile` → `renderMarkdown` path, which only populates a label on a
successful read, so an **absent file is silently skipped** — the no-op property is
inherited, not special-cased.

Order: the explorer template ranges over the `map[string]template.HTML` by sorted
key (Go's documented map-range-in-templates behavior), and `"AGENTS" < "RULES"`,
so the AGENTS section deterministically precedes the RULES section — the same
AGENTS-then-RULES order the agent edit context documents.

### 3. `internal/web/filecontext.go` — the chat-diff editor (agent/edit) context

The per-file context resolver (`chat-diff-file-context`) already carried a
`RULES.md` rule (`rulesFileContext`), keyed on a `"RULES.md"` literal placeholder
that was explicitly noted as "riding submodule-rules-md". That literal is now the
`repo.RulesFile` constant, so the resolver stays in lockstep with every other
reader. The `rulesFileContext` preamble tells the editing agent RULES.md is
ADDITIVE to AGENTS.md and that AGENTS is applied first, then RULES.

### Honeybee context

The honeybee is not given RULES.md content up-front (that would spend tokens on
every pass regardless of whether the file exists, cutting against the current
"cut tokens per honeybee" priority, and `internal/swarm` is deliberately out of
this task's file set). Instead RULES.md is a first-class named overlay the
honeybee reads **on demand** in its worktree, exactly as it reads a submodule's
`AGENTS.md` overlay: the shipped `prompts/AGENTS.md` already lists `RULES.md` as
an optional per-submodule file, and it now has a stable constant naming it.

### Not touched

No ROI.md write path is involved anywhere in this change — the explorer read is
read-only and the resolver returns a canned preamble. The "never auto-edits
ROI.md" acceptance holds by construction.

## Tests

- `internal/repo/repo_test.go` `TestRulesFileConstant` — pins `RulesFile ==
  "RULES.md"` and that it is distinct from `AgentsFile` (additive, not a rename).
- `internal/web/web_test.go` `TestExplorerShowsAgentsAndRules` — a submodule with
  both `AGENTS.md` and `RULES.md` renders both sections, and the AGENTS section
  index precedes the RULES section index (AGENTS-then-RULES). Negative control:
  reverting the docs map to PLAN/ROI-only makes this test fail.
- `internal/web/web_test.go` `TestExplorerRulesAbsentNoOp` — with no RULES.md on
  disk the explorer still returns 200 and omits the RULES section (absence no-op).
- `internal/web/filecontext_test.go` `TestRulesFileContextKeysOffConstant` — the
  resolver keys the RULES.md rule off `repo.RulesFile`, a submodule-qualified path
  resolves the same as the bare basename, the preamble states the AGENTS-then-RULES
  order, and it is distinct from the AGENTS.md rule. The pre-existing
  `TestResolveFileContextDistinct` still covers the RULES.md preamble tokens.

## Acceptance mapping

- *present RULES.md appears in explorer* → `TestExplorerShowsAgentsAndRules`.
- *present RULES.md appears in agent/edit context* → `TestRulesFileContextKeysOffConstant`
  + `TestResolveFileContextDistinct` + `TestChatSystemPromptSeedsFileRules`.
- *AGENTS.md+RULES.md both present, order AGENTS-then-RULES* → explorer order
  assertion + the resolver preamble ordering assertion.
- *absence no-op* → `TestExplorerRulesAbsentNoOp` (and the read-only skip in the
  explorer loop).
- *never auto-edits ROI.md* → no ROI write path is touched; changes are a
  read-only view and a canned-preamble resolver.
