# session-poll-dom-morph: morph polled panes in place instead of full innerHTML replace

## Problem

Enqueued by ui-audit-004 (Finding #14 — client performance; carried across
ui-audit-002 -> 003 -> 004, deferred twice, now enqueued GATED not deferred again).
Every htmx-polled pane swapped its whole subtree with `hx-swap="innerHTML"` each
tick, rebuilding identical DOM (and its inner scroll container) even when a few
bytes changed:

- `#session-body` (`session_view.html`, every 2s while live)
- `#session-list` (`session_list.html`, every 2s)
- `#editor` (`editor.html`, every 1500ms)
- `#chatedit` (`bootstrap_agent.html`, every 1500ms)
- the human-resolve panel `#resolve` (`human_resolve.html`, every 2s)

A JS shim, `poll-scroll-preserve` (`layout.html`), existed only to paper over the
scroll loss a node-preserving morph avoids at the source: full innerHTML replace
destroys and recreates the pane's inner nodes every tick, so the browser zeroes
`scrollTop`, drops focus, and (since chat-editor-snappy-polish) collapses any
`<details class="agent-step">` the reader had expanded.

This was the heaviest/riskiest item the UI-audit series had queued: it has to
co-exist with (a) `layout.html`'s scroll-preserve shim, (b) `session_view.html`'s
SSE render path (mutates `#session-transcript.textContent` directly and cancels
the poll while `__sseLive`), and (c) `layout.html`'s etag-304 `shouldSwap=false`
guard (poll-fragment-etag-304). It was gated on `poll-backoff-ended-content` so
the selector held it until the session-poll path was stable.

## Design

### Vendor + embed idiomorph — `internal/web/assets/idiomorph-ext.min.js`

