# responsive-table-overflow: horizontal-scroll wrapper for data tables

## Problem

`ui-audit-002` (Finding #8, ranked #1 of that pass — responsive layout for narrow
widths) flagged that `style.css` carried exactly ONE `@media` width breakpoint
(`max-width: 48rem`, stacking only `.editor-grid`) and no `overflow-x` anywhere.
Every data `<table>` rendered `width:100%` with no scroll wrapper or narrow-width
strategy:

- `plan_items.html` — the 6-column task table (id/status/desc/deps/claim/change
  doc), the widest and most-visited table in the app.
- `branch_view.html` — the per-date commit table.
- `human.html` — the pending-human table.
- `stats.html` — the two top-level tables plus three `<table class="nested">`
  drill-downs (by-model, deliveries, total-by-model).

At a phone-width viewport this forced whole-page horizontal scroll or squeezed
columns illegibly — the broadest narrow-width impact in the frontend.

## Design

A single reusable wrapper utility, applied around every table above; no
per-column redesign, no card-per-row collapse (out of scope — a later pass may
revisit the plan table specifically if scroll proves insufficient).

### `internal/web/assets/style.css`

`.table-scroll` sets `overflow-x: auto` (+ `-webkit-overflow-scrolling: touch`)
so a too-wide table scrolls within its own box instead of blowing out the page
width. The wrapper — not the table — carries the table's usual vertical margin
(the same `--space-4` token the bare `table` rule already uses), and
`.table-scroll > table` zeroes the wrapped table's own margin. This matters
because `overflow-x: auto` establishes a new block-formatting context: without
moving the margin, the table's margin would stop collapsing with its wrapper's
(zero) margin and effectively double the vertical gap around every wrapped
table. Token-driven only — no new color token, and `.nested` keeps its
existing inherited table styling (the rule adds no color/border override).

### Templates

Each table is wrapped `<div class="table-scroll"><table>…</table></div>`,
markup-only — no column/data/sort changes:

- `plan_items.html` — the single task table.
- `branch_view.html` — the per-date-section commit table (each `<section>`'s
  table gets its own wrapper).
- `human.html` — the pending-human table.
- `stats.html` — both top-level tables (the default per-submodule view and the
  filtered/grouped view are mutually exclusive, so both get a wrapper) AND all
  three nested `.nested` drill-downs (by-model, deliveries, total-by-model),
  each already scoped inside its own `<details>`.

## Tests — `internal/web/web_test.go`

`TestTableScrollWrapsDataTables`:
1. Asserts the embedded stylesheet carries the `.table-scroll` rule
   (`overflow-x: auto`, the `.table-scroll > table { margin: 0; }`
   bookkeeping) and that the rule body introduces no color literal (`#…` /
   `hsl(...)`).
2. Renders `plan_items.html` and `branch_view.html` directly and asserts each
   table is wrapped.
3. Drives `human.html` through the real `/human` handler and asserts the
   wrapper.
4. Renders `stats.html` twice — once with `Filtered: false` (Subs carrying
   `Models` + `Deliveries` so all three nested tables render, plus `Total`
   carrying `Models` for the total-by-model table) and once with
   `Filtered: true` (a `Grouped` row) — asserting exactly 4 wrappers in the
   default view (top-level + by-model + deliveries + total-by-model) and one
   more in the grouped view.

## Acceptance mapping

- *a too-wide table scrolls within its own box at narrow widths (plan table +
  one other)* → `.table-scroll` wraps `plan_items.html` and every other
  enumerated table; `TestTableScrollWrapsDataTables`.
- *utility is token-driven, no new color token* → `.table-scroll` uses only
  `--space-4`; the test asserts no color literal in the rule body.
- *desktop rendering unchanged* → the wrapper carries the table's former
  margin and zeroes the table's own, so wrapping introduces no extra vertical
  space; no column/width/data change.
- *`.nested` stats tables keep their styling* → `.table-scroll` adds no
  color/border rule, only `overflow-x` + margin bookkeeping; `.nested`'s own
  (inherited) table styling is untouched.
- *a test asserts the wrapper/rule around the enumerated tables* →
  `TestTableScrollWrapsDataTables`.
- *gofmt/go vet/go test ./internal/web green (CGO_ENABLED=0)* → verified.
