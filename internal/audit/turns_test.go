package audit

import "testing"

// TestSplitTurnsBoundaries proves a transcript decomposes on the EXACT
// "## user"/"## assistant" marker lines into ordered, role-labelled turns whose
// bodies carry the text between markers (marker line consumed, blanks trimmed),
// with the header block before the first marker captured as the preamble.
func TestSplitTurnsBoundaries(t *testing.T) {
	body := "# session bee-x\n\nsubmodule: alpha\n\n" +
		"## user\n\nfirst prompt\n\n" +
		"## assistant\n\ndoing work\n\n" +
		"## user\n\ncontinue\n\n" +
		"## assistant\n\ndone\n"
	pre, turns := SplitTurns(body)
	if pre != "# session bee-x\n\nsubmodule: alpha" {
		t.Fatalf("preamble = %q, want the header block before the first marker", pre)
	}
	if len(turns) != 4 {
		t.Fatalf("len(turns) = %d, want 4", len(turns))
	}
	wantRole := []string{"user", "assistant", "user", "assistant"}
	wantBody := []string{"first prompt", "doing work", "continue", "done"}
	for i, tn := range turns {
		if tn.Role != wantRole[i] {
			t.Errorf("turn %d role = %q, want %q", i, tn.Role, wantRole[i])
		}
		if tn.Body != wantBody[i] {
			t.Errorf("turn %d body = %q, want %q", i, tn.Body, wantBody[i])
		}
	}
}

// TestSplitTurnsExactMarkerRule locks the pinned exact-line rule: only a line
// EXACTLY equal to a marker splits. Inline, indented, or suffixed markers are
// ordinary content — matching internal/audit's scanBody so the structured view
// and the turn count agree on boundaries.
func TestSplitTurnsExactMarkerRule(t *testing.T) {
	body := "## user\n\n" +
		"quoting `## assistant` inline, and a trailing ## user mid-sentence\n" +
		"  ## user\n" + // indented: not a marker
		"## users\n" + // suffix: not a marker
		"still the one and only turn\n"
	pre, turns := SplitTurns(body)
	if pre != "" {
		t.Errorf("preamble = %q, want empty (body opens on a marker)", pre)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1 (only the exact '## user' line splits)", len(turns))
	}
	if turns[0].Role != "user" {
		t.Errorf("role = %q, want user", turns[0].Role)
	}
}

// TestSplitTurnsCRLF proves a trailing CR on a marker line still splits, so a
// transcript written with CRLF endings decomposes identically (scanBody tolerates
// the same trailing CR).
func TestSplitTurnsCRLF(t *testing.T) {
	pre, turns := SplitTurns("## user\r\n\r\nhello\r\n## assistant\r\n\r\nhi\r\n")
	if pre != "" {
		t.Errorf("preamble = %q, want empty", pre)
	}
	if len(turns) != 2 || turns[0].Role != "user" || turns[1].Role != "assistant" {
		t.Fatalf("turns = %+v, want a user then an assistant turn", turns)
	}
	if turns[0].Body != "hello" || turns[1].Body != "hi" {
		t.Errorf("bodies = %q/%q, want hello/hi (CR trimmed from content lines)", turns[0].Body, turns[1].Body)
	}
}

// TestSplitTurnsNoMarkers proves a body with no markers yields the whole
// (blank-trimmed) body as the preamble and no turns — the readable fallback for a
// placeholder or a not-yet-started stub.
func TestSplitTurnsNoMarkers(t *testing.T) {
	pre, turns := SplitTurns("(waiting for session output…)\n")
	if len(turns) != 0 {
		t.Fatalf("len(turns) = %d, want 0", len(turns))
	}
	if pre != "(waiting for session output…)" {
		t.Errorf("preamble = %q, want the whole body", pre)
	}
}
