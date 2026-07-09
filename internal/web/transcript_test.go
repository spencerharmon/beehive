package web

import (
	"fmt"
	"strings"
	"testing"
)

// TestParseTranscriptSplitsTurns proves a recorded transcript decomposes on the
// EXACT "## user"/"## assistant" marker lines (the audit parser's pinned turn
// boundaries) into ordered, role-labelled, anchored turns, each with its
// markdown body rendered to HTML, plus a rendered preamble and per-role counts.
func TestParseTranscriptSplitsTurns(t *testing.T) {
	body := "# session bee-x\n\nsubmodule: alpha · kind: work · branch: bee-x\n\n" +
		"## user\n\nfirst **prompt**\n\n" +
		"## assistant\n\ndoing `work`\n\n" +
		"## user\n\ncontinue\n\n" +
		"## assistant\n\ndone\n"
	v := parseTranscript(body)

	if !v.Rendered {
		t.Fatal("Rendered = false, want true for a transcript with turn markers")
	}
	if len(v.Turns) != 4 {
		t.Fatalf("len(Turns) = %d, want 4", len(v.Turns))
	}
	if v.Users != 2 || v.Assistants != 2 {
		t.Fatalf("Users=%d Assistants=%d, want 2/2", v.Users, v.Assistants)
	}
	wantRole := []string{"user", "assistant", "user", "assistant"}
	for i, turn := range v.Turns {
		if turn.Role != wantRole[i] {
			t.Errorf("turn %d Role = %q, want %q", i+1, turn.Role, wantRole[i])
		}
		if turn.Index != i+1 {
			t.Errorf("turn %d Index = %d, want %d", i+1, turn.Index, i+1)
		}
		if turn.Anchor != fmt.Sprintf("turn-%d", i+1) {
			t.Errorf("turn %d Anchor = %q, want turn-%d", i+1, turn.Anchor, i+1)
		}
	}
	if v.Turns[0].Label != "runner reply" || v.Turns[1].Label != "agent output" {
		t.Errorf("labels = %q/%q, want runner reply/agent output", v.Turns[0].Label, v.Turns[1].Label)
	}
	// Markdown is RENDERED to HTML, not dumped: **prompt** -> <strong>, `work` -> <code>.
	if got := string(v.Turns[0].HTML); !strings.Contains(got, "<strong>prompt</strong>") {
		t.Errorf("turn 1 HTML did not render bold markdown:\n%s", got)
	}
	if got := string(v.Turns[1].HTML); !strings.Contains(got, "<code>work</code>") {
		t.Errorf("turn 2 HTML did not render code markdown:\n%s", got)
	}
	// The marker line is a boundary, consumed — never re-rendered inside the body.
	if strings.Contains(string(v.Turns[1].HTML), "## assistant") {
		t.Errorf("turn body must not carry its own marker line:\n%s", v.Turns[1].HTML)
	}
	// The header block before the first turn is rendered, not dropped.
	if !strings.Contains(string(v.Preamble), "bee-x") {
		t.Errorf("preamble missing header text:\n%s", v.Preamble)
	}
}

// TestParseTranscriptExactMarkerRule locks the pinned exact-line rule: only a
// line EXACTLY equal to a turn marker splits. A marker quoted inline, indented,
// or carrying a suffix is ordinary content — matching internal/audit so the
// structured view and the audit turn count never disagree on boundaries.
func TestParseTranscriptExactMarkerRule(t *testing.T) {
	body := "## user\n\n" +
		"quoting `## assistant` inline, and a trailing ## user mid-sentence\n" +
		"  ## user\n" + // indented: not a marker
		"## users\n" + // suffix: not a marker
		"still the one and only turn\n"
	v := parseTranscript(body)
	if len(v.Turns) != 1 {
		t.Fatalf("len(Turns) = %d, want 1 (only the exact '## user' line splits)", len(v.Turns))
	}
	if v.Assistants != 0 || v.Users != 1 {
		t.Fatalf("Users=%d Assistants=%d, want 1/0 (inline/indented/suffixed markers ignored)", v.Users, v.Assistants)
	}
}

// TestParseTranscriptCRLF proves a trailing CR on a marker line still splits, so
// a transcript written with CRLF endings decomposes identically (the audit
// parser tolerates the same trailing CR).
func TestParseTranscriptCRLF(t *testing.T) {
	body := "## user\r\n\r\nhello\r\n## assistant\r\n\r\nhi\r\n"
	v := parseTranscript(body)
	if len(v.Turns) != 2 {
		t.Fatalf("len(Turns) = %d, want 2 for CRLF-delimited markers", len(v.Turns))
	}
	if v.Turns[0].Role != "user" || v.Turns[1].Role != "assistant" {
		t.Fatalf("roles = %q/%q, want user/assistant", v.Turns[0].Role, v.Turns[1].Role)
	}
}

// TestParseTranscriptNoMarkers proves a body with no turn markers (a placeholder
// like "(waiting…)", or a not-yet-started stub) yields Rendered=false with no
// turns and the whole body as the (still-sanitized) preamble — a readable
// fallback, never a hard error or an empty pane.
func TestParseTranscriptNoMarkers(t *testing.T) {
	v := parseTranscript("(waiting for session output…)")
	if v.Rendered {
		t.Error("Rendered = true, want false for a body with no turn markers")
	}
	if len(v.Turns) != 0 {
		t.Errorf("len(Turns) = %d, want 0", len(v.Turns))
	}
	if !strings.Contains(string(v.Preamble), "waiting for session output") {
		t.Errorf("preamble missing the placeholder text:\n%s", v.Preamble)
	}
}

// TestParseTranscriptSanitizes proves each turn inherits renderMarkdown's
// sanitization: an UNTRUSTED transcript's raw <script> is dropped and a
// javascript: link protocol is stripped, never emitted as live markup.
func TestParseTranscriptSanitizes(t *testing.T) {
	body := "## assistant\n\n<script>alert('xss')</script>\n\n[evil](javascript:alert(1))\n"
	v := parseTranscript(body)
	if len(v.Turns) != 1 {
		t.Fatalf("len(Turns) = %d, want 1", len(v.Turns))
	}
	got := string(v.Turns[0].HTML)
	if strings.Contains(got, "<script>") {
		t.Errorf("raw <script> survived sanitization:\n%s", got)
	}
	if strings.Contains(got, "javascript:") {
		t.Errorf("javascript: link protocol survived sanitization:\n%s", got)
	}
}

// TestRenderStringMatchesExecuteTemplate proves Server.renderString (the SSE
// stream's render path) produces byte-identical output to a direct
// ExecuteTemplate call for the SAME template+data — it is a thin buffering
// wrapper, not an alternate rendering path that could drift from the poll's.
func TestRenderStringMatchesExecuteTemplate(t *testing.T) {
	s, _ := setup(t)
	v := parseTranscript("## user\n\nhi\n\n## assistant\n\nhello\n")

	got, err := s.renderString("transcript_pane", v)
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	if err := s.tmpl.ExecuteTemplate(&want, "transcript_pane", v); err != nil {
		t.Fatal(err)
	}
	if got != want.String() {
		t.Fatalf("renderString diverged from ExecuteTemplate:\ngot:  %q\nwant: %q", got, want.String())
	}
}
