# chat-editor-working-indicator-clear: auto-clear the stuck chat-diff working spinner

## Problem

Frontend-aesthetics reconcile flagged a bug on the chat-diff editor: the
"working"/spinner bubble sticks after the agent's reply is ready. The finished
output only appeared after a manual page refresh — the live status never
transitioned from working to the rendered reply on its own.

Root cause is client-side, and it is a regression from `session-poll-dom-morph`.
The chat panel keeps refreshing itself while a turn is in flight through a hidden
"self-perpetuating poll" node in `chatedit_panel.html`:

```
{{if .Busy}}<div hx-get=".../panel" hx-trigger="load delay:1500ms"
     hx-target="#chatedit" hx-swap="morph:innerHTML" hidden></div>{{end}}
```

`hx-trigger="load delay:1500ms"` is a ONE-SHOT trigger: it fires only when htmx
first PROCESSES a freshly-added node. The design relied on each poll REPLACING
the node so the fresh copy re-arms the trigger. But `session-poll-dom-morph`
switched the swap to idiomorph (`hx-swap="morph:innerHTML"`), which PATCHES the
DOM in place and PRESERVES a node it can match (same tag, same attributes, no
distinguishing id). The preserved poll node's `load` trigger therefore never
re-fires, so polling stops after the FIRST tick. If the turn outlives that one
1.5s tick (the common case), the panel never refreshes again and the working
bubble sticks until a manual refresh.

The server side was already correct: `runTurn` clears `busy` the moment
`swarm.Session.Prompt` returns, and `Prompt` blocks until the assistant message
reports its `completed` timestamp — the exact completed-turn idle signal the
runner (opencode-turn-poll) uses. The panel simply stopped asking.

## Design

All changes are client-facing surface in `internal/web` (the chat-diff editor's
handler-side panel projection + its template); no swarm/opencode-db reads, no new
endpoint. The lifecycle (connecting → connected → working) is untouched — only
the terminal (idle) transition is repaired.

### 1. Re-arm the poll reliably — `chatedit.go` + `chatedit_panel.html`

`chatPanelData` now emits a per-render `Nonce` (`sess.mgr.now().UnixNano()`) and
the re-arm poll node carries it in its id: `id="chatedit-poll-{{.Nonce}}"`.
Because idiomorph keys node identity on id, a changing id forces it to REPLACE
(not preserve) the poll node every tick, so htmx re-processes the fresh node,
`load delay:1500ms` re-arms, and the loop continues until the turn goes idle —
at which point the panel renders WITHOUT the node (still gated on `{{if .Busy}}`)
and polling stops. On idle the same render drops the `msg agent busy` bubble and
appends the completed reply, so the working state auto-clears with no refresh.

Tradeoff: while busy, the unique id makes each panel body differ, so the
`poll-fragment-etag-304` short-circuit no longer collapses successive busy ticks
to a 304. That is correct and necessary — the whole point is that the busy panel
MUST keep re-fetching — and a busy panel's live steps change across ticks anyway.
An IDLE panel emits no poll node and no Nonce, so its bytes stay identical and
the 304 path is unaffected (TestPolledPanelFragmentETag304 still passes).

### 2. Render the settled reply as markdown — `chatedit.go` + `chatedit_panel.html`

The log is projected through a new `chatLogView`/`chatLogEntry` that renders an
AGENT turn's body as sanitized markdown→HTML via `renderMarkdown` (the same VIEW
path the doc/session panes use), leaving user/system turns as plain escaped text.
So once idle the working bubble is replaced by a formatted reply, consistent with
the existing rendered path, instead of raw markdown.

## Tests — `internal/web/chatedit_test.go`

- `TestChatEditPanelClearsWorkingOnIdle` (new): a gated turn renders the busy
  panel (asserts the `msg agent busy` bubble, the spinner, and the re-arm poll
  node `id="chatedit-poll-…"` with `hx-trigger="load delay:1500ms"`); once the
  gate opens and the turn settles, the very next render asserts the working
  bubble, spinner, AND poll node are ALL gone, and the rendered markdown reply
  (`<strong>done</strong>`) is present — the busy→idle fragment swap with no
  refresh.
- Existing `TestChatStartChatShowsUserMessageAndWorkingBeforeReply` updated for
  the new `[]chatLogEntry` log projection (agent body is now rendered HTML).

## Acceptance mapping

- *auto-clears the working/spinner state and shows the rendered reply, no manual
  refresh* → the per-render poll-node id keeps the poll firing until idle, then
  the idle render drops the working bubble; `TestChatEditPanelClearsWorkingOnIdle`.
- *idle transition driven by the same completed-turn signal the runner uses
  (repo/editor session state only, no opencode-db)* → `busy` is cleared by
  `runTurn` when `Prompt` returns on the assistant `completed` timestamp;
  unchanged, no out-of-repo reads.
- *in-flight connecting/connected/working indicators unchanged for pre-idle
  states* → `connState`/badge logic untouched; only the terminal render changes.
- *a test asserts the busy→idle transition swaps the rendered fragment for the
  working bubble* → `TestChatEditPanelClearsWorkingOnIdle`.
- *go test ./... green under CGO_ENABLED=0* → full suite passes.
