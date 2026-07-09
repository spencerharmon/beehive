package web

import (
	"fmt"
	"html/template"
	"strings"
)

// Turn markers in a recorded session transcript. A transcript is a flat markdown
// file whose turns are delimited by these EXACT header lines — the same pinned
// markers internal/audit counts turns on (see internal/audit/parse.go scanBody),
// so the structured view splits on the identical boundaries the audit trusts.
// "## assistant" is agent output; "## user" is a runner reply (the prompt or the
// "continue" the runner feeds the agent between turns).
const (
	turnMarkerAssistant = "## assistant"
	turnMarkerUser      = "## user"
)

// transcriptTurn is one rendered turn: the block of text between two turn-marker
// lines, converted from markdown to sanitized HTML for the structured session
// view. It carries a stable anchor so the TOC overlay (and the prev/next jump
// buttons) can navigate to it.
type transcriptTurn struct {
	Index  int           // 1-based position among turns (drives the TOC + anchor)
	Role   string        // "assistant" | "user" (the marker word)
	Label  string        // human label: "agent output" | "runner reply"
	Anchor string        // stable DOM id for the jump target ("turn-1", "turn-2", …)
	HTML   template.HTML // the turn body rendered from markdown to sanitized HTML
}

// transcriptView is a recorded transcript decomposed for the structured session
// pane — rendered by the SHARED "transcript_pane" template (session_body.html)
// that BOTH the htmx-poll path (sessionBody) and the SSE stream path
// (sessionStream, via Server.renderString) execute, so the two transcript
// delivery paths can never disagree on the turn/TOC HTML for a given transcript
// (session-transcript-toc-relanding's no-rendering-drift requirement).
//
// Preamble is the optional "# session …" header block that precedes the first
// turn marker, rendered the same sanitized way. Turns are in file order. Rendered
// is false when the body carried NO turn markers — a placeholder like "(waiting
// for session output…)", or a legacy/malformed transcript — in which case the
// template shows Preamble alone (still sanitized), never a raw text dump.
type transcriptView struct {
	Preamble   template.HTML    // header block before the first turn, rendered (may be empty)
	Turns      []transcriptTurn // the turns in file order
	Assistants int              // count of assistant turns (agent outputs)
	Users      int              // count of user turns (runner replies)
	Rendered   bool             // true iff at least one turn marker was found
}

// turnLabel maps a turn marker's role word to the human label the TOC and the
// section header show. An unknown role (a future marker) falls back to itself so
// a new producer marker degrades to readable text rather than an empty label.
func turnLabel(role string) string {
	switch role {
	case "assistant":
		return "agent output"
	case "user":
		return "runner reply"
	default:
		return role
	}
}

// parseTranscript splits a recorded session transcript into its preamble and
// turns on the EXACT "## assistant"/"## user" marker lines (matching the audit
// parser's pinned rule, with a trailing CR tolerated), rendering each turn's
// markdown body to sanitized HTML via renderMarkdown. The marker line itself is
// consumed as the turn boundary — its role becomes the section label — and is
// NOT re-rendered as an inline heading. Content before the first marker is the
// preamble. A body with no markers yields Rendered=false with the whole body as
// the (rendered, sanitized) preamble, so callers still show readable content for
// placeholders and not-yet-started stubs.
//
// Sanitization is inherited wholesale from renderMarkdown: transcripts are
// UNTRUSTED repo files (arbitrary agent-authored text), so any embedded raw HTML
// is dropped and unsafe link protocols are stripped before the result is emitted
// as template.HTML.
//
// parseTranscript re-parses and re-renders the WHOLE body on every call — the
// same cost the htmx poll already pays each refresh, and (session-transcript-
// toc-relanding) also the SSE stream now pays each tick, since both paths must
// render identically. There is no incremental/cached path; a pathologically long
// transcript re-costs the full render every tick, matching the existing poll
// behavior rather than introducing a new regression.
func parseTranscript(body string) transcriptView {
	var (
		v        transcriptView
		preamble []string
		curRole  string
		curBody  []string
		inTurn   bool
	)
	flush := func() {
		if !inTurn {
			return
		}
		v.Turns = append(v.Turns, newTurn(len(v.Turns)+1, curRole, curBody))
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		role := ""
		switch line {
		case turnMarkerAssistant:
			role = "assistant"
		case turnMarkerUser:
			role = "user"
		}
		if role == "" {
			if inTurn {
				curBody = append(curBody, line)
			} else {
				preamble = append(preamble, line)
			}
			continue
		}
		// New turn marker: close the turn in progress, open the new one.
		flush()
		inTurn = true
		curRole = role
		curBody = nil
		if role == "assistant" {
			v.Assistants++
		} else {
			v.Users++
		}
	}
	flush()
	v.Rendered = len(v.Turns) > 0
	if pre := strings.TrimSpace(strings.Join(preamble, "\n")); pre != "" {
		v.Preamble = renderMarkdown(pre)
	}
	return v
}

// newTurn renders one turn's collected body lines to a transcriptTurn. The body
// is trimmed of surrounding blank lines (the marker line and the blank that
// follows it are boundary noise, not content) before markdown rendering.
func newTurn(index int, role string, bodyLines []string) transcriptTurn {
	body := strings.Trim(strings.Join(bodyLines, "\n"), "\n")
	return transcriptTurn{
		Index:  index,
		Role:   role,
		Label:  turnLabel(role),
		Anchor: fmt.Sprintf("turn-%d", index),
		HTML:   renderMarkdown(body),
	}
}

// renderString executes a named template into a string instead of an
// http.ResponseWriter. sessionStream (the SSE path) uses it to render the EXACT
// same "transcript_pane" template session_body.html executes for the htmx poll,
// so the two transcript delivery paths (agent-output-streaming) can never
// render different HTML for the same transcript — they share one template
// definition, not two independent renderers that could silently drift apart.
func (s *Server) renderString(name string, data interface{}) (string, error) {
	var buf strings.Builder
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
