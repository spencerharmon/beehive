# poll-pane-loading-skeletons: shaped, announced loading states for polled panes

## Problem

`ui-audit-001` (Finding #4, ranked #4 — empty & loading states) flagged a verbatim
ROI ask: "skeletons, not blank/spinning panes". Every htmx-polled pane's initial
shell was a bare `<p>loading…</p>` (or `<p class="muted">loading setup assistant…</p>`)
with **no `aria-live`**, repeated across six templates:

- `session_list.html` — `#session-list` (the sessions list, polled every 2s).
- `session_view.html` — `#session-body` / `#session-loading` (the transcript).
- `human_resolve.html` — `#resolve` (the resolution agent chat + diff).
- `editor.html` — `#editor` (the single-file AI editor chat + diff).
- `bootstrap_agent.html` — `#chatedit` (the setup wizard agent).
- `bootstrap_banner.html` — `#bootstrap-agent` (the dashboard bootstrap banner agent).

Two defects: a screen-reader user got **no notification when the swap landed**
(the shell was not a live region), and sighted users saw a plain-text flash
instead of a shaped placeholder sized to the eventual content.

Out of scope (explicitly, per the finding): `hx-trigger` / `hx-swap` wiring and
polling cadence — untouched here; they belong to `ui-audit-001`'s remaining
client-performance findings.

## Design

A small **reusable skeleton component** plus the `role="status" aria-live="polite"`
live-region marker on every polled shell. Two shapes cover all six panes, matching
their eventual content: a transcript-shaped block and list rows.

### `internal/web/templates/skeleton.html` (new)

Two `{{define}}` components, auto-parsed by the existing
`template.ParseFS(tmplFS, "templates/*.html")`:

- `skeleton-transcript` — a stack of six staggered-width mono-height lines on a
  surface panel, echoing a `<pre class="session">` transcript / chat log. Used by
  the five transcript/chat panes.
- `skeleton-list` — four full-width bordered rows echoing `ul.sessions` `<li>`
  rows. Used by the sessions list.

Each emits its shaped bars as **`aria-hidden="true"`** (decorative) followed by a
visually-hidden `<span class="sr-only">Loading…</span>` text equivalent. The
shell's own live region announces the swapped-in content.

### `internal/web/assets/style.css`

A token-only skeleton section (placed with the other loading/motion rules, right
after `#htmx-progress`):

- `.skeleton` — a `flex` column with `--space-2` gaps.
- `.skeleton-line` / `.skeleton-row` — filled with `--surface-2`, animated by the
  new `@keyframes bee-skeleton` (a neutral opacity pulse), **no color literal**.
- `.skeleton-transcript` — a `--surface` panel (border + `--radius` + padding)
  whose lines are text-row height with staggered `nth-child` widths so it reads as
  a wrapped transcript, not a table.
- `.skeleton-list` — carries `ul.sessions`' `--space-4` vertical margin so the
  swap-in list does not shift layout; its rows are full-width, bordered, `2.25em`.
- `@media (prefers-reduced-motion: reduce)` disables the pulse on both bar kinds —
  the same gating the existing `.active` claim pulse and `#htmx-progress` use.
- `.sr-only` — a standard visually-hidden utility for the skeletons' text
  equivalent (none existed before).

### Templates (markup-only)

Each of the six polled shells gains `role="status" aria-live="polite"` and swaps
its bare `<p>loading…</p>` for `{{template "skeleton-transcript"}}` (or
`skeleton-list` for the sessions list). `session_view.html` keeps its
`id="session-loading"` element (now a `<div>` wrapping the skeleton) because the
SSE stream path removes it by id (`document.getElementById('session-loading')`);
behaviour is preserved. `bootstrap_banner.html`'s outer `<section role="status">`
is left intact; the inner polled `#bootstrap-agent` is marked independently.

## Tests — `internal/web/web_test.go`

`TestPollPaneLoadingSkeletons`:

1. Asserts the embedded stylesheet carries the skeleton component
   (`.skeleton`, `.skeleton-transcript`, `.skeleton-list`, `.skeleton-line`,
   `.skeleton-row`), the `@keyframes bee-skeleton` pulse, the
   `prefers-reduced-motion` gate (`.skeleton-row { animation: none; }`), and the
   `.sr-only` utility — and that the shared fill rule introduces **no color
   literal** (token-driven).
2. Renders **all six** touched templates and asserts each renders its shaped
   skeleton (`skeleton-transcript` / `skeleton-list`), carries
   `role="status" aria-live="polite"`, no longer renders the bare loading text,
   and keeps its stable pane id (incl. `session_view.html`'s preserved
   `#session-loading`).

## Acceptance mapping

- *every one of the six listed initial panes shows a shaped skeleton (not bare
  "loading…") sized to its eventual content* → the six shells embed
  `skeleton-transcript` / `skeleton-list`; `TestPollPaneLoadingSkeletons` renders
  all six and asserts the shape class + absence of the bare loading node.
- *every polled shell carries `role="status" aria-live="polite"`* → added to all
  six; asserted per template.
- *skeleton motion respects `prefers-reduced-motion`* → `@keyframes bee-skeleton`
  is disabled under the reduced-motion media query; asserted.
- *tests cover at least two of the six touched templates* → covers all six.
- *does not touch hx-trigger/hx-swap/cadence* → only `role`/`aria-live` attrs and
  the inner loading node changed; poll wiring is byte-identical.
- *gofmt / go vet / go test ./internal/web green (CGO_ENABLED=0)* → verified.
