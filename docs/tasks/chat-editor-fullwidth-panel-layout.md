# chat-editor-fullwidth-panel-layout: full-width, panel-style AI editor

## Problem

A Frontend-aesthetics reconcile ("Full-width, panel-style layout") asked the AI
chat-diff editor to feel dense and panel-driven (Midnight Commander / Emacs), not
a narrow centered column. The editor page rendered inside the shared shell's
`<main class="container">`, which caps content at `max-width: 66rem` and centers
it — so the two-panel `.editor-grid` (chat + diff) sat in a slim column with wide
empty gutters, and the panes had no visual "panel" framing.

## Design

Surface-only change in `internal/web` — the shell shell, the editor page handler,
and the one stylesheet. No editor/session behaviour changes; single-binary embed,
no SPA.

### 1. Opt-in full-width shell — `internal/web/templates/layout.html`

The header's `<main>` gains an opt-in modifier: `class="container{{if .Wide}}
container-wide{{end}}"`. Pages that do not set `.Wide` render exactly
`class="container"` as before (the centered 66rem column is unchanged and stays
the default for every other page). Only a page whose data sets `Wide: true` gets
the full-width surface.

### 2. Editor page opts in — `internal/web/editor.go`

`editorPage` adds `"Wide": true` to its render data, so the AI editor page (and
only it) renders in the full-width shell. The panel/chat/merge/API handlers and
the JSON API are untouched.

### 3. Styles — `internal/web/assets/style.css`

- `.container-wide { max-width: none; }` — drops the centered column cap while
  keeping the shell's side padding, so content spans the whole window.
- `.editor-grid` becomes a full-width dense two-panel layout: `width: 100%`,
  `align-items: stretch` (panes equal height), `min-height: 72vh`. Each pane
  (`.chat`, `.diffpane`) is now a bordered `--surface` "panel" (token border,
  radius, padding, shadow). The diff pane carries the extra width (`flex: 1.6`)
  and lays its `pre.diff` out to fill the panel height.
- The single narrow-viewport breakpoint (`@media (max-width: 48rem)`) is kept, so
  the two panels still stack to one column on a phone.

All values are design-system tokens (`--surface`, `--border`, `--radius`,
`--shadow`, `--space-*`); no new color literals.

## Tests — `internal/web/web_test.go`

`TestEditorFullWidthPanelLayout` (new): the editor shell rendered with
`Wide: true` carries `class="container container-wide"`; the dashboard (no Wide)
never carries `container-wide` (opt-in never leaks); the stylesheet ships
`.container-wide { max-width: none; }`, a `width: 100%` editor-grid rule, and
keeps the `@media (max-width: 48rem)` stack breakpoint.

## Acceptance mapping

- *editor spans the full window width, no narrow centered column* → `.Wide`
  flag + `.container-wide` (max-width: none) + `.editor-grid { width: 100% }`;
  asserted by `TestEditorFullWidthPanelLayout`.
- *dense two-panel layout* → the bordered-surface panes with `align-items:
  stretch` and the widened diff pane.
- *existing rendered content still displays correctly* → markup of the panes,
  diff rows, chat log, and merge controls is unchanged; the pre-existing editor
  panel/poll tests still pass. Every other page keeps the centered column
  (test asserts no `container-wide` leak).
- *`go test ./...` green under CGO_ENABLED=0* → verified.
