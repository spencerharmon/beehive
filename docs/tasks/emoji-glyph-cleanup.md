# emoji-glyph-cleanup: drop redundant emoji-next-to-own-word in the web UI

## Problem

`ui-audit-003` (Finding #7, ranked lowest — cosmetic / consistent iconography;
carried forward from `ui-audit-001`->`002`) found labels that repeat an emoji
glyph next to its own word, so the icon adds no information the text doesn't —
and, worse, announces a redundant token to assistive-tech (a screen reader reads
"honeybees bee" for `honeybees 🐝`). Re-verified instances:

- `internal/web/templates/stats.html` — `delivered tasks ✅`, `honeybees 🐝`,
  `delivered ✅` in the prose legend (l.4-5) and every stats table header
  (l.57, l.70, l.81, l.115).
- `internal/web/templates/dashboard.html` (l.28) — a bare `🐝` glyph prefixing
  the honeybee count on each submodule card.
- `⚠️` beside "deletion"/"blocked" in the delete-risk banner
  (`editor_panel.html` l.12).

Lowest severity (a text alternative is already present in every case) but
concrete: the fix is textual template edits only — no CSS, no layout, no
behavior.

## Fix — templates only

### `internal/web/templates/stats.html`

Dropped the glyph wherever an adjacent word already carries the meaning:

- l.4 `<b>delivered tasks (✅)</b>` -> `<b>delivered tasks</b>`
- l.5 `<b>honeybees (🐝)</b>` -> `<b>honeybees</b>`
- l.57, l.70 header `delivered tasks ✅` / `honeybees 🐝` -> `delivered tasks` /
  `honeybees`
- l.81, l.115 by-model header `delivered ✅` / `honeybees 🐝` -> `delivered` /
  `honeybees`

### `internal/web/templates/dashboard.html`

- l.28 the per-card honeybee badge `🐝 {{.Bees}}` -> `bees {{.Bees}}`. The badge
  is a COUNT + text label (rendering `bees 0`, `bees 1`, ...), matching its two
  sibling count badges on the same card — `pending {{.Pending}}` and
  `needs-human {{.Human}}` — rather than a bare glyph. The `.badge.bees`/
  `.bees-live` classes and the `title="honeybees actively working this
  submodule…"` hover are untouched, so styling and the live-teal overlay are
  unchanged.

### `internal/web/templates/editor_panel.html`

- l.12 delete-risk banner `<span class="state danger">⚠️ whole-file deletion of
  a human-owned file — blocked</span>` -> drop the `⚠️`. The words "deletion"
  and "blocked" already carry the meaning, and the danger is reinforced by the
  adjacent red `button.danger` ("Confirm deletion & merge") and its `hx-confirm`
  dialog — the glyph was pure redundancy.

## Glyphs KEPT (each named + justified)

Per the task's keep-rule ("KEEP a glyph that is the SOLE label or a compact
legend or a state marker the word doesn't emphasize"):

1. **`✅/🐝` ratio** — `stats.html` l.2 (`<h1>` badge), l.6 (verbal definition
   `✅/🐝 = delivered tasks per honeybee`), and the `✅/🐝` column header on
   l.57/l.70/l.81/l.115. This is a compact ratio legend: the glyphs ARE the
   label (no adjacent word repeats them), the task explicitly names it as the
   legend to keep, and l.6 defines it in words. It is also asserted by
   `web_test.go` (`/stats` must contain `✅/🐝`).
2. **Error-message `⚠️`** — `<div class="msg err">⚠️ {{.Error}}</div>` in
   `editor_panel.html` l.5, `human_resolve_panel.html` l.5, and
   `chatedit_panel.html` l.7. This is the SOLE announced marker that the chat
   entry is an error: unlike a normal `.msg` entry it has NO `.who` text label,
   and its `.msg.err` red background is a VISUAL-only cue a screen reader never
   announces. The `⚠️` sits beside the dynamic `{{.Error}}` text — not a fixed
   word emphasizing the error state — so it is exactly the guidance's "state
   marker the word doesn't emphasize" case. Dropping it would strip the only
   AT-perceivable error cue, the opposite of this finding's accessibility goal.

## Tests — `internal/web/web_test.go`

The dashboard assertions locked the old glyph, so they were repointed to the new
text label (the only behavior is the rendered string):

- `TestDashboardCards`: the card-grid body-contains list `🐝 0` -> `bees 0`.
- `TestDashboardCardPolish`: idle `🐝 0` -> `bees 0`, live `🐝 1` -> `bees 1`,
  plus the surrounding explanatory comments. The `badge bees bees-live`
  class-modifier assertion is unchanged (styling untouched).

`TestStats`'s `✅/🐝` `/stats` assertion is unchanged and still green (the ratio
legend is kept). No test references `⚠️`. `gofmt -l`, `go vet ./internal/web`,
and `go test ./internal/web` are green with `CGO_ENABLED=0`.

## Caveats / out-of-scope

- The by-model nested tables read `delivered` (not `delivered tasks`) — that
  shorter wording pre-existed; only the glyph was removed, no label was
  reworded.
- `internal/web/web.go` l.546 has a code comment ("…surfaced on the card with
  the 🐝 badge") that now names the old glyph. `web.go` is outside this task's
  enumerated Files and the "textual template edits only" scope, so it was left
  untouched rather than creep the change surface; flagged here for a possible
  follow-up.
- No CSS, layout, or behavior changed; every affected label still reads without
  the glyph. Never touched `ROI.md`; `PLAN.md` touched only for this task's own
  status transition.
