# bee-glyph-restore: restore the 🐝 counter glyph (with an accessible name) on the two bee-count surfaces

## Problem

ROI (the "Refine the submodule card" bullet) now makes the honeybee 🐝 counter glyph
REQUIRED intent and explicitly REVERSES two prior DONE tasks:

- `emoji-glyph-cleanup` rewrote the dashboard submodule-card honeybee counter
  (`dashboard.html:28`) from `🐝 {{.Bees}}` to a plain-text `bees {{.Bees}}` label.
- `stats-active-now-bee-label` dropped the `🐝 ` prefix from the stats per-submodule
  "active now" cell (`stats.html:78`), leaving a bare number.

The literal 🐝 MUST render on exactly those two surfaces again — never a plain-text
"bees N" label, never a bare number. The accessibility concern those tasks cited
(an icon-only label announcing "honeybee N") is now met by ADDING an accessible name
ALONGSIDE the glyph, not by removing the glyph.

## Design

Textual template edits only — no CSS token, no new asset, no behavior/value change
(the canonical `.Bees` / `.ActiveNow` counts from active-honeybee-count-unify are
untouched). On each of the two counters:

- Give the count an `aria-label` naming the metric — `aria-label="{{.Bees}} honeybees
  active"` (dashboard) / `aria-label="{{.ActiveNow}} honeybees active"` (stats). This
  is the count's accessible name.
- Mark the decorative glyph `aria-hidden="true"` by wrapping it:
  `<span aria-hidden="true">🐝</span>`. AT announces the labelled count once (not
  "honeybee N"); the visible space sits outside the hidden span so the glyph is
  followed by `</span>`, never a bare space.
- Preserve everything else on the element: the existing `title`, the dashboard
  `badge bees{{if .Working}} bees-live{{end}}` class (its teal live modifier), the
  stats `badge live` class, and the stats `{{else}}0{{end}}` zero case.

### `internal/web/templates/dashboard.html:28`

```html
<span class="badge bees{{if .Working}} bees-live{{end}}" title="honeybees actively working this submodule right now (…)" aria-label="{{.Bees}} honeybees active"><span aria-hidden="true">🐝</span> {{.Bees}}</span>
```

### `internal/web/templates/stats.html:78`

```html
<td>{{if .ActiveNow}}<span class="badge live" title="honeybees actively working this submodule right now" aria-label="{{.ActiveNow}} honeybees active"><span aria-hidden="true">🐝</span> {{.ActiveNow}}</span>{{else}}0{{end}}</td>
```

### Scope guard (unchanged surfaces)

Only the bee counter on those two surfaces changes. NOT touched: the ⚠️ glyphs
`emoji-glyph-cleanup` removed from editor/human-resolve/chatedit panels; the
intentionally-kept ✅/🐝 ratio header/badge/legend; the stats TOTAL active-now cell
(`{{.Total.ActiveNow}}`, a plain bold number). Because the glyph is now explicit ROI
intent, later ui-audit passes must treat it as INTENDED, not a re-removable regression.

## Tests — `internal/web/web_test.go`

- `TestBeeCounterGlyphRestored` (replaces the now-inverted
  `TestStatsActiveNowNoBareBeeGlyph`) asserts BOTH surfaces render the 🐝 glyph WITH
  an accessible name: the exact stats active-now cell markup (glyph aria-hidden +
  `aria-label="3 honeybees active"` + preserved title), the dashboard counter markup
  (`aria-label="0 honeybees active"><span aria-hidden="true">🐝</span> 0` + preserved
  `badge bees` class), the stats zero case still a plain 0, the stats TOTAL cell still
  a bare `<td><b>3</b></td>`, and the kept ✅/🐝 ratio badge + header unchanged.
- `TestDashboardCards` and `TestDashboardCardPolish` updated: the interim `"bees 0"`
  / `"bees 1"` text-label assertions become the restored `aria-label="N honeybees
  active"` form; the `badge bees bees-live` live-modifier assertion is preserved.

## Acceptance mapping

- *dashboard counter renders the literal 🐝 with its count (not "bees N", not a bare
  number)* → template edit; asserted by `TestBeeCounterGlyphRestored` dashboard block.
- *stats active-now cell renders the 🐝 with its count* → template edit; asserted by
  the stats block.
- *both give the count an accessible name (aria-label) with the bare glyph
  aria-hidden* → the paired `aria-label` + `<span aria-hidden="true">🐝</span>`.
- *title + badge-live highlight + stats {{else}}0{{end}} zero case preserved* → class
  and title untouched; asserted (`badge bees bees-live`, zero-case plain 0).
- *⚠️ panels, kept ✅/🐝 ratio header/badge/legend, stats TOTAL active-now cell
  UNCHANGED* → not touched; TOTAL + ratio asserted unchanged.
- *a test asserts BOTH surfaces render the 🐝 glyph WITH an accessible name* →
  `TestBeeCounterGlyphRestored`.
- *gofmt / go vet / go test ./internal/web green (CGO_ENABLED=0); single-binary embed
  preserved (no new asset/CSS token)* → verified; templates are `//go:embed`-bundled,
  no CSS/asset added.
