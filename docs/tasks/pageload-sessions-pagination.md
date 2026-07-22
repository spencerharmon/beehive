# pageload-sessions-pagination

## Problem
The sessions view (`/submodule/{name}/sessions`) rendered EVERY recorded session
per paint. `sessionInfos` walked `sessions/`, and for each entry read the
transcript (stub detection + `sessionTags` kind/model derivation), resolved the
live stream branch (a git call for stubs), and resolved delivery links. As a
hive accumulates sessions without limit, that per-paint cost grows with the total
session count — an unbounded list, polled every 2s.

## Change
Windowed pagination for the sessions list (`internal/web/sessions.go`,
`templates/session_list*.html`):

- `sessionInfosPage(ctx, sm, now, ttl, page, size)` renders ONE page of at most
  `size` sessions. It does a cheap pre-pass — `ReadDir` + each entry's file mtime
  (no file read) — sorts newest-first, then does the expensive per-session work
  (transcript read for stub/tags, live-branch git resolution, delivery-link
  resolution) ONLY for the selected window. Cost is bounded by `size`, not by the
  total session count. The window is re-sorted by its refined `Modified` time (a
  stub's live-branch tip) so ordering within the page stays exact.
- `sessionInfos` is kept as a thin full-window wrapper for callers/tests that
  want every session.
- Page size defaults to `sessionsPageSize` (50), overridable at runtime via
  `BEEHIVE_SESSIONS_PAGE_SIZE` (a positive integer) with no code change.
- The handlers thread `?page=` through: `sessionsList` (shell) sets the body's
  HTMX poll URL to `/sessions/body?page=N` so the 2s auto-refresh stays on the
  navigated page; `sessionsListBody` renders that page plus prev/next navigation.
- `session_list_body.html` gains a `<nav class="pagination">` with newer/older
  links and a "page X of Y · N sessions" indicator, shown only when >1 page.

Live/tags/link semantics are identical to the previous path — only the set of
sessions materialized per paint is bounded.

Other lists: the only unbounded RENDERED list on a page is the sessions list.
`/stats` scans all transcripts but is an AGGREGATION (owned by the separate
pageload stats work), not a rendered list, so it is out of scope here.

## Tests
`internal/web/sessions_pagination_test.go` (`TestSessionsListPaginationBounds`)
seeds the session-heavy `seedPerfServer` fixture (400 sessions), sets
`BEEHIVE_SESSIONS_PAGE_SIZE=25`, and asserts:
- page 1 renders EXACTLY 25 rows (bounded), not all 400 (the regression it
  guards: it would render 400 without pagination);
- pagination navigation is present with a next-page link;
- every page is bounded to the page size and the union of all pages covers
  exactly the 400 distinct sessions with no duplicates (navigation reaches all).

Command + result (run in the code worktree):

    $ CGO_ENABLED=0 go test ./internal/web/ -run 'TestSessionsListPaginationBounds|TestPageLoadBudgetsSynthetic' -v
    --- PASS: TestPageLoadBudgetsSynthetic (0.18s)
    --- PASS: TestSessionsListPaginationBounds (0.16s)
    PASS
    ok  github.com/spencerharmon/beehive/internal/web

Full suite green under CGO_ENABLED=0:

    $ CGO_ENABLED=0 go test ./...
    ok  github.com/spencerharmon/beehive/internal/web  79.614s
    (all packages ok)
