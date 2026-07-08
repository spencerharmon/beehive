package audit

import (
	"bufio"
	"bytes"
	"strings"
)

// Turn is one segment of a transcript body, split on the exact-line
// "## assistant" / "## user" turn markers described by the package's PINNED
// turn-count rule (see the package doc). Role is "assistant" or "user" for a
// real turn, or "" for the leading preamble segment — the transcript's own
// "# session <id>" title line and "submodule: ... " metadata header line, or
// (when a transcript carries no turn marker at all, e.g. a legacy/malformed
// file) the entire body. Body is the raw markdown source strictly between
// this marker (exclusive of the marker line itself) and the next marker or
// EOF, with leading/trailing blank lines trimmed.
type Turn struct {
	Role string
	Body string
}

// SplitTurns splits raw transcript bytes into ordered Turn segments using the
// IDENTICAL exact-line rule scanBody counts turns by: a line must equal
// "## assistant" or "## user" verbatim, not merely start with "## ", because
// assistant output routinely embeds its own level-2 markdown headings (e.g.
// "## Notes") that must never be mistaken for a turn boundary. Callers that
// render turns (internal/web's session view) and this package's own Turns/
// UserTurns count therefore agree, by construction, on where a turn begins —
// this is the single shared segmentation the session-transcript-rendered-toc
// ROI asked for ("reuse... rather than re-deriving a second parser").
//
// A transcript with no turn-marker line at all yields exactly one Role==""
// segment wrapping the whole input, so a caller always gets at least one
// segment back for non-empty input (nil only for an empty transcript). This
// function is purely structural: it does not render markdown and does not
// special-case the trailing "## \u26a0\ufe0f warning" block the way scanBody
// does for abort classification — a warning header is just plain body text
// here, folded into whichever segment it falls in (normally the last one),
// and renders as an ordinary heading when the caller runs the body through a
// markdown renderer.
func SplitTurns(data []byte) []Turn {
	if len(data) == 0 {
		return nil
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var turns []Turn
	role := ""
	var lines []string
	flush := func() {
		body := strings.Trim(strings.Join(lines, "\n"), "\n")
		if role != "" || body != "" {
			turns = append(turns, Turn{Role: role, Body: body})
		}
		lines = nil
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch line {
		case "## assistant":
			flush()
			role = "assistant"
			continue
		case "## user":
			flush()
			role = "user"
			continue
		}
		lines = append(lines, line)
	}
	flush()
	if err := sc.Err(); err != nil {
		// An over-long line or similar scan failure: fall back to the whole
		// input as one opaque segment rather than dropping it — a rendering
		// caller must never lose content on a scan error the way a metrics
		// caller (which surfaces the error) would.
		return []Turn{{Role: "", Body: strings.Trim(string(data), "\n")}}
	}
	return turns
}
