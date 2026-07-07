# stats: tag-based filter bar + group-by aggregation (LogQL-inspired, no expression language)

## Problem

`/stats` (`internal/web/stats.go`) always rendered the same fixed per-submodule +
total view. `stats-tag-model` gave every session an open, git-derived tag set
(`sessionTags`: built-ins `{submodule, kind, branch, model}` plus config-declared
tags) but built no way for an operator to actually FILTER or GROUP by it. There was
no way to ask "how do opus and sonnet compare on reviews" or "give me a per-target
A/B grid" without reading raw transcripts by hand.

## Fix

### Filter model (`internal/web/stats.go`)

- `tagFilter{Key, Value}` is one ANDed `tag=value` equality chip. `matchesFilters`
  requires EVERY chip to match a session's resolved tags (`sessionTags`); a tag
  absent from the map reads as `""` (the accessor never stores an empty value), so
  a filter on an unknown key or an unknown value simply matches nothing — no
  special-casing needed for the "unknown tag yields an empty group, never an
  error" contract.
- `parseFilters` reads every `filter=key=value` query param (repeated params are
  the FILTER BAR's shape); a malformed chip (no `=`, empty key) is silently
  dropped — there is no query-expression language, only composable k=v chips.

### Group-by aggregation (`computeGroupedStats`)

- `groupBy []string` is an ordered tag-key list. `tagValues` resolves each key
  against a session's tag map (`""` for an omitted key) into that session's
  TUPLE; sessions pooled across **every** submodule are bucketed by their exact
  tuple (`groupStat.Values`, aligned by index with the request's key list).
- Per-task attribution mirrors `computeStats`'s existing by-model logic exactly,
  generalized from "keyed by model" to "keyed by the resolved tuple": for each
  task, find its LATEST (highest epoch, then pid) session that survives every
  filter chip, and credit that task's delivered/stranded status to THAT session's
  tuple. A task with no filter-surviving session is attributed nowhere (correctly
  absent from a `kind=review` view if none of its sessions were review passes).
- `strandedCount` is split into `strandedTaskSet` (the underlying set) +
  `strandedCount` (a thin `len()` wrapper preserving the old signature/behavior
  for `computeStats`), so the grouped view can attribute EACH stranded task id to
  its tuple the same way delivered tasks are.
- `groupBy == nil` collapses every filter-surviving session into the single `""`
  tuple — a filtered aggregate with no breakdown (the filter-only, no-group-by
  case). Rows are sorted lexicographically by their own tuple values for a
  deterministic render, independent of submodule/map iteration order.

### Query-param contract (`parseGroupBy`, `buildStatsURL`)

- `group-by` accepts BOTH the canonical single comma-separated param
  (`group-by=model,submodule`, the shareable-URL shape from the ROI's example) AND
  repeated params (`group-by=model&group-by=submodule`, what a plain HTML
  checkbox group posts with no JS) — either shape, or a mix, yields the same
  ordered, de-duplicated key list.
- `buildStatsURL(filters, groupBy)` is the ONE place that composes a `/stats`
  query string, so every chip-removal link, group-by-toggle form, and
  add-filter redirect converge on the identical canonical shape — the URL an
  operator bookmarks is always `filter=k=v&filter=k=v&group-by=a,b`.

### Handler (`(*Server).stats`)

- **Unchanged default**: with `filters` and `groupBy` both empty, the handler
  calls the pre-existing `computeStats` exactly as before — byte-identical
  per-submodule/total/by-model/deliveries output (`stats-filter-groupby`'s "empty
  filter set + no group-by == today's default view" contract). This is enforced
  structurally: the old code path is untouched, just gated behind
  `if !filtered`.
- Either param present switches to `computeGroupedStats` instead.
- **No-JS add-filter control**: the FILTER BAR's add-filter form posts two plain
  `fkey`/`fval` inputs (composable chips, never a query string to type) rather
  than a pre-joined `filter=k=v`. The handler detects `fkey` present, appends the
  new chip to the already-active filters, and 303-redirects to
  `buildStatsURL(...)` — so the URL the operator lands on (and can bookmark)
  always matches the one grammar every other control emits. Read-only throughout:
  the handler only ever computes and redirects, never writes.

### Template (`internal/web/templates/stats.html`)

- A persistent `filter-bar` card (always rendered, even in the default view):
  removable chips (`RemoveHref` precomputed server-side per chip — every OTHER
  filter + the current group-by preserved, a plain link, no JS), the add-filter
  form (key input with a `<datalist>` of the built-in tag keys + a value input),
  and a group-by selector (one checkbox per built-in tag key, checked state from
  `GroupBySet`, plus a free-text "extra keys" input prefilled with any active
  config-declared group-by keys via `extraGroupBy` so resubmitting the checkboxes
  never silently drops them).
- `{{if .Filtered}}` switches between the new grouped table (columns = the
  request's group-by keys, in order, then delivered/honeybees/yield/stranded) and
  the untouched default per-submodule/total table. `{{range .Grouped}}...{{else}}`
  renders a plain "no sessions match every filter" row on an empty result — never
  an error page.

### CSS (`internal/web/assets/style.css`)

New `.chips`/`.chip`/`.groupby-form label` rules reusing the existing design
tokens (`--surface-2`, `--border`, `--radius-pill`, `--danger` on chip-remove
hover) — no new dependencies, matches `htmx-polish`'s existing look.

## Tests (`internal/web/web_test.go`)

- `TestStatsGroupedFilterAndGroupByModel` — `filter=kind=review&group-by=model`:
  one row per model, a same-submodule `kind=work` session on the SAME model is
  excluded (proves the filter actually restricts the pool, not just group-by).
- `TestStatsGroupedTwoFilterAND` — two filter chips (`kind=review`,
  `submodule=alpha`) intersect: excludes both alpha's non-review session AND
  beta's review session, each matching only one of the two chips.
- `TestStatsGroupedTwoKeyGroupBy` — `group-by=model,submodule` across two
  submodules emits one row per distinct `(model, submodule)` TUPLE (3 rows for 3
  tuples), not one row per key.
- `TestStatsDefaultUnchanged` — `GET /stats` with no params still renders the
  exact pre-existing `computeStats` numbers, the filter bar's empty-chip state,
  and never the grouped view's empty-result copy.
- `TestStatsGroupedUnknownTagFilter` — an unknown value AND an unknown key each
  yield zero rows with no error, end-to-end through the real handler (200, not
  500).
- `TestParseFiltersAndGroupBy` — malformed filter chip dropped; `group-by` CSV
  and repeated-param shapes both parse to the same deduplicated list.
- `TestStatsAddFilterRedirect` — the fkey/fval control redirects to the
  canonical `filter=k=v` URL, preserving an already-active filter and group-by.

`CGO_ENABLED=0 go build ./...` and `go test ./...` green across every package.

## Caveats honored

- Read-only throughout: `computeGroupedStats` and the handler's redirect branch
  never write anything.
- Git-derived/stateless: every figure is re-derived from `sessions/*.md` +
  `PLAN.md` + git branch state on each request, same as the pre-existing view.
- No query-expression language: filters are k=v equality chips ANDed, group-by is
  a key list — no parser, no operators.
- Never touched `ROI.md` or hand-edited `PLAN.md` task bodies.
