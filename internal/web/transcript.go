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
// view. It carries a stable anchor so the TOC overlay can jump to it.
type transcriptTurn struct {
	Index  int           // 1-based position among turns (drives the TOC + anchor)
	Role   string        // "assistant" | "user" (the marker word)
	Label  string        // human label: "agent output" | "runner reply"
	Anchor string        // stable DOM id for the jump target ("turn-1", "turn-2", …)
	HTML   template.HTML // the turn body rendered from markdown to sanitized HTML
}

// transcriptView is a recorded transcript decomposed for the structured session
// pane: an optional Preamble (the "# session …" header block that precedes the
// first turn marker) plus the ordered Turns, each rendered to sanitized HTML,
// and the per-role counts the TOC overlay summarizes. Rendered is false when the
// body carried NO turn markers — a placeholder like "(waiting for session
// output…)", or a stub that has not begun its turns — in which case the template
// shows Preamble alone (still sanitized), never a raw text dump.
type transcriptView struct {
	Preamble   template.HTML    // header block before the first turn, rendered (may be empty)
	Turns      []transcriptTurn // the turns in file order
	Assistants int              // count of assistant turns (agent outputs)
	Users      int              // count of user turns (runner replies)
	Rendered   bool             // true iff at least one turn marker was found
}

// turnLabel maps a turn marker's role word to the human label the TOC and the
// section header show. An unknown role (future marker) falls back to itself so a
// new producer marker degrades to readable text rather than an empty label.
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
// UNTRUSTED repo files, so any embedded raw HTML is dropped and unsafe link
// protocols are stripped before the result is emitted as template.HTML.
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
