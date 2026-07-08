# stats: per-chip filter operators (`=`, `!=`, `=~`), LogQL-style

## Problem

`stats-filter-groupby` gave `/stats` a FILTER BAR, but every chip was a fixed
`tag=value` equality test (`matchesFilters`). There was no way to ask "every review
EXCEPT sonnet's" (`model!=sonnet`) or "any cohort starting with `exp-`"
(`cohort=~^exp-`) without reading raw transcripts by hand — only exact-match chips.

## Fix

### Filter model (`internal/web/stats.go`)

- `tagFilter` grows an `Op` field: `""`/`"="` (equality, and `""` is the zero value
  so every pre-existing chip/literal keeps working unchanged), `"!="` (not-equal),
  `"=~"` (regex-match). Op is a per-chip **selector**, never typed syntax — there is
  still no query-expression language.
- `matchesFilters` switches on `f.Op`: `!=` fails the chip when `tags[Key] ==
  Value` (a tag a session's resolved set omits reads as `""`, so the existing
  missing-tag-reads-as-empty-string semantics carry over unchanged); `=~` compiles
  `Value` as a regex and fails the chip unless it matches `tags[Key]` — an
  unparsable pattern degrades to "matches nothing" (never a Go error, never a
  panic, never a 500), the same "unsatisfiable filter yields an empty group"
  contract every other chip already has.
- `parseFilters` detects the operator off the raw `filter=key<op>value` chip: a
  trailing `!` on the key (`key!=value`) selects `!=`; a leading `~` on the value
  (`key=~value`) selects `=~`; anything else keeps the pre-existing bare-`=`
  parse (`Op` stays `""`), so every `stats-filter-groupby`-era chip/URL/test is
  untouched byte-for-byte.
- `buildStatsURL` reconstructs `Key+op+Value` per chip (defaulting `""`/`"="` back
  to a literal `=`), so the URL shape stays the single grammar every control
  (chip removal, group-by toggle, add-filter redirect) converges on —
  `filter=k<op>v`, still one query param per chip, still shareable/bookmarkable.
- `parseFilterOp` normalizes the add-filter form's new `fop` selector value the
  same way: `!=`/`=~` pass through, anything else (missing, `=`, or a
  hand-built request's garbage) normalizes to `""` equality.

### Handler + template

- `(*Server).stats`'s add-filter branch reads `fop` (via `parseFilterOp`)
  alongside the existing `fkey`/`fval` and folds it into the new chip before the
  303 redirect — the operator is chosen from a `<select>`, never typed into a
  combined string.
- The rendered `filterChip` always carries the DISPLAY form of `Op` (`"="` /
  `"!="` / `"=~"`, never `""`) so a plain-equality chip still shows its operator
  in the UI even though the underlying `tagFilter.Op` zero value is `""`.
- `internal/web/templates/stats.html`: the add-filter form gains an `fop`
  `<select>` (`=`/`!=`/`=~ (regex)`) between the key and value inputs; chips
  render `{{.Key}}{{.Op}}{{.Value}}` instead of the hardcoded `=`. **Both**
  forms that re-post the currently-active filters as hidden inputs (the
  add-filter form AND the separate group-by form) were updated to
  `value="{{.Key}}{{.Op}}{{.Value}}"` — missing the second one would have
  silently downgraded an active `!=`/`=~` chip back to plain equality the
  moment an operator submitted the group-by checkboxes, a real regression
  caught while implementing this (see `TestStatsGroupedNotEqualOperator`'s
  hidden-input assertion).
- No CSS changes: the new `<select>` inherits the existing generic
  `input, select, textarea` rule (`internal/web/assets/style.css`), same as
  every other form control on the page.

## Tests (`internal/web/web_test.go`)

- `TestParseFiltersOperators` — unit-tests the raw-string parse for all three
  operators plus the operator-only-no-key malformed case.
- `TestMatchesFiltersOperators` — unit-tests `matchesFilters` directly: `=`
  unchanged, `!=` (including the missing-tag-reads-as-"" edge on both a
  non-empty and an empty `Value`), `=~` (match, no-match, and an invalid
  pattern degrading to no-match), and an AND across all three in one chip set.
- `TestStatsGroupedNotEqualOperator` — end-to-end `!=` acceptance case:
  `model!=sonnet` excludes only the sonnet session (an opus session AND a
  no-model-header session both survive); flipping to `model!=` (empty value)
  excludes exactly the no-model session instead. Also asserts the hidden
  filter re-post input appears, with the operator, in BOTH forms.
- `TestStatsGroupedRegexOperator` — end-to-end `=~` acceptance case: an
  anchored pattern matches only the opus session, not sonnet.
- `TestStatsGroupedInvalidRegexNoMatches` — an unparsable pattern yields zero
  rows through `computeGroupedStats` (no error) and a 200 (never 500) through
  the real handler.
- `TestStatsGroupedMixedOperatorAND` — the acceptance case's core scenario:
  one chip per operator ANDed together, with each of three "wrong" sessions
  failing EXACTLY one chip, proving every operator actually restricts the pool
  (not just one of them).
- `TestStatsAddFilterRedirectWithOperator` — the add-filter form's `fop`
  selector (`!=`, `=~`, and a garbage value normalizing to equality) flows
  through the 303 redirect into the canonical `filter=k<op>v` URL.
- Every pre-existing `stats-filter-groupby` test
  (`TestStatsGroupedFilterAndGroupByModel`, `TestStatsGroupedTwoFilterAND`,
  `TestStatsGroupedTwoKeyGroupBy`, `TestStatsDefaultUnchanged`,
  `TestStatsGroupedUnknownTagFilter`, `TestParseFiltersAndGroupBy`,
  `TestStatsAddFilterRedirect`) passes UNCHANGED — none needed editing, since
  `Op`'s zero value (`""`) is exactly equality.

`CGO_ENABLED=0 go build ./...` and `go test ./...` green across every package.

## Caveats honored

- Still no query-expression language: the operator is a per-chip selector
  (`Op`/the `fop` `<select>`), never a typed string an operator composes by
  hand.
- Read-only throughout: `matchesFilters`/`computeGroupedStats`/the handler's
  redirect branch never write anything.
- An invalid `=~` pattern is a data problem, not a program error: it degrades
  to an empty result, never a Go `error` return, never a 500.
- The URL shape stays a single grammar (`buildStatsURL`) every control
  converges on, still shareable/bookmarkable, still one `filter` query param
  per chip.
- Never touched `ROI.md` or hand-edited `PLAN.md` task bodies.
