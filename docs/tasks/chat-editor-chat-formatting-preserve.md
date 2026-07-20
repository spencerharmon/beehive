# chat-editor-chat-formatting-preserve

The chat-edit pane must honor authored formatting instead of collapsing every
message into one run of text (ROI "Frontend aesthetics" — "Chat window respects
formatting").

## Change

- `internal/web/chatedit.go`: `chatTurn.BodyHTML()` renders a turn's `Text`
  through the shared `renderMarkdown` path (the same sanitized goldmark renderer
  the doc / ROI / transcript view panes use). Empty text renders nothing; raw
  HTML / unsafe links are still dropped (no `WithUnsafe`).
- `internal/web/templates/chatedit_panel.html`: each message body now renders as
  `<div class="body">{{.BodyHTML}}</div>` instead of the raw `{{.Text}}` run, so
  newlines, whitespace, and markdown/code structure become real HTML block
  structure rather than being folded away by HTML whitespace collapsing.
- `internal/web/assets/style.css`: `.msg .body` spacing + `pre`/`code` wrapping so
  multi-line and fenced-code content displays as authored.

## Test

`TestChatPanelPreservesFormatting` asserts a multi-line message with a fenced Go
code block renders with `<p>`, `<pre>`, `<code>`, and its code text intact — both
via `BodyHTML()` directly and through the panel template.
