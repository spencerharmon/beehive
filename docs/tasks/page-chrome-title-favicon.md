# page-chrome-title-favicon: per-page `<title>` + embedded favicon

## Problem

Enqueued by `ui-audit-002` (Findings #9 + #10 bundled, ranked #3 — consistent
navigation: page titles + favicon). `layout.html`'s `{{define "header"}}`
hardcoded `<title>beehive</title>` for every route, and every one of the 20
full-page templates invoked it as bare `{{template "header"}}` — no data
pipeline at all — so no per-page title could ever render regardless of what a
handler knew. Separately, `internal/web/assets/` held only `htmx.min.js` +
`style.css`: no favicon asset existed, so every page load triggered a failed
default `/favicon.ico` request.

## Dependency note (`breadcrumb-nav-trail`)

The task depended on `breadcrumb-nav-trail` on the premise that it threads a
per-handler page-context mechanism into the header/layout to derive the title
from, rather than adding a parallel one. `PLAN.md` shows that task `DONE`
("already merged into tracked main `131dc335…`"), but the tracked submodule tip
(the commit that pointer SHA actually names, and this task's own worktree
base) carries **no** breadcrumb code at all — no `breadcrumb.go`, no `Crumbs`
field, no `{{define "breadcrumb"}}` anywhere in `layout.html`. The real
implementation exists only on the orphaned, never-merged `bee-breadcrumb-nav-trail`
branch (commit `497f0b1`, parent `19a6b48` — an ancestor of, but not equal to,
the current tip); the `DONE` stamp appears to have been a review/reconcile
error that cited main's tip SHA without verifying that SHA's tree actually
contained the change. That is a defect in a *different* task's history, not
this one's to fix (this pass never touches `PLAN.md` beyond its own status
line, and never touches `ROI.md`).

Consequence for this task: there is no existing "threaded page context" to
build on. Rather than block on it or invent a placeholder for a mechanism that
does not exist, this fix threads its OWN minimal, additive `Title`/`Nav` keys
into each handler's existing render-data map (every full-page handler already
passes a `map[string]interface{}`). This is orthogonal to, and does not
collide with, `breadcrumb-nav-trail`'s real (unmerged) shape: that change adds
a SEPARATE `{{template "breadcrumb" .Crumbs}}` line and a `Crumbs` key, never
touching the header pipe or a `Title`/`Nav` key. The only overlap when that
task eventually lands is the literal `{{template "header"}}` line itself
(this fix changes it to `{{template "header" .}}`), which is a trivial,
easily-resolved textual merge, not a design collision.

## Design

### `<title>` (`internal/web/web.go`, `layout.html`, every page-handler file)

- `layout.html`'s header now renders
  `<title>{{if .Title}}{{.Title}}{{else}}beehive{{end}}</title>` — a handler
  that omits `Title` (or sets `""`) gets the bare site name. Every one of the
  20 `{{template "header"}}` call sites across the page templates is changed
  to `{{template "header" .}}` so the page's own render data (whatever
  map/struct the handler passed) actually reaches the header define.
- `pageTitle(parts ...string) string` (`web.go`) is the ONE place a title
  string is assembled: non-empty parts join innermost-first with `" · "` and
  the site name always trails (e.g. `pageTitle("plan", "alpha")` →
  `"plan · alpha · beehive"`; `pageTitle()` → `"beehive"`). Empty parts are
  silently dropped so a caller need not special-case a blank value.
- Every full-page handler sets `"Title"` from context it already has:
  `explorer`/`branches`/`doc`/`docExplorer`/`plan`/`roiGet`/`roiPost`/`envGet`
  (`internal/web/web.go`), `sessionsList`/`sessionView` (`sessions.go`),
  `commitView` (`delivery.go`), `humanResolvePage` (`humanresolve.go`),
  `skillsPage` (`skills.go`), `editorPage` (`editor.go`), `stats` (`stats.go`),
  plus the top-level `secretsGet`/`renderMerge`/`human`/`hygiene`. The
  dashboard (`dashboard()`) is the ONE handler that renders no `Title` at all
  — it is the root page and demonstrates the fallback.

