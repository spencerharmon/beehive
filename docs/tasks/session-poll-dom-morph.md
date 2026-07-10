# session-poll-dom-morph: morph-patch polled panes instead of full innerHTML swap

## Problem

Every htmx-polled pane swapped its whole subtree with `hx-swap="innerHTML"` each
tick, rebuilding identical DOM (and its inner scroll container) even when only a
few bytes changed. That destroyed the pane's scroll position, focus, and any
`<details>` open-state on every refresh, and a client-side JS shim
(`poll-scroll-preserve`, `layout.html`) existed only to paper over the scroll
loss the rebuild caused. `grep -riE "morph|idiomorph|hx-ext" internal/web/templates`
was empty — the UI ran vanilla htmx 1.9.10 with no in-place patching.

Panes affected (all polled): `#session-body` (every 2s while live), `#session-list`
(every 2s), `#editor` (1.5s while busy), `#chatedit` (1.5s while busy), and the
human-resolve `#resolve` panel (2s while busy).

## Design

Vendor + embed the idiomorph htmx `morph` extension and switch each polled pane to
a morph swap keyed on its stable id, so a refresh PATCHES the existing DOM in place
instead of rebuilding it. No CDN, no SPA, no framework — single-binary embed, same
as htmx itself.

### 1. Vendored asset — `internal/web/assets/idiomorph-ext.min.js`

idiomorph 0.3.0's `dist/idiomorph-ext.min.js` (the library bundled with its htmx
extension), prefixed with a version banner. It registers
`htmx.defineExtension("morph")`, so a pane opts in with `hx-ext="morph"` and swaps
with `hx-swap="morph:innerHTML"`. Served automatically by the existing
`//go:embed assets/*` FileServer at `/assets/idiomorph-ext.min.js`.

### 2. Layout — `internal/web/templates/layout.html`

- `<script src="/assets/idiomorph-ext.min.js">` loads immediately AFTER
  `htmx.min.js` (the extension calls `htmx.defineExtension`, so htmx must exist
  first) and before any pane wires `hx-ext="morph"`.
- `poll-scroll-preserve` is **scoped** for the morph swap. Because a morph keeps the
  inner scroll container as the SAME node, its `scrollTop`, focus, and `<details>`
  open-state survive natively — so the shim no longer stashes/restores per-pane
  `scrollTop` (which would fight the in-place patch). Two jobs a node-preserving
  morph does NOT do on its own remain: (a) `[data-scroll-pin]` bottom-follow (a
  persisted node holds its old `scrollTop` and would otherwise stop following new
  output), and (b) the window-scroll restore so a swap never jumps the viewport.

### 3. Polled panes

`session_view.html` (`#session-body`), `session_list.html` (`#session-list`),
`editor.html` (`#editor`), `bootstrap_agent.html` (`#chatedit`), and
`human_resolve.html` (`#resolve`) each gain `hx-ext="morph"` and change
`hx-swap="innerHTML"` → `hx-swap="morph:innerHTML"`. The self-perpetuating in-flight
poll nodes (`editor_panel.html`, `chatedit_panel.html`, `human_resolve_panel.html`)
also morph so a live turn's repeated re-fetch patches the panel in place.

### Co-existence (unchanged, verified)

- `session_view.html` SSE render path still mutates `#session-transcript` directly
  and cancels the poll while `__sseLive`; when the poll runs as fallback it now
  morphs the persisted transcript node in place.
- The `etag-304` `shouldSwap=false` guard still short-circuits a 304 before any
  swap (morph or otherwise) runs.
- `data-scroll-pin` bottom-follow is preserved by the scoped shim.
- Single-binary embed preserved: no CDN, no new framework.

## Tests — `internal/web/web_test.go`

- `TestAssetsIdiomorphServed`: the extension is embedded and served at
  `/assets/idiomorph-ext.min.js`, is the real library (`Idiomorph`,
  `defineExtension("morph"`, the pinned `idiomorph 0.3.0` banner), not a stub.
- `TestLayoutEmbedsIdiomorphNoCDN`: layout references the embedded asset, loads
  htmx BEFORE it, and reaches no CDN.
- `TestPollPanesMorphSwap`: each polled pane's own opening tag carries
  `hx-ext="morph"` + `hx-swap="morph:innerHTML"` and no longer a full
  `hx-swap="innerHTML"`.
- `TestBusyPanelPollMorphs`: the busy self-perpetuating poll morphs too.
- `TestScrollPreserveScopedForMorph`: the shim keeps the pin bottom-follow and
  window-scroll restore but no longer stashes per-pane `scrollTop`.
- Existing `TestPollBackoffWhenEndedOrIdle` updated: the session-view poll swap is
  now `morph:innerHTML` (cadence contract otherwise unchanged).

## Acceptance mapping

- *polled panes morph/patch in place (grep morph|idiomorph|hx-ext no longer empty)*
  → the five panes + the layout; `TestPollPanesMorphSwap`.
- *the morph lib is embedded (no CDN/new framework)* → vendored asset +
  `//go:embed`; `TestAssetsIdiomorphServed`, `TestLayoutEmbedsIdiomorphNoCDN`.
- *scroll/focus/<details> survive a tick; poll-scroll-preserve no longer
  double-handles scroll* → morph preserves the nodes; the scoped shim;
  `TestScrollPreserveScopedForMorph`.
- *SSE render + poll fallback still work and the etag-304 no-swap guard still
  short-circuits* → both paths untouched (see Co-existence); existing tests pass.
- *single-binary embed preserved; gofmt/go vet/go test ./internal/web green
  (CGO_ENABLED=0)* → all green.
