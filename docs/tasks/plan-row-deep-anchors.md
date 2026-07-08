# plan-row-deep-anchors: stable per-row anchors for the plan table

## Problem

`ui-audit-002` (Finding #12, ranked #4 of that pass — deep links / shareable anchors) flagged that
`plan_items.html`'s `{{range .Plan.Items}}<tr>` rendered each task's id as `<code>{{.ID}}</code>` but the
`<tr>` itself carried no `id` attribute, so `/submodule/<name>/plan#<id>` scrolled nowhere even though the
id was printed right in the row. The plan page is one of the most-linked surfaces in the swarm — reviews,
arbitrations, and change docs constantly cite a task by id — so a stable per-row anchor is a direct
"shareable anchor" win. Disjoint from `commit-sha-deep-links` (commit shas + clipboard, in flight) and
`plan-view-detail-polish` (doc-column links + markdown expand) — neither adds a row anchor.

## Design

### `internal/web/templates/plan_items.html`

Each row gets a stable, URL-safe anchor id derived from its task id: `<tr id="task-{{.ID}}">`. The
`task-` prefix avoids colliding with any other element id on the page (task ids are already
`[a-z0-9-]`, so no escaping/charset concerns). The id cell becomes a self-link to that same fragment:
`<a href="#task-{{.ID}}"><code>{{.ID}}</code></a>` — clicking it gives the reader the shareable URL via
the browser's own address bar / "copy link" affordance, no clipboard JS needed (that's
`commit-sha-deep-links`'s job for commit shas, not this task's). No column, data, or ordering change.

### `internal/web/assets/style.css`

Two concerns, both token-driven (existing `--space-*` / `--surface-2` / `--accent`; no new color token),
scoped to `tr[id^="task-"]` so they can never affect any other table:

- `scroll-margin-top` on every anchor row, so the browser's native fragment-jump lands the row BELOW the
  sticky `.topbar` instead of underneath it (the topbar is `position: sticky; top: 0` and would otherwise
  hide the top of a freshly-scrolled-to row).
- A `:target` highlight — `tr[id^="task-"]:target td { background: var(--surface-2); }` (the same tint
  `tr:hover td` already uses, so a jumped-to row reads consistently with the existing hover language) plus
  `tr[id^="task-"]:target td:first-child { box-shadow: inset 3px 0 0 var(--accent); }` (a left accent bar
  on the id cell only, so the row reads as "located" without a shadow-per-cell ladder effect down the
  whole row).

## Tests — `internal/web/web_test.go`

`TestPlanRowDeepAnchors` (new):
1. Renders `plan_items.html` with two fixture tasks and asserts each row carries `id="task-<id>"` and its
   id cell links `<a href="#task-<id>"><code><id></code></a>`.
2. Asserts the table header and row order are unchanged (no column/data/order regression).
3. Asserts the embedded stylesheet carries the scoped `:target` rules and that the highlight rule body
   introduces no color literal (`#…` / `hsl(...)`), locking "token-driven, no new color token".

Every pre-existing `internal/web` test still passes unchanged (`go test ./internal/web`,
`CGO_ENABLED=0`): none asserted the old bare `<tr>`/`<td><code>{{.ID}}</code></td>` markup literally, so
none needed editing — `TestPlanViewPills`, `TestPlanItemExpandRendersFullBody`, `TestPlanChangeDocLink`,
and `TestTableScrollWrapsDataTables` (which renders `plan_items.html` too) all still pass.
`gofmt -l internal/web` is clean; `go vet ./internal/web/...` and `go build ./...` are clean.

## Acceptance mapping

- *every plan row renders a stable id derived from its task id and the id cell links to its own
  `#task-<id>`* → `<tr id="task-{{.ID}}">` + the self-linked id cell; `TestPlanRowDeepAnchors` (1).
- *navigating to `plan#task-<id>` scrolls to + highlights that row (token-driven `:target`, no new color
  token)* → `scroll-margin-top` compensates for the sticky topbar, `tr[id^="task-"]:target` highlights
  using only existing tokens; `TestPlanRowDeepAnchors` (3).
- *no change to columns/data/order* → markup-only id/self-link addition, no column/data/sort edits;
  `TestPlanRowDeepAnchors` (2).
- *a test asserts the per-row id + self-link href* → `TestPlanRowDeepAnchors`.
- *gofmt/go vet/go test ./internal/web green (CGO_ENABLED=0)* → verified.

## Caveats

- No clipboard affordance here by design — that's `commit-sha-deep-links`'s scope, not this task's.
- Task ids are unique within a plan, so `task-<id>` is collision-free; the `task-` prefix is kept anyway
  so an anchor row can never clash with some other unrelated element id on the page.
- The highlight is scoped to `tr[id^="task-"]` specifically (not a bare `tr:target`), so it can never
  bleed into `branch_view.html`/`human.html`/`stats.html`'s tables even if those ever grow row ids later.
