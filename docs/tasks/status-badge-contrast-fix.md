# status-badge-contrast-fix: WCAG AA contrast on solid-fill status badges

## Problem

`ui-audit-001` (Finding #1) measured several solid-fill status/state badges in
`internal/web/assets/style.css` — via the standard WCAG relative-luminance
formula against the exact declared `hsl()` values, not eyeballed — under the
4.5:1 normal-text contrast threshold for their white foreground text:

| Pill(s) | Mode | Was | Threshold |
|---|---|---|---|
| `.badge.live` | both (mode-invariant) | 3.30:1 | 4.5:1 |
| `.badge.env-green` / `.state.live` / `.badge.drift-clean` | light | 3.99:1 | 4.5:1 |
| `.badge.env-green` / `.state.live` / `.badge.drift-clean` | dark | 2.27:1 | 4.5:1 (fails even the relaxed 3:1 large-text minimum) |
| `.badge.drift-drift` | both (mode-invariant) | 3.47:1 | 4.5:1 |
| `.state.dirty` / `.badge.drift-missing` | dark | 3.72:1 | 4.5:1 |

None of these pills reach the WCAG "large text" exception (`--text-xs`/
`--text-sm` at weight 600 is well short of 14pt-bold), and they carry real
operational meaning (session running, env green, file clean) — a genuine
accessibility defect, not cosmetic.

## Why not just retune `--success`/`--danger`

`--success`/`--danger` already do double duty: `.ok` uses them as plain TEXT
color against `--surface`, and `button.merge` pairs a NEAR-BLACK
`--accent-contrast` foreground (dark mode) against a `--success` fill. Both of
those want the OPPOSITE lightness move a white-text solid pill needs — the
pill needs a DARKER fill for more contrast with white text; the near-black
button text wants a LIGHTER fill for more contrast with itself. Retuning the
shared tokens to fix the pills would have regressed those unrelated
consumers. So the fix decouples the pill fill from `--success`/`--danger`
entirely, onto new dedicated tokens.

## Fix — `internal/web/assets/style.css`

Two new custom properties, declared in `:root` (light) and overridden in the
`prefers-color-scheme: dark` block (same "tokens differ, rules never differ"
architecture the whole stylesheet already follows):

```css
--badge-solid-green: hsl(var(--hue-done) 58% 32%);        /* light */
--badge-solid-red:   hsl(var(--hue-arbitration) 68% 48%); /* light */
/* ...dark override... */
--badge-solid-green: hsl(var(--hue-done) 50% 34%);        /* dark */
--badge-solid-red:   hsl(var(--hue-arbitration) 72% 50%); /* dark */
```

Hue is borrowed from the SAME `--hue-done`/`--hue-arbitration` tokens the
translucent `.status-done`/`.status-needs-arbitration` pills use, so the
green/red identity is pixel-identical in hue to the rest of the design system
— only lightness (and, matching each token's pre-existing per-mode
saturation, nothing else) was retuned. Consumers were repointed from
`var(--success)`/`var(--danger)` to the new tokens:

- `.badge.env-green`, `.badge.drift-clean`, `.state.live` -> `var(--badge-solid-green)`
- `.badge.drift-missing`, `.state.dirty` -> `var(--badge-solid-red)`

The two mode-invariant pills that already hardcoded a literal `S% L%` next to
a borrowed hue var (no token needed, since they don't vary by mode) just got
that literal darkened:

- `.badge.live`: `hsl(var(--hue-done) 58% 40%)` -> `58% 32%`
- `.badge.drift-drift`: `hsl(var(--hue-review) 70% 42%)` -> `70% 34%`

Light-mode `.state.dirty`/`.badge.drift-missing` (`hsl(2 68% 48%)`) already
cleared 4.5:1 (5.23:1) and is carried into `--badge-solid-red`'s light value
UNCHANGED — only the dark override needed retuning.

Resulting ratios (all >= 4.5:1, computed the same way as the finding):

| Pill(s) | Mode | Now |
|---|---|---|
| `.badge.live` | both | 4.875:1 |
| `.badge.env-green`/`.state.live`/`.badge.drift-clean` | light | 4.875:1 |
| `.badge.env-green`/`.state.live`/`.badge.drift-clean` | dark | 4.753:1 |
| `.badge.drift-drift` | both | 5.002:1 |
| `.state.dirty`/`.badge.drift-missing` | light | 5.231:1 (unchanged) |
| `.state.dirty`/`.badge.drift-missing` | dark | 4.800:1 |

No non-color tokens touched (spacing/radius/font-size/border-width all
unchanged); no template changes (no hardcoded colors or inline styles
reference these badges — grepped `internal/web/templates/`, confirmed clean).

## Tests — `internal/web/web_test.go`

`TestBadgeSolidFillsMeetWCAGAA` reads the EMBEDDED stylesheet at test time
(`assetFS.ReadFile("assets/style.css")`), regex-extracts the actual declared
hue/saturation/lightness for every enumerated pill (not hardcoded literals
duplicated from the CSS), reimplements the standard WCAG relative-luminance +
contrast-ratio formula in Go, and asserts every one is >= 4.5:1 in both light
and dark — so a future edit that quietly drags any of these back under
threshold fails the build instead of shipping a silent regression. It also
locks: (a) the enumerated consumers route through `--badge-solid-green`/
`--badge-solid-red` rather than back through `--success`/`--danger`, and (b)
the `.status-*` hue wiring (`--hue-done`/`--hue-review`/`--hue-arbitration`)
is untouched, proving the hue identity was preserved.
`TestBadgeContrastHelpersKnownValues` cross-checks the WCAG helpers themselves
against well-known reference ratios (white-on-white 1:1, black-on-white 21:1,
pure red vs white ~3.998:1) so a bug in the helpers can't rubber-stamp the
locking test. `gofmt -l`, `go vet ./...`, and `go test ./...` all green.

## Downstream consumers spot-checked

- **plan-view-pills** (`templates/plan_items.html`): both `.badge.live` uses
  (dependency-satisfied, active-claim) get the same darker/still-clearly-green
  fill; class names and markup untouched.
- **dashboard-cards** (`templates/dashboard.html`): `.badge.env-green` and the
  `.badge.drift-*` trio (root-instruction-file-links) render through the new
  tokens; `.badge.human`/`.badge.bees.bees-live` (untouched hues, out of this
  finding's scope) are unaffected.
- **htmx-polish** (`templates/{chatedit,editor,human_resolve}_panel.html`):
  `.state.live`/`.state.dirty` get the new tokens; no markup change.

No template, Go struct, or JS references a literal color value for any of
these classes (grepped), so the CSS-only change is the complete fix surface.

## Caveats / out-of-scope

- `.badge.human` (`hsl(var(--hue-human) 60% 45%)`, violet) already clears
  4.5:1 (6.17:1) — untouched, not in the finding.
- `.badge.bees.bees-live` (`hsl(var(--hue-active) 62% 42%)`, teal) computes to
  **2.76:1**, also a genuine WCAG AA failure — but it is NOT in this task's
  enumerated pairs or its hue-identity list (green/amber/red/violet; teal/
  `--hue-active` is absent), and the "actively worked" teal hue is a
  different semantic category (activity indicator, not task status) from the
  four this finding scoped to. Left untouched to stay inside this task's
  Accept criteria; flagging here for a possible follow-up finding rather than
  silently fixing (scope creep) or silently ignoring it.
- Never touched `ROI.md` or hand-edited `PLAN.md` task bodies.
