# session-transcript-rendered-toc: render session transcripts as turns + a TOC

## Problem

The session view dumped the whole recorded transcript into one flat
`<pre class="session">` — a wall of raw markdown text. A finished honeybee
transcript is long (thousands of lines: tool calls, code, prose), so a reader had
to scroll a monospace plaintext blob with no structure and no way to jump between
the agent's outputs and the runner's replies. The ROI reconcile (ROI 3c6f3d66)
put it plainly: "Render sessions, don't scroll raw plaintext."

## Design

The transcript is already a flat markdown file whose turns are delimited by exact
`## user` / `## assistant` header lines — the SAME pinned markers
`internal/audit/parse.go` (`scanBody`) counts turns on. We split on those exact
boundaries, render each turn's markdown body to sanitized HTML, and add a
table-of-contents overlay. All changes are in `internal/web`; no swarm/runtime
behaviour changes and the file format is untouched.

### Parse + render — `internal/web/transcript.go` (new)

`parseTranscript(body string) transcriptView` walks the body line by line and
splits on lines EXACTLY equal to `## user` / `## assistant` (trailing CR
tolerated, matching audit). The marker line is consumed as the boundary (its role
becomes the section label), not re-rendered. Content before the first marker is
the `Preamble` (the `# session …` header block). Each turn's markdown body is run
through the existing `renderMarkdown` (editor-markdown-render's goldmark helper,
built WITHOUT `html.WithUnsafe`) — so sanitization is inherited wholesale:
UNTRUSTED transcript raw HTML is dropped and `javascript:`/`data:` links stripped.

`transcriptView` carries the rendered `Preamble`, the ordered `Turns` (each with
`Index`, `Role`, human `Label` — "agent output"/"runner reply" —, a stable
`Anchor` `turn-N`, and rendered `HTML`), the per-role counts, and `Rendered`
(false when the body has no markers, e.g. a `(waiting…)` placeholder — the
template then shows the sanitized `Preamble` alone, never a raw dump).

### Poll fragment — `internal/web/templates/session_body.html`

`sessionBody` now passes `parseTranscript(body)` as `Transcript` (was the raw
`Body` string). The fragment renders:

- a collapsible **TOC overlay** (`<details id="session-toc">`, floated top-right
  over the pane) listing every turn as a `[data-anchor]` jump link, plus
  prev/next `[data-jump]` buttons for agent outputs and runner replies;
- the scroll container `<div id="session-transcript" class="session"
  data-scroll-preserve data-scroll-pin>` holding the rendered `Preamble` and one
  `<section class="turn turn-<role>" id="turn-N">` per turn.

The container KEEPS its stable id and `data-scroll-preserve` + `data-scroll-pin`
attributes verbatim, so poll-scroll-preserve's save/restore + bottom-pin logic is
untouched (it keys purely off those). It changed from a `<pre>` to a `<div>`; the
CSS moves the box chrome/scroll onto `.session` and the raw pre-wrap onto the new
`.session-live`.

### Jump navigation — `internal/web/templates/session_view.html`

A new delegated `session-toc-nav` script (loaded for BOTH live and ended pages,
independent of the SSE stream) handles clicks on `[data-anchor]` / `[data-jump]`.
Because the transcript scrolls INSIDE `#session-transcript` (not the window), a
plain `#id` href would move the wrong scroller — the script scrolls the container
itself via `getBoundingClientRect` deltas. Delegated off `document` so it survives
every htmx poll-swap of `#session-body`; a no-op when the target is absent.

### Live SSE interplay (poll-scroll-preserve + agent-output-streaming)

While a honeybee runs, the SSE stream shows raw text token-by-token (rendering
half-written markdown — an unclosed ``` fence — would flicker). So the live frame
stays raw and the structured turns + TOC are the poll / "end" / no-SSE render (the
authoritative and by-far-most-common transcript view). `render()` in
session_view.html now targets the `<div>` container, streams into a single
`<pre class="session-live">` child, and hides any TOC left by a prior poll (its
anchors don't exist against raw text). On the stream's `end` event the existing
one authoritative poll swaps the structured turns + TOC back in. The pin math
still runs on `#session-transcript`, so live bottom-follow is unchanged.

### Styling — `internal/web/assets/style.css`

Token-only (dark mode flips for free): `.session` becomes the bordered scroll
box; `.session-live` the raw stream; `.turn` blocks get a role-tinted left rule
(accent = agent, amber = reply) and a small uppercase role header; `.session-toc`
is the floating, collapsible overlay with jump buttons and an anchored link list.

## Tests

- `internal/web/transcript_test.go` — `parseTranscript` splits on exact markers
  (inline/indented/suffixed markers ignored), tolerates CRLF, renders markdown
  (`**x**`→`<strong>`, `` `x` ``→`<code>`), drops the marker line from the body,
  renders the preamble, handles a no-marker placeholder (`Rendered=false`), and
  inherits sanitization (`<script>` dropped, `javascript:` stripped).
- `internal/web/web_test.go` —
  - `TestScrollPreserveWiring` updated to the new `Transcript` data shape; still
    asserts the container's id + scroll-preserve + pin.
  - `TestSessionTranscriptRendersTurnsAndTOC`: structured rendered turn sections,
    raw markdown/marker lines gone, TOC overlay + anchors + jump buttons, and the
    scroll-preserve contract all present in one fragment.
  - `TestSessionTranscriptSanitizedInTemplate`: no live `<script>`/`javascript:`
    through the template path.
  - `TestSessionBodyHandlerRendersTurns`: the `/…/body` HTTP handler renders a
    real on-disk final transcript as turns + TOC.
  - `TestSessionViewShipsTOCNavScript`: the container-scrolling nav script ships
    for both live and ended pages (asserted on surviving JS — html/template
    strips `<script>` comments), with no external lib.

## Files

- `internal/web/transcript.go` (new), `internal/web/transcript_test.go` (new)
- `internal/web/sessions.go` (sessionBody wires parseTranscript)
- `internal/web/templates/session_body.html`, `internal/web/templates/session_view.html`
- `internal/web/assets/style.css`
- `internal/web/web_test.go`
