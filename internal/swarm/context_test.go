package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spencerharmon/beehive/prompts"
)

// TestFileFeedFirstSightPinsSilently proves a file the agent surfaces for the
// first time is pinned but NOT re-emitted: the agent already has it from its own
// read, so echoing the full body back is exactly the per-turn waste this cuts.
func TestFileFeedFirstSightPinsSilently(t *testing.T) {
	tc := newTurnCompactor()
	out := tc.fileFeed(map[string]string{"a.go": "line1\nline2\n"})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("first sight of a file must not re-emit its content, got:\n%s", out)
	}
	if tc.pins["a.go"] != "line1\nline2\n" {
		t.Fatalf("first sight must pin the content for later diffing; pins=%v", tc.pins)
	}
}

// TestFileFeedUnchangedIsNoOpLine proves a file unchanged since its pin collapses
// to a single one-line no-op — never a diff, never the file body.
func TestFileFeedUnchangedIsNoOpLine(t *testing.T) {
	tc := newTurnCompactor()
	cur := map[string]string{"a.go": "x\n", "b.go": "y\n"}
	tc.fileFeed(cur)        // first sight: pin
	out := tc.fileFeed(cur) // unchanged
	if !strings.Contains(out, "unchanged since last turn: a.go, b.go") {
		t.Fatalf("unchanged files must collapse to one no-op line, got:\n%s", out)
	}
	if strings.Contains(out, "@@") {
		t.Fatalf("unchanged files must not produce a diff, got:\n%s", out)
	}
}

// TestFileFeedChangedEmitsDiffNotFullFile proves a changed file yields a unified
// diff of only the changed hunk, materially smaller than re-sending the file.
func TestFileFeedChangedEmitsDiffNotFullFile(t *testing.T) {
	tc := newTurnCompactor()
	big := strings.Repeat("same line\n", 200)
	tc.fileFeed(map[string]string{"a.go": big + "old tail\n"})
	out := tc.fileFeed(map[string]string{"a.go": big + "new tail\n"})
	if !strings.Contains(out, "@@") || !strings.Contains(out, "-old tail") || !strings.Contains(out, "+new tail") {
		t.Fatalf("a changed file must yield a unified diff, got:\n%s", out)
	}
	if len(out) >= len(big) {
		t.Fatalf("diff (%d bytes) is not smaller than the full file (%d bytes); the diff feed saved nothing", len(out), len(big))
	}
	if strings.Contains(out, "same line\nsame line") {
		t.Fatalf("diff re-embedded unchanged context lines, got:\n%s", out)
	}
}

// TestRollingSummaryVerbatimUnderCap proves a transcript within the cap is
// returned unchanged (no lossy compression when none is needed).
func TestRollingSummaryVerbatimUnderCap(t *testing.T) {
	tc := &turnCompactor{summaryCap: 100}
	s := "short transcript\nsecond line"
	if got := tc.rollingSummary(s); got != s {
		t.Fatalf("under-cap summary must be verbatim;\n got: %q\nwant: %q", got, s)
	}
}

// TestRollingSummaryElidesMiddleKeepsEnds proves an over-cap transcript keeps the
// head (task framing) and, critically, the tail (most recent state / completion
// cue), eliding only the middle — the tail is what the completion check needs.
func TestRollingSummaryElidesMiddleKeepsEnds(t *testing.T) {
	tc := &turnCompactor{summaryCap: 300}
	lines := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("line-%03d", i))
	}
	full := strings.Join(lines, "\n")
	got := tc.rollingSummary(full)

	if !strings.Contains(got, "elided") {
		t.Fatalf("over-cap summary must mark the elision, got:\n%s", got)
	}
	if !strings.Contains(got, "line-000") {
		t.Errorf("summary dropped the head (task framing):\n%s", got)
	}
	if !strings.Contains(got, "line-099") {
		t.Errorf("summary dropped the tail (most recent state / completion cue):\n%s", got)
	}
	if strings.Contains(got, "line-050") {
		t.Errorf("summary did not elide the middle scrollback:\n%s", got)
	}
	if len(got) >= len(full) {
		t.Errorf("summary (%d) not smaller than the full transcript (%d)", len(got), len(full))
	}
}

// TestAssembleIsSmallerThanReinjectAll is the headline byte-cut proof: on a turn
// whose only file is unchanged and whose transcript is long, the assembled brief
// is materially smaller than re-injecting the whole transcript plus the whole
// file, and never re-embeds the unchanged file body.
func TestAssembleIsSmallerThanReinjectAll(t *testing.T) {
	tc := newTurnCompactor()
	big := strings.Repeat("package main // boilerplate line\n", 300)
	files := map[string]string{"main.go": big}
	tc.fileFeed(files) // an earlier turn pinned main.go
	transcript := strings.Repeat("assistant: reasoning about the code\n", 500)

	lean := tc.assemble("continue", transcript, files)
	full := reinjectAll(transcript, files)

	if len(lean) >= len(full) {
		t.Fatalf("assembled turn context (%d) is not smaller than re-inject-everything (%d)", len(lean), len(full))
	}
	if strings.Contains(lean, big) {
		t.Fatalf("assembled context re-embedded the unchanged file in full")
	}
	if !strings.Contains(lean, "continue") {
		t.Fatalf("assembled context dropped the caller's instruction")
	}
	if !strings.Contains(lean, "do NOT re-read unchanged files") {
		t.Fatalf("assembled context omitted the no-re-read directive the byte cut depends on")
	}
}

