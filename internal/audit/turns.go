package audit

import "strings"

// Turn-marker lines in a recorded session transcript. A transcript is a flat
// markdown file whose turns are delimited by these EXACT header lines — the same
// pinned markers scanBody (parse.go) counts turns on, so anything splitting a
// transcript into turns splits on the identical boundaries the audit turn count
// trusts, never a second rule that could drift. "## assistant" is agent output;
// "## user" is a runner reply (the prompt or the "continue" the runner feeds the
// agent between turns).
const (
	TurnMarkerAssistant = "## assistant"
	TurnMarkerUser      = "## user"
)

// Turn is one segment of a transcript between two turn-marker lines: the marker's
// Role ("assistant" | "user") and the raw markdown Body that follows it up to the
// next marker (surrounding blank lines trimmed). The marker line is the boundary
// and is NOT part of Body.
type Turn struct {
	Role string // "assistant" | "user" (the marker word)
	Body string // raw markdown between this marker and the next, blank-trimmed
}

// SplitTurns splits a recorded transcript body into the preamble (everything
// before the first turn marker — e.g. the "# session …" header block) and the
// ordered turns. It splits ONLY on lines EXACTLY equal to a turn marker (a
// trailing CR tolerated), the identical pinned rule scanBody counts turns on, so
// the structured view and the audit turn count never disagree on boundaries: a
// marker quoted inline, indented, or carrying a suffix is ordinary content, not a
// boundary. The marker line is consumed as the boundary (its word becomes the
// turn's Role) and appears in neither the preamble nor any Body. A body with no
// markers yields the whole (blank-trimmed) body as the preamble and no turns —
// the caller's readable fallback for a placeholder or a not-yet-started stub.
func SplitTurns(body string) (preamble string, turns []Turn) {
	var (
		pre    []string
		role   string
		cur    []string
		inTurn bool
	)
	flush := func() {
		if inTurn {
			turns = append(turns, Turn{Role: role, Body: strings.Trim(strings.Join(cur, "\n"), "\n")})
		}
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch line {
		case TurnMarkerAssistant:
			flush()
			inTurn, role, cur = true, "assistant", nil
			continue
		case TurnMarkerUser:
			flush()
			inTurn, role, cur = true, "user", nil
			continue
		}
		if inTurn {
			cur = append(cur, line)
		} else {
			pre = append(pre, line)
		}
	}
	flush()
	return strings.Trim(strings.Join(pre, "\n"), "\n"), turns
}
