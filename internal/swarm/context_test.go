package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLineDiff(t *testing.T) {
	cases := []struct {
		name, old, neu string
		want           string // "" means expect empty
		wantHas        []string
		wantMissing    []string
	}{
		{name: "identical", old: "a\nb\nc\n", neu: "a\nb\nc\n", want: ""},
		{name: "identical no trailing newline", old: "a\nb", neu: "a\nb\n", want: ""},
		{
			name: "single middle change", old: "a\nb\nc\n", neu: "a\nB\nc\n",
			wantHas:     []string{"-b\n", "+B\n"},
			wantMissing: []string{"a", "c"}, // unchanged prefix/suffix not emitted
		},
		{
			name: "insert line", old: "a\nc\n", neu: "a\nb\nc\n",
			wantHas: []string{"+b\n"}, wantMissing: []string{"a", "c"},
		},
		{
			name: "delete line", old: "a\nb\nc\n", neu: "a\nc\n",
			wantHas: []string{"-b\n"}, wantMissing: []string{"a", "c"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lineDiff(c.old, c.neu)
			if c.want == "" && len(c.wantHas) == 0 {
				if got != "" {
					t.Fatalf("expected empty diff, got %q", got)
				}
				return
			}
			for _, h := range c.wantHas {
				if !strings.Contains(got, h) {
					t.Fatalf("diff missing %q:\n%s", h, got)
				}
			}
			for _, m := range c.wantMissing {
				// The unchanged line must not appear as an emitted +/- line.
				if strings.Contains(got, "-"+m+"\n") || strings.Contains(got, "+"+m+"\n") {
					t.Fatalf("diff should not touch unchanged line %q:\n%s", m, got)
				}
			}
		})
	}
}

// TestFileSectionPinsAndDiffs is the working-set lever: a file is sent full the
// first time, OMITTED while unchanged, and sent as a DIFF when it changes.
func TestFileSectionPinsAndDiffs(t *testing.T) {
	tc := newTurnContext(0)

	// Turn 1: brand-new file -> full content.
	s1 := tc.fileSection(map[string]string{"a.go": "line1\nline2\nline3\n"})
	if !strings.Contains(s1, "a.go (new)") || !strings.Contains(s1, "line2") {
		t.Fatalf("new file must be sent in full:\n%s", s1)
	}

	// Turn 2: same content -> omitted entirely (not re-sent).
	s2 := tc.fileSection(map[string]string{"a.go": "line1\nline2\nline3\n"})
	if s2 != "" {
		t.Fatalf("unchanged file must be omitted, got:\n%s", s2)
	}

	// Turn 3: content changed -> a diff, not the whole file.
	s3 := tc.fileSection(map[string]string{"a.go": "line1\nCHANGED\nline3\n"})
	if !strings.Contains(s3, "a.go (changed") || !strings.Contains(s3, "+CHANGED") {
		t.Fatalf("changed file must be sent as a diff:\n%s", s3)
	}
	if strings.Contains(s3, "line1") {
		t.Fatalf("diff must not re-send the unchanged surrounding lines:\n%s", s3)
	}
}

// TestRollTranscriptBounded is the history lever: prior turns roll into a bounded
// summary (oldest evicted) rather than growing without limit, and a message is
// digested at most once.
func TestRollTranscriptBounded(t *testing.T) {
	tc := newTurnContext(80) // small cap to force eviction of the oldest digest
	mk := func(id, text string) Message {
		return Message{ID: id, Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}
	}
	msgs := []Message{
		mk("m1", "oldest decision AAAAAAAAAAAAAAAAAAAA"),
		mk("m2", "middle decision BBBBBBBBBBBBBBBBBBBB"),
		mk("m3", "newest decision CCCCCCCCCCCCCCCCCCCC"),
	}
	tc.rollTranscript(msgs)
	if tc.summaryBytes() > tc.summaryMax {
		t.Fatalf("summary %d bytes exceeds cap %d:\n%s", tc.summaryBytes(), tc.summaryMax, tc.summary())
	}
	if !strings.Contains(tc.summary(), "newest decision") {
		t.Fatalf("most recent decision must be retained:\n%s", tc.summary())
	}
	if strings.Contains(tc.summary(), "oldest decision") {
		t.Fatalf("oldest decision should have been evicted:\n%s", tc.summary())
	}

	// Re-rolling the same messages must not double-count them.
	before := len(tc.digests)
	tc.rollTranscript(msgs)
	if len(tc.digests) != before {
		t.Fatalf("already-summarised messages were digested again: %d -> %d", before, len(tc.digests))
	}
}