// TestLineDiffTrimsCommonContext proves lineDiff emits only the changed hunk,
// trimming the common leading/trailing lines.
func TestLineDiffTrimsCommonContext(t *testing.T) {
	d := lineDiff("a\nb\nc\nd\n", "a\nB\nc\nd\n")
	if !strings.Contains(d, "-b\n") || !strings.Contains(d, "+B\n") {
		t.Fatalf("diff missing the change, got:\n%s", d)
	}
	if strings.Contains(d, "-a") || strings.Contains(d, "+a") || strings.Contains(d, "c\nc") || strings.Contains(d, "-d") {
		t.Fatalf("diff should trim common prefix/suffix, got:\n%s", d)
	}
}

// TestLatestFileContentsReadWriteLatestWins proves file snapshots are harvested
// from completed reads and writes, later turns win, and incomplete reads are
// ignored (no partial content pollutes the pins).
func TestLatestFileContentsReadWriteLatestWins(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Parts: []Part{
			{Type: "tool", Tool: "read", Status: "completed", Input: map[string]any{"filePath": "a.go"}, Output: "v1"},
		}},
		{Role: "assistant", Parts: []Part{
			{Type: "tool", Tool: "write", Input: map[string]any{"filePath": "a.go", "content": "v2"}},
			{Type: "tool", Tool: "read", Status: "completed", Input: map[string]any{"filePath": "b.go"}, Output: "bee"},
			{Type: "tool", Tool: "read", Status: "running", Input: map[string]any{"filePath": "c.go"}, Output: ""},
		}},
	}
	got := latestFileContents(msgs)
	if got["a.go"] != "v2" {
		t.Errorf("a later write must override an earlier read; got %q", got["a.go"])
	}
	if got["b.go"] != "bee" {
		t.Errorf("completed read content missing; got %q", got["b.go"])
	}
	if _, ok := got["c.go"]; ok {
		t.Errorf("an incomplete read must not be harvested")
	}
}

// TestTranscriptTextRendersTextAndTools proves the digest source captures
// assistant text, reasoning, and tool commands + output.
func TestTranscriptTextRendersTextAndTools(t *testing.T) {
	msgs := []Message{{Role: "assistant", Parts: []Part{
		{Type: "reasoning", Text: "thinking"},
		{Type: "text", Text: "hello"},
		{Type: "tool", Tool: "bash", Status: "completed", Input: map[string]any{"command": "go build"}, Output: "ok"},
	}}}
	got := transcriptText(msgs)
	for _, want := range []string{"assistant: thinking", "assistant: hello", "tool bash go build", "ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcriptText missing %q in:\n%s", want, got)
		}
	}
}

// errMsgSession fails every Messages poll, to drive the fallback path.
type errMsgSession struct{}

func (errMsgSession) Prompt(context.Context, string) (string, error) { return "", nil }
func (errMsgSession) Messages(context.Context) ([]Message, error) {
	return nil, errors.New("poll failed")
}
func (errMsgSession) Close() error { return nil }

// TestLeanContextPromptFallsBackOnPollError proves a session-poll failure never
// fails the turn: leanContextPrompt degrades to the plain nextPrompt instruction
// (here the bare "continue"), so the loop proceeds exactly as the default path.
func TestLeanContextPromptFallsBackOnPollError(t *testing.T) {
	r := &Runner{LeanContext: true} // LeanInject false -> nextPrompt == "continue"
	sel := &selectt.Selection{Kind: selectt.Work}
	got := r.leanContextPrompt(context.Background(), errMsgSession{}, newTurnCompactor(), sel, "bee-T1")
	if got != "continue" {
		t.Fatalf("on a Messages() error the prompt must fall back to the plain instruction, got:\n%s", got)
	}
}

// TestLeanContextWrapsFollowUpTurn is the end-to-end wiring proof: with
// LeanContext on, a Work run still completes and the post-first turn's prompt is
// the bounded brief (the assembled header) wrapping the continue instruction,
// while turn one still sends the seeded `first` verbatim.
func TestLeanContextWrapsFollowUpTurn(t *testing.T) {
	g, sm, planPath, sel, rp := newWorkRun(t)

	var sent []string
	cl := &mockClient{sess: &mockSession{all: &sent, onTurn: func(turn int) {
		if turn == 2 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		}
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, LeanContext: true}
	res, err := r.Run(context.Background(), sel, prompts.Honeybee, "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	if len(sent) < 2 {
		t.Fatalf("expected at least 2 turns, got %d prompts: %q", len(sent), sent)
	}
	if !strings.HasSuffix(sent[0], "first") || strings.Contains(sent[0], "# Bounded turn context") {
		t.Errorf("turn one must carry the seeded prompt without the bounded brief, got:\n%s", sent[0])
	}
	if !strings.Contains(sent[1], "# Bounded turn context") {
		t.Errorf("lean follow-up turn is not the bounded brief:\n%s", sent[1])
	}
	if !strings.Contains(sent[1], "continue") {
		t.Errorf("lean follow-up turn dropped the continue instruction:\n%s", sent[1])
	}
}

// TestLeanContextOffIsInert proves toggling LeanContext off changes nothing on the
// per-turn path: the follow-up prompt is the byte-identical bare "continue".
func TestLeanContextOffIsInert(t *testing.T) {
	g, sm, planPath, sel, rp := newWorkRun(t)

	var sent []string
	cl := &mockClient{sess: &mockSession{all: &sent, onTurn: func(turn int) {
		if turn == 2 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		}
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour} // LeanContext: false
	res, err := r.Run(context.Background(), sel, prompts.Honeybee, "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	if len(sent) < 2 || sent[1] != "continue" {
		t.Fatalf("LeanContext off must leave the follow-up prompt the bare \"continue\", got: %q", sent)
	}
}