### Favicon (`internal/web/assets/favicon.svg` new, `web.go`, `layout.html`)

- A small embedded SVG (`viewBox 0 0 32 32`, a single honeycomb-cell hexagon,
  amber fill `#f4a825` / dark stroke `#1d2734` — hex equivalents of the
  existing `--hue-review`-family amber and `--text` design tokens, so it reads
  as on-brand without needing a browser to resolve CSS custom properties from
  a standalone asset file). No new embed directive: it lands under the
  existing `//go:embed assets/*` (`assetFS`), so it is served at
  `/assets/favicon.svg` for free through the pre-existing
  `http.FileServer(http.FS(assetFS))` route, identically to `style.css`/
  `htmx.min.js` today.
- `layout.html`'s `<head>` gains
  `<link rel="icon" href="/assets/favicon.svg" type="image/svg+xml">`.
- A browser that does NOT honor `<link rel="icon">` (or any tooling that
  probes the conventional path regardless) still hits `/favicon.ico`
  directly, so `web.go` additionally registers `GET /favicon.ico` ->
  `faviconICO`, which re-serves the SAME embedded bytes with
  `Content-Type: image/svg+xml` (a browser keys off the header, not the URL
  extension) — one embedded source, reachable from both paths, never a failed
  request.

### Bonus (optional, Finding #11): `aria-current` on the active top-nav link

The top nav's 7 links (`dashboard`/`stats`/`secrets`/`merge`/`human`/
`hygiene`/`skills`) mark the current section with `aria-current="page"` when
the handler set `"Nav"` to the matching name — only the 7 handlers that route
straight off one of those links set it; every submodule-scoped page (plan,
session, explorer, ...) leaves `Nav` unset, so none of the 7 links light up.
This is a distinct `<nav>` landmark from `breadcrumb-nav-trail`'s own
`aria-current` on its trail's terminal crumb (when that lands), so the two
never collide on the same element. No new CSS: this is the semantic attribute
only, no visual restyle (out of scope for this fix).

## Tests — `internal/web/web_test.go`

- `TestPageTitleHelper` — unit-tests `pageTitle` in isolation (empty-part
  dropping, join order, the no-args fallback).
- `TestPageTitlesDistinctPerRoute` — drives the dashboard, a plan page, a
  session page, and `/stats` through the real handlers and asserts all four
  `<title>` values are both correct AND pairwise distinct, and that the
  dashboard specifically renders the bare `"beehive"` fallback.
- `TestFaviconEmbeddedAndServed` — `/assets/favicon.svg` and `/favicon.ico`
  both 200 with an `image/svg+xml` content-type and IDENTICAL bytes (one
  embedded source); the dashboard's `<head>` links the asset.
- `TestNavAriaCurrentOptional` — the dashboard and `/stats` each mark their OWN
  top-nav link current and no other; a submodule-scoped page (plan) marks
  none.
- Every pre-existing `internal/web` test still passes unchanged: no test
  asserted a literal `{{template "header"}}` (no dot) or an exact `<title>`/
  nav-link string, so none needed editing.

## Acceptance mapping

- *dashboard, a plan page, a session page, and `/stats` render DISTINCT
  meaningful `<title>` values (unset falls back to beehive)* ->
  `TestPageTitlesDistinctPerRoute`.
- *a favicon asset is embedded + served (no failed `/favicon.ico`)* ->
  `favicon.svg` under the existing `assets/*` embed; `/favicon.ico` route;
  `TestFaviconEmbeddedAndServed`.
- *tests assert two routes yield two `<title>`s and the favicon is
  reachable* -> `TestPageTitlesDistinctPerRoute` (four routes) +
  `TestFaviconEmbeddedAndServed`.
- *single-binary embed preserved* -> no new embed directive, no CDN
  reference added; `TestLayoutEmbedsHtmxNoCDN` still green.
- *gofmt / go vet / go test ./internal/web green (`CGO_ENABLED=0`)* ->
  verified, plus a full-repo `go build ./...` / `go test ./...`.