// TestDigestTurnDropsVerbatimOutput proves the summary keeps decisions + actions
// but drops the verbatim tool OUTPUT that dominates transcript size.
func TestDigestTurnDropsVerbatimOutput(t *testing.T) {
	m := Message{ID: "x", Role: "assistant", Parts: []Part{
		{Type: "text", Text: "Decided to refactor the loader."},
		{Type: "tool", Tool: "read", Input: map[string]any{"filePath": "loader.go"}, Output: strings.Repeat("VERBATIM-DUMP ", 500)},
	}}
	d := digestTurn(m)
	if !strings.Contains(d, "Decided to refactor") {
		t.Fatalf("digest dropped the decision text:\n%s", d)
	}
	if !strings.Contains(d, "read loader.go") {
		t.Fatalf("digest dropped the tool action:\n%s", d)
	}
	if strings.Contains(d, "VERBATIM-DUMP") {
		t.Fatalf("digest must not carry verbatim tool output:\n%s", d)
	}
}

// TestAssembleBelowBaseline is acceptance criterion (2): across a multi-turn
// fixture, the bounded per-turn payload is strictly smaller than re-injecting
// every file in full plus the whole transcript, and unchanged files are absent.
func TestAssembleBelowBaseline(t *testing.T) {
	tc := newTurnContext(0)
	big := strings.Repeat("filler line that is stable across turns\n", 60)
	files := map[string]string{
		"stable_a.go": "package a\n" + big + "UNIQUE_MARKER_A\n",
		"stable_b.go": "package b\n" + big + "UNIQUE_MARKER_B\n",
		"churn.go":    "package c\nvalue := 1\n",
	}
	turn1 := []Message{{ID: "m1", Role: "assistant", Parts: []Part{{Type: "text", Text: "Read the files; starting work."}}}}

	// Turn 1 pins all three files.
	tc.assemble("continue", files, turn1)

	// Turn 2: only churn.go changes; the stable files are unchanged.
	files2 := map[string]string{
		"stable_a.go": files["stable_a.go"],
		"stable_b.go": files["stable_b.go"],
		"churn.go":    "package c\nvalue := 2\n",
	}
	turn2 := append(turn1, Message{ID: "m2", Role: "assistant", Parts: []Part{
		{Type: "text", Text: "Adjusted the value."},
		{Type: "tool", Tool: "edit", Input: map[string]any{"filePath": "churn.go"}, Output: strings.Repeat("noise ", 400)},
	}})

	bounded := tc.assemble("continue", files2, turn2)
	baseline := reinjectEverything("continue", files2, turn2)

	if len(bounded) >= len(baseline) {
		t.Fatalf("bounded payload (%d) not smaller than baseline (%d)", len(bounded), len(baseline))
	}
	// Unchanged files must not be re-sent in full.
	if strings.Contains(bounded, "UNIQUE_MARKER_A") || strings.Contains(bounded, "UNIQUE_MARKER_B") {
		t.Fatalf("unchanged file content re-sent on turn 2:\n%s", bounded)
	}
	// The change itself must be present (as a diff).
	if !strings.Contains(bounded, "+value := 2") {
		t.Fatalf("changed content missing from bounded payload:\n%s", bounded)
	}
	// The directive must survive so the loop never loses its "continue".
	if !strings.HasSuffix(bounded, "continue") {
		t.Fatalf("bounded payload dropped the turn directive:\n%s", bounded)
	}
}

// TestAssembleBareWhenEmpty: with nothing to bound yet, assemble is exactly the
// directive, so an early turn behaves like a plain continue.
func TestAssembleBareWhenEmpty(t *testing.T) {
	tc := newTurnContext(0)
	if got := tc.assemble("continue", nil, nil); got != "continue" {
		t.Fatalf("empty assemble must equal the bare directive, got %q", got)
	}
}

// TestFilesFromTranscript reads the current on-disk content of files the agent
// touched, resolving relative paths against cwd and skipping unreadable ones.
func TestFilesFromTranscript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "rel.go"), []byte("REL-CONTENT\n"), 0o644)
	abs := filepath.Join(dir, "abs.go")
	os.WriteFile(abs, []byte("ABS-CONTENT\n"), 0o644)

	msgs := []Message{{ID: "m", Role: "assistant", Parts: []Part{
		{Type: "tool", Tool: "read", Input: map[string]any{"filePath": "rel.go"}},
		{Type: "tool", Tool: "write", Input: map[string]any{"filePath": abs}},
		{Type: "tool", Tool: "read", Input: map[string]any{"filePath": "gone.go"}}, // unreadable -> skipped
		{Type: "tool", Tool: "bash", Input: map[string]any{"command": "ls"}},       // not a file tool
	}}}
	got := filesFromTranscript(msgs, dir)
	if got["rel.go"] != "REL-CONTENT\n" {
		t.Fatalf("relative path not resolved against cwd: %q", got["rel.go"])
	}
	if got[abs] != "ABS-CONTENT\n" {
		t.Fatalf("absolute path not captured: %q", got[abs])
	}
	if _, ok := got["gone.go"]; ok {
		t.Fatalf("unreadable file must be skipped")
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly the two readable files, got %v", got)
	}
}
