# session-list-row-flex-wrap: reflow the session-list row at narrow widths

## Problem

`session-list-links-labels` grew each `ul.sessions` `<li>` from ~3 items to up to
8 — the session-name link, up to four badges, the task/doc/commit deep links, and
the right-pushed timestamp. But `ul.sessions li` (`internal/web/assets/style.css`)
is `display: flex` with **no** `flex-wrap`, so it defaults to `nowrap`; flex items
also default to `min-width: auto`. On a narrow / mobile viewport the grown row
therefore overflows horizontally instead of reflowing — the classic mobile
horizontal-scroll bug, on the session list (a core per-submodule operator page).

The sole width breakpoint `@media (max-width: 48rem)` adapts only `.editor-grid`,
never `.sessions`. This also breaks the codebase's own convention: every other
multi-item pill/meta row wraps — `.card-meta` and `.chips` are both
`display: flex; flex-wrap: wrap`. No prior task covers it
(`responsive-table-overflow` was `<table>`-scoped and explicitly excluded the
card-per-row collapse).

## Design

One CSS rule, no template/markup change. Add `flex-wrap: wrap` to `ul.sessions li`
(in `display` → `flex-wrap` → `align-items` order, matching `.card-meta` /
`.chips`). The existing `gap: var(--space-2)` already applies on both axes, so
wrapped lines keep spacing; `ul.sessions .ago { margin-left: auto }` still
right-aligns the timestamp on wide widths and degrades cleanly on wrap (the
auto-margin simply has no free space to consume once the row wraps).

Desktop (wide) rendering is unchanged for rows that already fit on one line: a
`flex-wrap: wrap` container with content that fits lays out identically to
`nowrap`. `session_list_body.html` and every badge/link markup are untouched, so
the single-binary embed (`assetFS` at `/assets/style.css`) is preserved.

## Tests — `internal/web/web_test.go`

`TestSessionListRowWraps` (new): serves `/assets/style.css`, extracts the
`ul.sessions li { … }` rule block, and asserts it carries `flex-wrap: wrap` (scoped
to that block, not merely somewhere in the sheet) AND remains `display: flex`; it
also confirms the `ul.sessions .ago { margin-left: auto; }` right-push rule is
still present so the timestamp keeps its wide-width alignment. There is no browser
in the test harness, so this locks the embedded-stylesheet contract, not rendered
pixels. Negative control: removing the `flex-wrap: wrap` line fails the test.

## Acceptance mapping

- *`ul.sessions li` carries `flex-wrap: wrap` and the grown row reflows onto
  multiple lines at narrow widths instead of overflowing* → the one-line CSS rule;
  `TestSessionListRowWraps` asserts the rule is present in the embedded sheet.
- *no change to `session_list_body.html` or any badge/link markup* → CSS-only diff.
- *desktop (wide) rendering visually unchanged for rows that already fit* →
  `flex-wrap: wrap` is a no-op for content that fits on one line.
- *the timestamp still right-aligns on wide widths and degrades cleanly on wrap* →
  `ul.sessions .ago { margin-left: auto }` is preserved; the test asserts it.
- *a test asserts the rule is present in the embedded stylesheet* →
  `TestSessionListRowWraps`.
- *single-binary embed preserved; go vet / go test green (CGO_ENABLED=0); gofmt
  clean* → verified in the change doc.
