# poll-fragment-etag-304: conditional GET (ETag / 304) for polled htmx fragments

## Problem

A UI performance audit (ui-audit-003, Finding #13, ranked #1 — broadest client+server
perf waste of that pass) found that every polled htmx fragment re-renders and
re-transfers its FULL HTML body on every tick (1.5-2s), even when the bytes are
byte-identical to the previous poll:

- `GET /submodule/{name}/sessions/body` (`sessionsListBody`) — every 2s
- `GET /submodule/{name}/session/{branch}/body` (`sessionBody`) — every 2s
- the editor/chat panels (`editorPanel`, `humanResolvePanel`) — every 1.5-2s
- `GET /human/{sub}/{id}/panel/{sid}` — every 2s

`internal/web/web.go`'s lone render path (`Server.render`, formerly the only one) sets
`Content-Type` and executes the template straight to the `ResponseWriter` — no
conditional-response short-circuit. `cache.go`'s `viewCache` already memoizes the
expensive parse/read work keyed on the repo HEAD (`headSHA`, `web.go`), but that
signal is the WRONG validator for this feature: `cache.go`'s own doc (lines 26-31)
documents that some rendered content is time-dependent — a claim's active/stale
badge flips purely on the wall clock crossing the TTL, with no new commit — so a
HEAD-only ETag would wrongly keep 304-ing a pane whose visible text actually changed.

## Design

### Conditional-response helpers — `internal/web/web.go`

- `writeConditional(w, r, body []byte, contentType string)` computes a strong ETag
  over `body` (`fragmentETag`, below), sets `ETag` and `Cache-Control: no-cache` on
  the response, and — when the request's `If-None-Match` already names that tag
  (`etagMatch`) — replies `304 Not Modified` with no body and no `Content-Type`
  (RFC 7230 §3.3.2: a 304 must not carry a representation or the headers describing
  one). Otherwise it sets `Content-Type` and writes the full body as a normal 200.
- `etagMatch(header, etag string) bool` implements the weak comparison RFC 7232
  §2.3.2 mandates for `If-None-Match`: `*` matches anything, a comma-separated list
  is split and each entry's optional `W/` prefix is stripped before comparing the
  opaque quoted value.
- `Server.renderConditional(w, r, name, data)` is `Server.render`'s poll-friendly
  sibling: executes the named template into a `bytes.Buffer` (instead of straight to
  `w`) and hands the result to `writeConditional`. A template execution error still
  surfaces as a 500 exactly like `render`'s.
- `Cache-Control: no-cache` (not absent, not `no-store`) is deliberate: it lets the
  BROWSER'S OWN HTTP cache store the fragment but mandates it always revalidate with
  the server first. That is what makes the payoff automatic for a plain htmx poll —
  no client-side code has to manage the validator — because the browser attaches
  `If-None-Match` for us on the next request and, on a 304, hands the XHR layer the
  previously-cached 200 body. htmx (which polls via `XMLHttpRequest`) never even
  observes the 304 in that common case.

### Validator: hash the rendered bytes, not the repo HEAD — `internal/web/cache.go`

`fragmentETag(body []byte) string` returns a strong, quoted ETag
(`"<sha256-hex>"`) over the EXACT rendered bytes about to be sent. Hashing the final
output — rather than a coarser signal like `headSHA` — is what keeps the validator
correct for a pane whose content can change with no new commit: any observable
difference, commit-driven or purely time-driven (a claim's active/stale flip), changes
the digest, so a 304 only ever fires when the fragment the client already holds is
truly byte-identical to what would be re-rendered right now. The cost is an extra
render + hash on every request (no cache-hit shortcut before rendering) — acceptable
here because these fragments are already cheap, file-derived reads, and correctness
(never silently serving stale content across a real change) matters more than shaving
the render itself.

### Wiring — `internal/web/sessions.go`

`sessionsListBody` and `sessionBody` now call `s.renderConditional` in place of
`s.render`, unchanged otherwise. These two were chosen as the wired proof because
they are the simplest polled fragments with no other in-flight change touching them
(the editor/chat and human-resolve panels are deliberately left on `render` for this
task — same helper is available to wire them later with no further design work).

### htmx 304 behavior — verified, and guarded — `internal/web/templates/layout.html`

Verified against the vendored htmx 1.9.10 (`internal/web/assets/htmx.min.js`): it
swaps any response whose status is in `[200,400)` except `204` — there is no native
"304 means no-op" rule. A raw 304 that reached htmx's swap layer would carry an empty
body (per HTTP, a 304 MUST NOT have one) and htmx would swap that in, blanking the
pane. In the common case this never happens: the browser's own HTTP cache absorbs the
304 transparently before htmx's XHR ever sees it (see the `Cache-Control` note above).
But that absorption is a cache/proxy implementation detail outside this app's control
(devtools "disable cache", a cache-stripping intermediary, …), so a global
`htmx:beforeSwap` listener is added to the layout footer as a belt-and-suspenders
guard: if `event.detail.xhr.status === 304`, it sets `event.detail.shouldSwap = false`
and the pane keeps its last-good content. Polling itself is unaffected (driven by
htmx's own trigger timer, not by whether a swap happened), and the guard is global so
it also covers any future fragment wired onto `renderConditional`.

## Tests — `internal/web/web_test.go`

- `TestFragmentETag304`: end-to-end through the real handlers for both wired
  fragments (`/submodule/alpha/session/bee-final/body`,
  `/submodule/alpha/sessions/body`). Asserts (1) the first GET is a 200 carrying a
  non-empty `ETag` and a `Cache-Control` header with a non-empty body; (2) a repeat
  GET presenting that exact `ETag` as `If-None-Match` gets `304` with an EMPTY body
  and the SAME `ETag` header; (3) a GET with a non-matching `If-None-Match` still
  gets the full 200 body (no false-positive 304). The session file's mtime is pinned
  90 minutes in the past so `sessionsListBody`'s per-item "Ago" text cannot tick over
  between the test's two quick calls — otherwise the fragment's own rendered bytes
  (correctly, by design) would legitimately differ.
- `TestNativeSkip304ScriptEmbedded`: locks that the `htmx:beforeSwap` /
  `shouldSwap` / `304` guard script ships on a real full-page response, the same
  embedding contract `TestScrollPreserveScriptEmbedded` already locks for the
  scroll-restore script.

## Acceptance mapping

- *at least one polled fragment endpoint sets an ETag on its 200 and returns 304
  (empty body) on a matching If-None-Match* → `sessionBody` and `sessionsListBody`
  via `renderConditional`; `TestFragmentETag304`.
- *the 304 path does not blank the pane in htmx (verified, guard added only if
  needed, documented)* → verified against the vendored htmx source (no native 304
  handling); guard added in `layout.html`; documented above and in the script's own
  comment; locked by `TestNativeSkip304ScriptEmbedded`.
- *the validator is over rendered bytes (or HEAD + time bucket) so a claim
  stale-flip still re-renders* → `fragmentETag` hashes the rendered output itself,
  not `headSHA`.
- *a test asserts both the 200+ETag and the 304-on-repeat* → `TestFragmentETag304`.
- *single-binary embed preserved* → no new external asset or CDN reference; the new
  script is inline in the embedded `layout.html`.
- *gofmt/go vet/go test ./internal/web green (CGO_ENABLED=0)* → verified.
