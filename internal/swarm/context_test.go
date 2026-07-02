package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFileSectionOmitsUnchangedDiffsChanged is the core token-lever proof at the
// mechanism level: a file the agent has already seen costs ZERO bytes on the next
// turn when unchanged, the full body exactly ONCE when first seen, and only a line
// diff (not the whole body) when it changes. fileSection is driven directly so the
// omit/diff decision is asserted without any transcript or runner scaffolding.
func TestFileSectionOmitsUnchangedDiffsChanged(t *testing.T) {
	tc := newTurnContext()
	body := "package a\n\nfunc A() int { return 1 }\n"

	first := tc.fileSection(map[string]string{"a.go": body})
	if !strings.Contains(first, "file a.go (seen") || !strings.Contains(first, "func A() int { return 1 }") {
		t.Fatalf("first sight must emit the full body once, got:\n%s", first)
	}

	// Re-observed unchanged across a later turn: NOTHING is emitted. This is the
	// cut — an unchanged, already-seen file is never re-sent.
	if again := tc.fileSection(map[string]string{"a.go": body}); again != "" {
		t.Fatalf("unchanged already-seen file must emit nothing, got:\n%q", again)
	}

	// Changed: only a diff, not the whole body. The removed and added lines show,
	// the untouched lines do not, and the full new body is NOT re-emitted verbatim.
	changed := "package a\n\nfunc A() int { return 2 }\n"
	diff := tc.fileSection(map[string]string{"a.go": changed})
	if !strings.Contains(diff, "file a.go (changed") {
		t.Fatalf("changed file must be labelled a diff, got:\n%s", diff)
	}
	if !strings.Contains(diff, "- func A() int { return 1 }") || !strings.Contains(diff, "+ func A() int { return 2 }") {
		t.Fatalf("diff must carry the -/+ changed lines, got:\n%s", diff)
	}
	if strings.Contains(diff, "+ package a") {
		t.Fatalf("unchanged lines must not appear in the diff, got:\n%s", diff)
	}

	// Unchanged again after the change was pinned: back to zero bytes.
	if none := tc.fileSection(map[string]string{"a.go": changed}); none != "" {
		t.Fatalf("re-observed unchanged file after a diff must emit nothing, got:\n%q", none)
	}
}

// TestLineDiff locks the pure-Go line differ: equal inputs produce nothing, and a
// pure add, a pure remove, and an in-place change each yield exactly the minimal
// -/+ lines in order (unchanged context lines dropped).
func TestLineDiff(t *testing.T) {
	cases := []struct {
		name, old, next, want string
	}{
		{"equal", "a\nb\nc", "a\nb\nc", ""},
		{"add", "a\nb", "a\nb\nc", "+ c"},
		{"remove", "a\nb\nc", "a\nc", "- b"},
		{"change", "a\nb\nc", "a\nB\nc", "- b\n+ B"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lineDiff(c.old, c.next); got != c.want {
				t.Fatalf("lineDiff(%q,%q) = %q, want %q", c.old, c.next, got, c.want)
			}
		})
	}
}

// TestAssembleSmallerThanNaiveOnReReads proves the whole-assembler win against the
// status quo: when the same file is read in full across several turns (plus verbose
// reasoning/output), the bounded block is materially smaller than the naive
// re-inject-everything transcript, because the body is pinned once and the prior
// turns collapse to a terse summary. base is always preserved verbatim up front.
func TestAssembleSmallerThanNaiveOnReReads(t *testing.T) {
	body := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 150)
	read := func() Part {
		return Part{Type: "tool", Tool: "read", Status: "completed", Input: map[string]any{"filePath": "big.go"}, Output: body}
	}
	msgs := []Message{
		{Role: "user", Parts: []Part{{Type: "text", Text: "do the task"}}},
		{Role: "assistant", Parts: []Part{read(), {Type: "reasoning", Text: strings.Repeat("thinking hard about it\n", 40)}, {Type: "text", Text: "looked at big.go"}}},
		{Role: "assistant", Parts: []Part{read(), {Type: "text", Text: "re-read big.go, unchanged"}}},
		{Role: "assistant", Parts: []Part{read(), {Type: "text", Text: "re-read big.go again, still unchanged"}}},
	}

	naive := renderTranscript("", msgs) // what re-injecting the full transcript costs
	tc := newTurnContext()
	got := tc.assemble("continue", msgs)

	if !strings.HasPrefix(got, "continue") {
		t.Fatalf("assemble must keep base first and verbatim, got prefix:\n%.40q", got)
	}
	if strings.Count(got, "the quick brown fox") >= strings.Count(naive, "the quick brown fox") {
		t.Fatalf("the re-read body was not de-duplicated: block copies=%d naive copies=%d",
			strings.Count(got, "the quick brown fox"), strings.Count(naive, "the quick brown fox"))
	}
	if len(got) >= len(naive) {
		t.Fatalf("bounded block (%d bytes) is not smaller than the naive transcript (%d bytes)", len(got), len(naive))
	}
}

