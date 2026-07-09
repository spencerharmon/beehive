# skip-to-content-link: keyboard skip link past the repeated top nav

## Problem

`ui-audit-002` (Finding #6, accessibility / keyboard nav) found that
`internal/web/templates/layout.html`'s top nav renders 7 links (dashboard / stats
/ secrets / merge / human / hygiene / skills) identically on **every** page, and
`<main class="container">` carried no `id`. There was no skip affordance
(`grep -i skip internal/web/templates/` was empty), so a keyboard or screen-reader
user had to tab through all 7 nav links on every single page load before reaching
the main content. This is the exact problem the standard "skip to content" pattern
solves, and it is a purely layout-local defect (the nav and `<main>` live in the one
shared layout template).

## Fix

Layout-local and additive, across three files. No new markup is shown to a mouse
user (the link is visually hidden until keyboard focus), and no new design token is
introduced.

### `internal/web/templates/layout.html`

- Prepend `<a class="skip-link" href="#main">Skip to content</a>` immediately after
  `<body>`, making it the **first focusable element** on every page. The
  `#htmx-progress` / `#htmx-toast` divs that precede the nav are non-focusable
  status/alert regions, so the link is genuinely first in tab order.
- Give the landmark the jump target: `<main class="container" id="main"
  tabindex="-1">`. `tabindex="-1"` makes the otherwise non-interactive `<main>`
  programmatically focusable, so activating the link moves keyboard focus **into**
  the main content rather than only scrolling the viewport to it.

### `internal/web/assets/style.css`

- **Reuse** the existing `.sr-only` visually-hidden utility instead of adding a
  second, colliding hidden-text rule: `.skip-link` is grouped into that one rule
  (`.skip-link, .sr-only { … }`), so by default the link is clipped off-screen
  exactly like `.sr-only`. The utility's doc comment is broadened to name this third
  use (skeleton loading text, sr-only form labels, and now the skip link's hidden
  base). `.skip-link` is grouped **before** `.sr-only`, preserving the exact
  `.sr-only {` substring that the `poll-pane-loading-skeletons` test locks.
- Add `.skip-link:focus` — on keyboard focus the link un-clips to a normal, visible
  control pinned top-left (`position: fixed; top/left: var(--space-2); clip: auto;
  auto width/height; visible padding`), above the sticky topbar / htmx overlays
  (`z-index: 100`, clearing the topbar `10`, progress `50`, toast `60`). The rule is
  built **only** from existing design tokens (`--surface`, `--accent`, `--border`,
  `--border-width`, `--radius`, `--shadow`, `--space-2`, `--space-4`, `--text-sm`) —
  no new color token. The reveal is a discrete state change (no transition /
  animation), so `prefers-reduced-motion` is respected by construction, and the shown
  control inherits the shared `a:focus-visible { outline … }` convention already in
  the sheet.

## Tests — `internal/web/web_test.go`

`TestSkipToContentLink` (new) renders the full layout shell via the dashboard `GET /`
handler and asserts:

1. the exact `<a class="skip-link" href="#main">Skip to content</a>` markup is
   present;
2. it precedes `class="topbar"` within `<body>` (first focusable element, before the
   nav);
3. `<main class="container" id="main" tabindex="-1">` is the jump target;

then, off `GET /assets/style.css`:

4. `.skip-link,` is present — the link reuses the grouped `.sr-only` visually-hidden
   base rather than defining a second hidden-text rule;
5. `.skip-link:focus {` exists (the reveal-on-focus rule);
6. that reveal rule contains no `#` or `hsl(` color literal — proving it stays
   token-driven and "adds no new color token".

Gates (CGO_ENABLED=0): `gofmt -l internal/web/web_test.go` clean, `go vet
./internal/web` clean, `go test ./internal/web` green — including the pre-existing
`TestPollPaneLoadingSkeletons` and `TestFormInputsHaveAccessibleNames`, whose
`.sr-only` assertions the grouping preserves.

## Acceptance mapping

- *a Skip-to-content link is the first focusable element in `<body>`, hidden until
  keyboard focus, jumping to `#main`* → prepended `<a class="skip-link"
  href="#main">`; `.skip-link` clipped via the shared `.sr-only` base and revealed
  only by `.skip-link:focus`; the test checks presence + position-before-nav.
- *`<main>` carries `id="main"` (focus lands in main after activation)* → `<main …
  id="main" tabindex="-1">`; the negative tabindex lets focus land on the landmark.
- *reveal styling reuses `.sr-only`, adds no new color token, respects existing
  focus/reduced-motion conventions* → grouped `.skip-link, .sr-only`; reveal uses
  only existing tokens (asserted: no color literal in the rule); instant reveal (no
  motion) + shared `a:focus-visible` outline.
- *a test asserts the skip link (`href="#main"`) + `id="main"` target* →
  `TestSkipToContentLink`.
- *gofmt / go vet / go test ./internal/web green (CGO_ENABLED=0)* → all green above.

## Notes

- The three code files (`layout.html`, `style.css`, `web_test.go`) were shipped in
  commit `d22f4b1` and pass every `Accept:` gate; per the arbitration
  (`docs/skip-to-content-link-arbitration.md`) they are left **untouched** and this
  change adds only this `docs/tasks/` design doc — the one deliverable the original
  commit omitted — completing the delivery per the zero-exception `docs/tasks/`
  convention.
- Never touched `ROI.md` or hand-edited `PLAN.md` task bodies.
