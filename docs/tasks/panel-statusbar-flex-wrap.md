# panel-statusbar-flex-wrap: reflow the .statusbar action row at narrow widths

## Problem

The shared `.statusbar` row (`internal/web/assets/style.css:646`) is
`display: flex; align-items: center; gap: var(--space-3)` with **no** `flex-wrap`,
so it defaults to `nowrap`; flex items also default to `min-width: auto`. Three
templates render this class verbatim — `editor_panel.html` (the general AI-edit
chat shell at `/editor/{id}`), `chatedit_panel.html` (the bootstrap wizard's fixed
chat session at `/bootstrap`), and `human_resolve_panel.html` (the NEEDS-HUMAN
resolution workspace). Each renders 2–3 flex children; several branches pair a
real-length status message (e.g. editor_panel's DeleteRisk branch,
"whole-file deletion of a human-owned file — blocked", 48 chars) with the primary
Approve/Reject/Merge/Publish action buttons. On a narrow / mobile viewport the row
overflows horizontally instead of reflowing — the same bug class
`session-list-row-flex-wrap` (pass 13) already fixed for `ul.sessions li`.

The sole width breakpoint `@media (max-width: 48rem)` (style.css:788) stacks
`.editor-grid` into a column but never touches `.statusbar`, so nothing mitigates
the overflow once the pane is full-width on mobile. This also breaks the codebase's
own now-eight-rule wrap convention (`.card-meta`, `.chips`, `ul.sessions li`,
`.breadcrumb ol`, `.nav-links`, `.file-index .file-links`, `form`,
`ul.secrets > li` all use `flex-wrap: wrap`).

## Design

One CSS rule, no template/markup/Go change. Add `flex-wrap: wrap` to `.statusbar`
(in `display` → `flex-wrap` → `align-items` order, matching the codebase
convention). The existing `gap: var(--space-3)` already applies on both axes, so
wrapped lines keep spacing. This one rule fixes all three host templates at once.

Desktop (wide) rendering is unchanged for rows that already fit on one line: a
`flex-wrap: wrap` container whose content fits lays out identically to `nowrap`.
No template, button text, or conditional branch changes, so the single-binary
embed (`assetFS` at `/assets/style.css`) is preserved.

## Tests — `internal/web/web_test.go`

`TestStatusbarRowWraps` (new): serves `/assets/style.css`, extracts the
`.statusbar { … }` rule block, and asserts it carries `flex-wrap: wrap` (scoped to
that block, not merely somewhere in the sheet) AND remains `display: flex`. There
is no browser in the test harness, so this locks the embedded-stylesheet contract,
not rendered pixels. Negative control: removing the `flex-wrap: wrap` line fails
the test.

## Acceptance mapping

- *`.statusbar` carries `flex-wrap: wrap`; the row reflows at narrow/mobile widths
  in all three host templates instead of overflowing* → the one-line CSS rule;
  `TestStatusbarRowWraps` asserts the rule is present in the embedded sheet.
- *no change to any of the three templates' markup, button text, or branches* →
  CSS-only diff.
- *desktop (wide) rendering visually unchanged for rows that already fit* →
  `flex-wrap: wrap` is a no-op for content that fits on one line.
- *a test asserts the rule is present in the embedded stylesheet* →
  `TestStatusbarRowWraps`.
- *single-binary embed preserved; go vet / go test green (CGO_ENABLED=0); gofmt
  clean* → verified below.

## Verification

- `go vet ./internal/web/` clean.
- `go test ./internal/web/` green (CGO_ENABLED=0).
- `gofmt -l` clean on the touched Go file.
</content>
</invoke>
