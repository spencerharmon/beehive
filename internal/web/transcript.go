package web

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/spencerharmon/beehive/internal/audit"
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
// first turn marker) plus the ordered Turns, each rendered to sanitized HTML, and
// the per-role counts the TOC overlay summarizes. Rendered is false when the body
// carried NO turn markers — a placeholder like "(waiting for session output…)",
// or a stub that has not begun its turns — in which case the template shows
// Preamble alone (still sanitized), never a raw text dump.
type transcriptView struct {
	Preamble   template.HTML    // header block before the first turn, rendered (may be empty)
	Turns      []transcriptTurn // the turns in file order
	Assistants int              // count of assistant turns (agent outputs)
	Users      int              // count of user turns (runner replies)
	Rendered   bool             // true iff at least one turn marker was found
}

// turnLabel maps a turn marker's role word to the human label the TOC and the
// section header show. An unknown role (a future marker) falls back to itself so a
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

// parseTranscript decomposes a recorded session transcript into its preamble and
// turns for the structured pane. It splits on the EXACT turn markers via
// audit.SplitTurns — the SAME pinned boundaries the audit turn count trusts, not
// a second rule that could drift — then renders each turn's markdown body (and
// the preamble) to sanitized HTML via renderMarkdown. A body with no markers
// yields Rendered=false with the whole body as the (rendered, sanitized)
// preamble, so callers still show readable content for placeholders and
// not-yet-started stubs.
//
// Sanitization is inherited wholesale from renderMarkdown: transcripts are
// UNTRUSTED repo files, so any embedded raw HTML is dropped and unsafe link
// protocols are stripped before the result is emitted as template.HTML.
func parseTranscript(body string) transcriptView {
	preamble, turns := audit.SplitTurns(body)
	v := transcriptView{Rendered: len(turns) > 0}
	if strings.TrimSpace(preamble) != "" {
		v.Preamble = renderMarkdown(preamble)
	}
	for i, tn := range turns {
		if tn.Role == "assistant" {
			v.Assistants++
		} else {
			v.Users++
		}
		v.Turns = append(v.Turns, transcriptTurn{
			Index:  i + 1,
			Role:   tn.Role,
			Label:  turnLabel(tn.Role),
			Anchor: fmt.Sprintf("turn-%d", i+1),
			HTML:   renderMarkdown(tn.Body),
		})
	}
	return v
}
