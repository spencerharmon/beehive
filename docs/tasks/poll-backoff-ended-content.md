# poll-backoff-ended-content: back off / stop polling once a pane's content is final

## Problem

`ui-audit-002` (Finding #15, ranked #2 — client performance / tuned poll
intervals) flagged that polled panes never back off or stop. Each htmx-polled
shell fired a fixed `hx-trigger="load, every <n>"` set once at page render, so a
pane kept re-fetching identical bytes forever while the tab stayed open — a full
server render + fragment transfer per tick of content that is not changing:

- `session_view.html` (`#session-body`, `every 2s`) — the worst case. The SSE
  stream that supersedes the poll is gated `{{if .Live}}`, so an **ended** session
  (`.Live` false) has no stream and polls a **durable, never-again-changing final
  transcript** every 2s forever. `sessions.go`'s `sessionTranscript` returns
  `live=false` for an ended session, and the ended state is terminal (a session
  never goes back to live).
- `session_list.html` (`#session-list`, `every 2s`) — polls unconditionally even
  when no session is live and the list only changes when a new pass starts.
- `human_resolve.html` (`#resolve`, `every 2s`) and `editor.html` (`#editor`,
  `every 1500ms`) — the AI agent chat panes poll at a fixed fast cadence even
  while the agent is idle (not producing) and the chat+diff are unchanged.

The signal each pane needs to decide "nothing more is coming (or it is slow)" is
already in template scope: `.Live` for `session_view`, per-session `Live` for the
list, `.Busy` for the two agent panes.

## Design (no new dep, no new JS)

### `session_view.html` — stop when ended

The ended transcript is **terminal**, so the shell's static trigger is exactly
right — gate it on `.Live`:

```
hx-trigger="load{{if .Live}}, every 2s{{end}}"
```

An ended session fetches **once** (`load`) and stops; a live session keeps
`load, every 2s` (unchanged — still the SSE fallback the stream cancels while
connected). Only the `hx-trigger` attribute changes; `hx-get`/`hx-swap`/the SSE
script/scroll-preserve/the rendered `session_body` are byte-identical.

### `session_list`, `editor`, `human_resolve` — adaptive self-refreshing poller

These three panes' idle state can revert (a new session starts; the human sends a
message), so a static shell trigger cannot adapt. Each shell is changed to a
**one-shot `load`** (fetch the fragment once) and the fragment carries its own
poller whose cadence is recomputed on every cycle and re-emitted:

- `session_list_body.html` gains `#session-list-poll` — `every 2s` when
  `.AnyLive`, else `every 10s`. `sessions.go`'s `sessionsListBody` computes
  `AnyLive` from the `sessionInfos` it already builds (no extra git call). Each
  poll re-renders the body (and the poller) into `#session-list`, so when the last
  session ends the next tick backs off to 10s, and when a new session goes live it
  speeds back to 2s — while still discovering new sessions within 10s.
- `editor_panel.html` gains `#editor-poll` — `every 1500ms` when `.Busy`, else
  `every 10s`. `human_resolve_panel.html` gains `#resolve-poll` — `every 2s` when
  `.Busy`, else `every 10s`.

The poller is **always present** (both busy and idle emit one, only the cadence
differs) for two reasons: it re-arms itself after a message/merge/publish **form**
swap (those forms already swap the panel fragment into the same `#editor` /
`#resolve` target, so the returned fragment reinstates the poller), and it is
immune to any lag in the `Busy` flag becoming observable right after a background
turn starts — a momentarily-idle render just polls at 10s and catches up within
one tick. Busy cadences are unchanged from today, so a live/working pane behaves
as before; only the idle case backs off.

Swap semantics are unchanged: every poller uses `hx-target="#<shell>"
hx-swap="innerHTML"`, the same target+swap the shell and the panes' existing forms
use, so there is no dual-swap conflict and no visible output change (the poller is
an empty `hidden` div).

## Tests — `internal/web/web_test.go`

`TestPollBackoffEndedContent`:

1. **session_view (the required ended-vs-live difference):** renders the template
   with `Live:false` → `hx-trigger="load"` and **no** `every 2s`; with `Live:true`
   → `hx-trigger="load, every 2s"`. Plus an end-to-end check: a served ended
   session page (`bee-final`, a non-stub) fetches once and stops, while a served
   live page (`bee-live`, a live stub) keeps the 2s poll — proving the handler
   wires `.Live`.
2. **session_list:** shell no longer polls directly; the body poller is `every 2s`
   with a live session and `every 10s` (into `#session-list`) with none.
3. **editor:** shell no longer polls directly; the panel poller is `every 1500ms`
   when busy, `every 10s` when idle.
4. **resolve:** shell no longer polls directly; the panel poller is `every 2s`
   when busy, `every 10s` when idle.

## Acceptance mapping

- *an ended session's pane fetches once and stops (no every-2s)* → `session_view`
  gates the poll on `.Live`; asserted directly and end-to-end.
- *a live session still polls/streams as today* → live keeps `load, every 2s` and
  the `{{if .Live}}` SSE script; unchanged and asserted.
- *at least one list/resolve/editor pane stops/slows when idle* → **all three** back
  off to 10s once idle (list: no live session; editor/resolve: agent not busy);
  each asserted. No pane is left at a fixed fast cadence.
- *no change to hx-swap/SSE/scroll-preserve/rendered output* → only `hx-trigger`
  changes plus an added empty `hidden` poller div; swap targets, the SSE script,
  `data-scroll-preserve`/`-pin`, and the visible fragment markup are unchanged.
- *a test asserts the ended-vs-live hx-trigger difference for session_view.html* →
  `TestPollBackoffEndedContent` (1).
- *gofmt / go vet / go test ./internal/web green (CGO_ENABLED=0)* → verified.

Disjoint from `poll-pane-loading-skeletons` (which scoped cadence OUT and only
touched loading skeletons + a11y attrs) and from any ETag work.
