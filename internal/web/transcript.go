package web

import (
	"bytes"
	"fmt"
	"html/template"

	"github.com/spencerharmon/beehive/internal/audit"
)

// transcriptTurn is one rendered, navigable turn in a session transcript view:
// a stable 1-based Index/Anchor pair (so a poll refresh's TOC/jump links keep
// pointing at the same place across re-renders), the producer Role
// ("assistant"/"user"/"" — see audit.Turn), a reader-facing Label, and the
// turn's markdown body already rendered to SANITIZED HTML via renderMarkdown
// (never raw agent-authored text/HTML).
type transcriptTurn struct {
	Index  int
	Role   string
	Label  string
	Anchor string
	HTML   template.HTML
}

// turnLabel maps a producer role to the reader-facing word the ROI asked the
// TOC/jump controls to navigate between: "agent outputs and runner replies".
// The raw role ("user"/"assistant") is kept as-is for the anchor/CSS class
// (turn-user/turn-assistant), so styling and the producer's own vocabulary
// never drift; Label is ONLY the human-facing word.
func turnLabel(role string) string {
	switch role {
	case "assistant":
		return "Agent"
	case "user":
		return "Runner"
	default:
		return "Session"
	}
}

// buildTranscriptTurns splits a transcript body into rendered, navigable turns
// (session-transcript-rendered-toc): it reuses audit.SplitTurns for
// segmentation — the SAME exact-line "## assistant"/"## user" rule the audit
// engine counts turns by, per the ROI's explicit "reuse... rather than
// re-deriving a second parser" — and renders each segment's markdown through
// renderMarkdown (editor-markdown-render), so every byte that reaches the page
// is sanitized the same way an explorer/ROI view is.
//
// The leading preamble segment (audit.Turn{Role: ""} — the transcript's own
// "# session <id>" title and "submodule: ..." header line) is DROPPED when at
// least one real "user"/"assistant" turn follows it: that header is already
// shown by the page chrome (session_view.html's Name/Branch/Live badge), and
// would otherwise render as a redundant, unlabeled first card. A transcript
// with NO real turn at all (a legacy/malformed transcript, or a placeholder
// like "(waiting for session output…)") keeps its lone preamble segment as
// turn 1 — labeled "Session" — so the page always has at least one rendered,
// anchorable turn instead of silently rendering nothing.
//
// An empty body yields nil, which session_body.html's {{if .Turns}} treats as
// falsy and falls back to the legacy flat <pre> render (also what a caller
// that only supplies "Body", never "Turns" — e.g. an existing white-box
// template test — gets, since a missing map key is likewise falsy).
func buildTranscriptTurns(body string) []transcriptTurn {
	segs := audit.SplitTurns([]byte(body))
	if len(segs) == 0 {
		return nil
	}
	hasReal := false
	for _, sg := range segs {
		if sg.Role != "" {
			hasReal = true
			break
		}
	}
	turns := make([]transcriptTurn, 0, len(segs))
	for _, sg := range segs {
		if hasReal && sg.Role == "" {
			continue
		}
		idx := len(turns) + 1
		turns = append(turns, transcriptTurn{
			Index:  idx,
			Role:   sg.Role,
			Label:  turnLabel(sg.Role),
			Anchor: fmt.Sprintf("turn-%d", idx),
			HTML:   renderMarkdown(sg.Body),
		})
	}
	return turns
}

// renderString executes a named template into a string instead of an
// http.ResponseWriter — the SSE transcript stream (sessionStream) needs the
// exact same "transcript_turns" markup the htmx poll renders (session_body.html)
// as a string to push as one SSE data frame, so the two delivery paths can
// never drift (session-transcript-rendered-toc's no-path-specific-rendering-
// drift requirement).
func (s *Server) renderString(name string, data interface{}) (string, error) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