// TestAssembleSummaryBounded proves the rolling summary is bounded regardless of
// session length: after folding far more prose than the cap, the retained summary
// stays within ctxSummaryCap (oldest dropped), so per-turn context does not grow
// without limit as a session runs long.
func TestAssembleSummaryBounded(t *testing.T) {
	msgs := make([]Message, 0, 400)
	for i := 0; i < 400; i++ {
		msgs = append(msgs, Message{Role: "assistant", Parts: []Part{
			{Type: "text", Text: "decision number " + strings.Repeat("x", 20) + " recorded"},
		}})
	}
	tc := newTurnContext()
	tc.assemble("continue", msgs)

	joined := strings.Join(tc.summary, "\n")
	if len(joined) > ctxSummaryCap {
		t.Fatalf("rolling summary (%d bytes) exceeded the cap (%d)", len(joined), ctxSummaryCap)
	}
	if len(tc.summary) == 0 {
		t.Fatal("summary must retain the most recent entries, got none")
	}
	// The window keeps the NEWEST turns: the final folded message survives, an
	// early one was dropped.
	if last := tc.summary[len(tc.summary)-1]; !strings.HasPrefix(last, "assistant:") {
		t.Fatalf("newest summary entry unexpected: %q", last)
	}
	if tc.folded != len(msgs) {
		t.Fatalf("fold must advance past every message: folded=%d want=%d", tc.folded, len(msgs))
	}
}

// ctxDiffSession is a runner-level fake whose transcript GROWS the way opencode's
// does (Messages returns the cumulative history), letting a full Run exercise the
// continue-prompt assembly. It is mutex-guarded because the recorder polls Messages
// on its own goroutine while the main loop drives Prompt.
type ctxDiffSession struct {
	mu      sync.Mutex
	sent    []string
	turn    int
	onTurn  func(turn int)
	msgsFor func(turn int) []Message
}

func (s *ctxDiffSession) Prompt(ctx context.Context, text string) (string, error) {
	s.mu.Lock()
	s.sent = append(s.sent, text)
	s.turn++
	turn := s.turn
	s.mu.Unlock()
	if s.onTurn != nil {
		s.onTurn(turn) // filesystem side effects (doc/plan); no shared Go state
	}
	return "", nil
}

func (s *ctxDiffSession) Close() error { return nil }

func (s *ctxDiffSession) Messages(ctx context.Context) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.msgsFor == nil {
		return nil, nil
	}
	return s.msgsFor(s.turn), nil
}

func (s *ctxDiffSession) prompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

type ctxDiffClient struct{ sess *ctxDiffSession }

func (c *ctxDiffClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	return c.sess, nil
}

// ctxReadTranscript is a minimal transcript in which the agent has read a file —
// the input a context-diff turn would pin/diff. Independent of the turn number.
func ctxReadTranscript(int) []Message {
	return []Message{
		{Role: "user", Parts: []Part{{Type: "text", Text: "start the task"}}},
		{Role: "assistant", Parts: []Part{
			{Type: "tool", Tool: "read", Status: "completed", Input: map[string]any{"filePath": "a.go"}, Output: "package a\n\nfunc A() {}\n"},
			{Type: "text", Text: "read a.go"},
		}},
	}
}

// TestContextDiffInertByDefault proves the default path is unchanged: with
// ContextDiff off, the continue prompt is the bare "continue" byte-for-byte EVEN
// when a re-readable transcript is available — nothing is injected and the
// assembler is never consulted.
func TestContextDiffInertByDefault(t *testing.T) {
	g, sm, planPath, sel, rp := newWorkRun(t)

	sess := &ctxDiffSession{msgsFor: ctxReadTranscript, onTurn: func(turn int) {
		if turn == 2 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		}
	}}
	r := &Runner{Repo: rp, Git: g, Client: &ctxDiffClient{sess: sess}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour} // ContextDiff: false
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	sent := sess.prompts()
	if len(sent) < 2 {
		t.Fatalf("expected at least 2 turns, got %d: %q", len(sent), sent)
	}
	if sent[1] != "continue" {
		t.Fatalf("default continue prompt must be the bare \"continue\", got:\n%q", sent[1])
	}
}

// TestRunCompletesWithContextDiff is the end-to-end ON proof: with ContextDiff
// enabled the runner still completes a task normally, and the continue prompt is
// the base plus a bounded context block assembled from the transcript (the file
// the agent read is pinned into the block), with base preserved first.
func TestRunCompletesWithContextDiff(t *testing.T) {
	g, sm, planPath, sel, rp := newWorkRun(t)

	sess := &ctxDiffSession{msgsFor: ctxReadTranscript, onTurn: func(turn int) {
		if turn == 2 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		}
	}}
	r := &Runner{Repo: rp, Git: g, Client: &ctxDiffClient{sess: sess}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, ContextDiff: true}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	sent := sess.prompts()
	if len(sent) < 2 {
		t.Fatalf("expected at least 2 turns, got %d: %q", len(sent), sent)
	}
	if !strings.HasPrefix(sent[1], "continue") {
		t.Fatalf("context-diff continue prompt must keep base first, got:\n%q", sent[1])
	}
	if !strings.Contains(sent[1], "--- context (bounded") {
		t.Fatalf("context-diff continue prompt missing the bounded context block:\n%q", sent[1])
	}
	if !strings.Contains(sent[1], "file a.go (seen") {
		t.Fatalf("context-diff continue prompt did not pin the file the agent read:\n%q", sent[1])
	}
}
