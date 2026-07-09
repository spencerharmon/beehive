# links-editor-route-gap: wire the submodule-links form to a real GET route

## Problem

`internal/web/templates/links_editor.html` (the "link two submodules" form) is a
complete, backend-complete feature that was **100% undiscoverable through the
shipped web UI**. Its POST half is fully wired — `(*Server).submoduleLink`
(`internal/web/web.go`) validates through the cycle-checked `submod.AddDep`,
publishes to main, and redirects to `/` — and its markup passed three prior
accessibility audits (ui-audit-005/006/007, folded into the DONE
`form-input-accessible-labels`: both inputs carry sr-only `<label>`s). But:

- **No GET route rendered it.** Every other action page (`/roi/{name}`,
  `/secrets`, `/merge`, `/submodule/{name}/env`) registers both a GET (render)
  and a POST (submit) handler; `/submodule/link` was the lone exception —
  `POST /submodule/link` existed with no `GET` half, so the page could never be
  reached in a browser.
- **Nothing linked to it.** The 7-item top-nav (`layout.html`'s `.nav-links`:
  dashboard/stats/secrets/merge/human/hygiene/skills — the set of global,
  non-submodule-scoped utility pages) had no "links" entry, even though
  `/submodule/link` is a hive-wide action exactly like `/secrets` and `/merge`
  (no `{name}` path segment). `dashboard.html` (which inlines the sibling
  add-submodule form) never mentions "link".

The gap survived three audit cycles because each prior pass verified the
template's own markup **in isolation**, via a direct `renderTmpl` call in a test
(`TestFormInputsHaveAccessibleNames`'s `links_editor.html link form` case), never
checking the page was wired to a route — exactly how a fully dead page goes
unnoticed. This is a pure web-UI discoverability gap for an operator-convenience
feature (the `beehive submodule link` CLI path that reconcile/bootstrap actually
use is unaffected), not a data-integrity or swarm-coordination defect.

## Design

Add the missing GET half and a nav entry, changing nothing about the POST path.

- **`internal/web/web.go`** — register `GET /submodule/link` next to the existing
  `POST /submodule/link`, bound to a new `(*Server).submoduleLinksGet`:

  ```go
  func (s *Server) submoduleLinksGet(w http.ResponseWriter, r *http.Request) {
      s.render(w, "links_editor.html", map[string]interface{}{"Title": pageTitle("links"), "Nav": "links"})
  }
  ```

  It seeds no dynamic state — the form has none — beyond the shared chrome: a
  `pageTitle("links")` browser title and `Nav: "links"` so the new nav entry
  lights up. This mirrors the other stateless global pages (`secretsGet`,
  `mergeGet`). Go 1.22's method-based `ServeMux` patterns let `GET` and `POST`
  on the identical path coexist without shadowing. A read-view of
  `SUBMODULE-LINKS.yaml`'s current edges is **deliberately out of scope** here
  (noted for a future pass) — this task only wires the existing render-only form.

- **`internal/web/templates/layout.html`** — append an 8th `.nav-links` entry
  after `skills`, using the existing `aria-current` convention verbatim:

  ```html
  <a href="/submodule/link"{{if eq .Nav "links"}} aria-current="page"{{end}}>links</a>
  ```

  The existing 7 entries are left byte-for-byte unchanged; only the trailing
  entry is added (plus the non-rendered section-list comment updated from "7" to
  "8" to stay accurate).

`(*Server).submoduleLink` (the POST handler) and its `submod.AddDep` /
publish / redirect-to-`/` behavior are untouched.

## Tests — `internal/web/web_test.go`

New `TestSubmoduleLinksGetRouted` closes the isolation gap with a **real HTTP
round-trip** (`get(t, s, "/submodule/link")`, not a direct `renderTmpl` call):

- asserts `GET /submodule/link` returns 200 and actually renders the form
  (`<h1>Submodule links</h1>` and `action="/submodule/link"` both present);
- extracts the top-nav `<nav class="nav">` region and asserts exactly the new
  `<a href="/submodule/link" aria-current="page">links</a>` entry is current,
  with an `aria-current` count of exactly 1 (only the links item) — the same
  convention the other 7 sections use;
- confirms the sibling `POST /submodule/link` still resolves on the identical
  path and 303-redirects, proving the new GET registration did not shadow it.

The pre-existing `TestNavAriaCurrentOptional`, `TestSubmoduleLink`, and
`TestSubmoduleLinkRejectsCycle` continue to pass unchanged, proving the 7 old nav
entries and the POST behavior are unaffected.

## Acceptance mapping

- *GET /submodule/link renders links_editor.html via a real HTTP round-trip test
  (not only a direct renderTmpl call)* → `TestSubmoduleLinksGetRouted` uses
  `get()` (a full `Routes().ServeHTTP` round-trip) and asserts the rendered form.
- *top-nav carries an 8th links entry with the same aria-current convention as
  the existing 7, which stay byte-for-byte unchanged* → one appended `<a>`, same
  `{{if eq .Nav "links"}}` idiom; the other 7 lines are untouched (diff-verified),
  `TestNavAriaCurrentOptional` still green.
- *POST /submodule/link's existing validation/AddDep/publish/redirect behavior is
  completely unchanged* → `submoduleLink` not modified; the two existing POST
  tests plus the new test's POST assertion confirm it.
- *a test confirms visiting /submodule/link marks only the links nav item
  aria-current* → the top-nav-scoped assertion + `aria-current` count == 1.
- *gofmt / go vet / go test ./internal/web green (CGO_ENABLED=0)* → verified.
- *single-binary embed preserved; no new asset/CSS/template* → reuses the
  already-embedded `links_editor.html` and `layout.html`; no files added under
  `assets/` or `templates/`.
