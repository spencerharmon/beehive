# breadcrumb-nav-trail: a real ancestor breadcrumb on every non-dashboard page

## Problem

Enqueued by `ui-audit-001` (Finding #2 — consistent navigation). No web view
rendered a breadcrumb trail; each page carried at most one ad hoc "back" link, and
`doc_view.html`'s was actively WRONG regardless of entry point:

```
<a href="/submodule/{{.Name}}/branches">&larr; {{.Name}} commits</a>
```

That link is hardcoded to the branches/commits view, so a doc reached from the
plan's change-doc column, the submodule doc explorer, or a `/stats` delivery row
sent the reader to a page they never came from — silently dropping their real
navigation context.

## Design

A single reusable trail, driven by each handler's already-available
`Name`/`Branch`/`File`/`sha` context. No new data source, no swarm-state change —
pure `internal/web` presentation.

### 1. Model + builders — `internal/web/breadcrumb.go` (new)

`Crumb{Label, Href}` is one trail node. Invariant: an ANCESTOR crumb carries an
`Href`; the terminal current-page crumb leaves `Href` empty, so the template keys
"empty Href == current page" off the data with no separate flag. Builders return
the `[]Crumb` for each page, all rooted at `crumbDashboard()` (`/`):

- `explorerCrumbs(name)` → dashboard > **name**
- `planCrumbs` / `sessionsCrumbs` / `docsCrumbs` / `branchesCrumbs(name)` →
  dashboard > name > **plan|sessions|docs|commits**
- `sessionCrumbs(name,branch)` / `commitCrumbs(name,sha)` → the deep views,
  dashboard > name > sessions|commits (a live link back) > **branch|sha**
- `docCrumbs(name, from, file)` → the multi-entry doc viewer (below).

`branchesCrumbs`/`commitCrumbs` label the branch graph **commits** to match that
view's own "<name> commits" title (and the dashboard-card-polish relabel).

### 2. The doc viewer fix — `docCrumbs` + threaded `from`

`docCrumbs` switches on a `from` query token that every caller now threads, so the
intermediate crumb names the page that ACTUALLY linked here:

| `from`     | trail                                    |
|------------|------------------------------------------|
| `plan`     | dashboard > name > plan > file           |
| `docs`     | dashboard > name > docs > file           |
| `branches` | dashboard > name > commits > file        |
| `stats`    | dashboard > stats > file (GLOBAL entry — submodule crumb dropped) |
| else/empty | dashboard > name > file (sane default)   |

Callers thread `?from=`: `plan_items.html` (`plan`), `branch_view.html`
(`branches`), `doc_explorer.html` (`docs`), `stats.html` (`stats`). The `doc`
handler reads `r.URL.Query().Get("from")`; an unknown/absent token falls through to
the submodule-page default rather than the old wrong "commits" target.

### 3. Partial + handlers — `layout.html`, `web.go`, `sessions.go`, `delivery.go`

`{{define "breadcrumb"}}` renders `<nav class="breadcrumb" aria-label="breadcrumb">`
with an `<ol>`; each ancestor is an `<a>`, the current crumb a
`<span aria-current="page">`. `{{if .}}` guards it, so a page that passes no
`Crumbs` (the dashboard) renders nothing. Every submodule-hierarchy handler adds a
`"Crumbs"` entry to its template data; each template calls
`{{template "breadcrumb" .Crumbs}}` right after the header, replacing its old ad hoc
back-link (`doc_view`, `commit_view`, `doc_explorer`, and `session_view`'s "all
sessions" link — the running/ended badge stays).

### 4. Styles — `internal/web/assets/style.css`

`.breadcrumb` is a wrapping flex `<ol>` in `--text-sm`/`--text-muted`. `/`
separators are drawn with `li + li::before` so they stay OUT of the a11y tree and
are never selected/copied. `aria-current` reads in the base `--text` and ellipsizes
(`overflow/text-overflow/white-space`) so a long branch/sha/file name never
overflows the row. Tokens only — no hard-coded colours (submodule-overlay rule).

## Tests — `internal/web/web_test.go`

A `breadcrumbHTML(t, body)` helper extracts just the breadcrumb `<nav>…</nav>`
fragment so crumb assertions never accidentally match the always-present top-nav bar
(which itself links `/` and `/stats`).

- `TestDocViewBreadcrumbMultiEntry` — the core fix. A table over
  `from=plan|docs|branches|stats|""|bogus` asserts, per case: the correct entry
  crumb, exactly one `aria-current` terminal (`bee-doc.md`), the trail rooted at the
  dashboard, the submodule crumb present except for the global `stats` entry, NO
  unrelated entry crumb leaking in, and — regression guard — that the old hardcoded
  `&larr;` back-link is gone.
- `TestSessionViewBreadcrumbDeep` — the deepest trail: dashboard > name > sessions
  (all live links) > branch (`aria-current`), exactly one current crumb.
- `TestBreadcrumbScoping` — the dashboard (root) renders NO breadcrumb; a page one
  level down (submodule explorer) renders the landmark rooted at the dashboard with
  itself the `aria-current` leaf.
- Existing doc-link href assertions updated for the `?from=` suffix (plan, branches,
  doc-explorer, stats).

## Acceptance mapping

- *every non-dashboard page renders a trail rooted at the dashboard through its real
  ancestors* → per-page builders + the `breadcrumb` partial on each template;
  `TestBreadcrumbScoping` (root renders none, child rooted at dashboard),
  `TestSessionViewBreadcrumbDeep`.
- *doc_view's crumb reflects whichever page actually linked to it* → `docCrumbs` +
  the threaded `from` token; `TestDocViewBreadcrumbMultiEntry`.
- *`<nav aria-label="breadcrumb">` landmark with `aria-current`* → the partial;
  asserted in every breadcrumb test.
- *styled with design-system tokens* → `.breadcrumb` rules, tokens only.
- *tests cover the doc multi-entry case and a deep crumb* →
  `TestDocViewBreadcrumbMultiEntry` + `TestSessionViewBreadcrumbDeep`.