Vendored [idiomorph](https://github.com/bigskysoftware/idiomorph) 0.7.4
(`dist/idiomorph-ext.min.js`, 0BSD license, ~10KB), which bundles BOTH the core
`Idiomorph.morph()` engine and its htmx extension (`htmx.defineExtension("morph",
...)`) in one file — no separate core file to also vendor. Dropped straight into
`internal/web/assets/`, already covered by `web.go`'s existing `//go:embed
assets/*` + `http.FileServer(http.FS(assetFS))` at `/assets/`: no Go code change
needed to serve it. Single binary, no CDN, no new framework/SPA — same embed
contract htmx itself already has (`TestAssetsHtmxServed`).

Compatible with the vendored htmx 1.9.10: idiomorph 0.7.4's own devDependency
pins `htmx.org` `1.9.9`, and the extension mechanism it uses
(`htmx.defineExtension`, `hx-ext`, `handleSwap`) is present verbatim in the
vendored `htmx.min.js`.

### Wiring — `internal/web/templates/layout.html`

- `hx-ext="morph"` on `<html>` (not `<body>` — `TestSkipToContentLink` keys off
  the literal `"<body>"` substring, so the attribute goes one level up; htmx
  resolves `hx-ext` from every ancestor of the swap target, so `<html>` covers
  the same ground). One declaration covers all five panes and every
  button/form that swaps into one of them.
- `<script src="/assets/idiomorph-ext.min.js">` immediately after
  `htmx.min.js`. Order matters: the ext calls `htmx.defineExtension` at
  script-eval time, so it must load AFTER htmx defines the global (both are
  plain, synchronous `<script src>` tags in `<head>`, so DOM order is
  execution order). Locked by `TestLayoutEmbedsMorphExtNoCDN`.
- `morph-preserve-open` (new footer IIFE): idiomorph's default attribute sync
  removes any attribute present on the LIVE node but absent from the freshly
  rendered fragment. The server never renders `<details open>` (expand/collapse
  is pure client interaction), so an unpatched morph would strip it as
  "removed" on the very next poll, re-closing every step a reader just opened.
  Configures `Idiomorph.defaults.callbacks.beforeAttributeUpdated` (idiomorph's
  own documented extension point) to veto JUST the removal of `open`; every
  other attribute (and `open` being explicitly added, which never happens
  here) still syncs normally. Locked by `TestMorphPreservesDetailsOpenState`.
- `poll-scroll-preserve` simplified: a morphed node is never destroyed, so its
  `scrollTop` (and focus, and `<details open>`) already survives a poll tick
  for free — the "restore saved scrollTop" branch idiomorph now makes
  redundant is dropped. What morph CANNOT infer on its own is bottom-follow
  (a `[data-scroll-pin]` pane that was scrolled to the bottom does not
  automatically track new content just because the node persists), so the
  script keeps exactly that: on `htmx:beforeSwap`, note which
  `[data-scroll-preserve][data-scroll-pin][id]` panes are currently pinned; on
  `htmx:afterSwap`, re-scroll those to the bottom. The window-scroll
  save/restore (unrelated to morph — compensates for page reflow if a pane's
  height changes) is unchanged. A `[data-scroll-preserve]` pane WITHOUT
  `-pin` (the diff panes, the sessions list) now gets no JS at all.

### Pane + action swaps — `internal/web/templates/*.html`

`hx-swap="innerHTML"` -> `hx-swap="morph:innerHTML"` on all FIVE polled panes,
AND on every other button/form that swaps into the SAME target (leaving those on
plain innerHTML would reintroduce the scroll/focus/`<details>` loss on every
non-poll action — send a message, merge, approve, reject, publish):

- `session_view.html` (`#session-body` poll) / `session_list.html`
  (`#session-list` poll) — no secondary actions target these; read-only panes.
- `editor.html` (`#editor` poll + the chat-send form) and `editor_panel.html`
  (the Merge / "confirm deletion & merge" buttons).
- `bootstrap_agent.html` (`#chatedit` poll + the chat-send form) and
  `chatedit_panel.html` (Approve / Reject buttons).
- `human_resolve.html` (`#resolve` poll + the message-send form) and
  `human_resolve_panel.html` (the Publish -> main button).

`hx-trigger` cadence is untouched everywhere (out of scope for this task; a
separate, still-in-flight task owns poll-interval backoff) — only the swap
mechanism changed.

### Why this is safe alongside the three things it must co-exist with

- **poll-scroll-preserve**: addressed above — narrowed to bottom-follow only.
- **SSE render path** (`session_view.html`): the stream writes
  `#session-transcript.textContent` directly via DOM APIs, never through an
  htmx swap, so it is untouched by the swap-style change. When the stream ends
  and the poll resumes (`htmx.trigger(body, 'load')`), the fetched
  `session_body.html` fragment's `<pre id="session-transcript">` morphs onto
  the SAME node the stream had been mutating (matched by id) instead of
  replacing it — strictly better than before (no flash), and the SSE code
  needed no change.
- **etag-304 no-swap guard** (`layout.html`): fires on `htmx:beforeSwap` and
  sets `shouldSwap = false` before any swap (morph or not) runs; unrelated to
  which swap STYLE would have applied, so it still short-circuits a 304
  exactly as before. `TestNativeSkip304ScriptEmbedded` (pre-existing) is
  unaffected.
- **poll-backoff-ended-content**: this task's original dependency. Verified at
  implementation time that this submodule's tracked tip carries none of that
  task's `.Live`/`.Busy`-conditional `hx-trigger` markup (a separate
  still-unmerged branch owns re-landing it after an earlier merge silently
  dropped the diff) — so there is nothing from it to reconcile against here.
  This task changed `hx-swap` only, never `hx-trigger`, keeping the two
  concerns' blast radii disjoint regardless of merge order.

## Idiomorph matching notes (honest, no ids added to chat/step markup)

`chatedit_panel.html`'s `.msg` rows and the shared `agent-step` `<details>`
template carry no unique `id` (only `class`). Idiomorph's matching does not
require one: for a purely-appending list (every poll re-renders the FULL log
from scratch; historical entries are byte-identical to their previous render),
its soft-match algorithm (same `nodeType` + `tagName`, positional scan) aligns
existing rows/steps 1:1 and only creates nodes for genuinely new tail content —
sufficient to keep an expanded `<details>` earlier in the log the SAME node
across a tick. Adding stable per-entry ids would sharpen matching further under
reordering, but reordering does not happen here (append-only), so it is left as
a documented follow-up rather than in-scope churn.

## Tests — `internal/web/web_test.go`

- `TestMorphSwapWiring`: `hx-ext="morph"` on the page; each of the five poll
  targets renders `hx-swap="morph:innerHTML"` and never a plain
  `hx-swap="innerHTML"`.
- `TestMorphAppliesToPanelActions`: the same for every button/form action that
  targets one of the five panes (chat-send x3, merge, confirm-deletion-merge,
  approve, reject, publish).
- `TestTemplatesGrepMorph`: the literal acceptance-criterion grep
  (`morph|idiomorph|hx-ext`) against the embedded template source, so it fails
  the same way a plain repo-tree `grep` would.
- `TestAssetsMorphExtServed`: `/assets/idiomorph-ext.min.js` is a real,
  reasonably-sized file (not a stub) served as JavaScript.
- `TestLayoutEmbedsMorphExtNoCDN`: the script tag is present, no CDN
  reference, and htmx loads strictly before the morph ext.
- `TestMorphPreservesDetailsOpenState`: the `beforeAttributeUpdated` veto for
  `open`/`remove` ships on every page.
- Pre-existing `TestScrollPreserveScriptEmbedded` / `TestScrollPreserveWiring`
  / `TestChatPanelWiring` / `TestPollPaneLoadingSkeletons` / etc. all still
  pass unmodified — the `data-scroll-preserve`/`data-scroll-pin` markup itself
  never changed, only what the script DOES with it.
- Scroll position / focus / `<details>` persistence during an actual morph is
  DOM/browser behavior, not unit-testable in Go (same limitation
  poll-scroll-preserve's own doc already notes) — the tests above lock the
  WIRING (swap style, extension load order, callback configuration); the
  behavioral guarantee follows from idiomorph's documented node-identity +
  `beforeAttributeUpdated` semantics, verified by reading its source
  (`morphAttributes`/`ignoreAttribute` in `idiomorph-ext.js`) before vendoring.

## Acceptance mapping

- *polled panes morph/patch the DOM in place instead of full innerHTML replace
  (grep morph|idiomorph|hx-ext no longer empty)* -> all five panes'
  `hx-swap="morph:innerHTML"` + `hx-ext="morph"`; `TestMorphSwapWiring`,
  `TestTemplatesGrepMorph`.
- *the morph lib is embedded (no CDN/new framework)* -> vendored
  `idiomorph-ext.min.js` under the existing `assets/*` embed;
  `TestAssetsMorphExtServed`, `TestLayoutEmbedsMorphExtNoCDN`.
- *scroll position, focus, and `<details>` open-state survive a poll tick;
  poll-scroll-preserve no longer double-handles scroll for them* -> morph's
  node-identity preservation (scroll/focus) + the `beforeAttributeUpdated`
  veto (`<details open>`); the shim narrowed to bottom-follow-only.
  `TestMorphPreservesDetailsOpenState`; DOM behavior itself not
  Go-unit-testable (see Tests section).
- *live-session SSE render + poll fallback still work; the etag-304 no-swap
  guard still short-circuits* -> both verified unaffected by construction
  (see "Why this is safe" above); pre-existing SSE and 304 tests pass
  unmodified.
- *single-binary embed preserved* -> `assets/*` glob embed, no CDN reference
  anywhere; `TestLayoutEmbedsMorphExtNoCDN`.
- *gofmt/go vet/go test ./internal/web green (CGO_ENABLED=0)* -> verified,
  plus `go build ./...` and `go test ./...` across the whole module.
