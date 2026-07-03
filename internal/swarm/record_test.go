package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubSession returns a scripted, growing message list so the recorder renders a
// changing transcript across snapshots.
type stubSession struct{ msgs func() []Message }

func (s *stubSession) Prompt(ctx context.Context, text string) (string, error) { return "", nil }
func (s *stubSession) Messages(ctx context.Context) ([]Message, error)         { return s.msgs(), nil }
func (s *stubSession) Close() error                                            { return nil }

// TestRecorderStreamsLiveFile proves the recorder writes the rendered transcript
// to its live file on every change (real-time streaming beehived reads off disk)
// and skips rewriting when the transcript is unchanged. There is no per-poll git
// commit anymore — durability is a single end-of-session commit by the runner.
func TestRecorderStreamsLiveFile(t *testing.T) {
	dir := t.TempDir()
	text := "one"
	sess := &stubSession{msgs: func() []Message {
		return []Message{{ID: "m1", Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}}
	}}
	rc := &recorder{
		sess:    sess,
		path:    filepath.Join(dir, "s.md"),
		header:  "# s\n",
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	ctx := context.Background()

	rc.snapshot(ctx) // first content -> writes the live file
	b, err := os.ReadFile(rc.path)
	if err != nil {
		t.Fatalf("session file not written: %v", err)
	}
	if !contains(string(b), "one") {
		t.Fatalf("live file missing first content: %q", b)
	}
	mod1 := statModTime(t, rc.path)

	rc.snapshot(ctx) // identical transcript -> no rewrite (mtime unchanged)
	if got := statModTime(t, rc.path); got != mod1 {
		t.Fatalf("unchanged transcript rewrote the file (mtime changed)")
	}

	text = "one two" // transcript grows -> live file updates
	rc.snapshot(ctx)
	b, _ = os.ReadFile(rc.path)
	if !contains(string(b), "one two") {
		t.Fatalf("live file not updated on change: %q", b)
	}
}

func statModTime(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.ModTime().UnixNano()
}

// TestRecorderCoalescesCommits is the producer half of remote-host-session-view:
// while a (remote) honeybee streams its transcript, the recorder must NOT commit
// every changed poll — it coalesces them to commitIvl so a distributed hive does
// not spam origin with a commit+push per 700ms tick. Many transcript changes
// within one interval collapse to a single commit; a change after the interval
// commits again. Deterministic: the interval boundary is crossed by rewinding
// lastCommit, never by sleeping.
func TestRecorderCoalescesCommits(t *testing.T) {
	dir := t.TempDir()
	text := "a"
	sess := &stubSession{msgs: func() []Message {
		return []Message{{ID: "m1", Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}}
	}}
	commits := 0
	rc := &recorder{
		sess:      sess,
		path:      filepath.Join(dir, "s.md"),
		header:    "# s\n",
		toolSt:    map[string]string{},
		partLen:   map[string]int{},
		started:   map[string]bool{},
		commit:    func(context.Context) { commits++ },
		commitIvl: time.Hour, // one wide interval for the whole test
	}
	ctx := context.Background()

	// First change: lastCommit is zero -> commit fires.
	rc.snapshot(ctx)
	if commits != 1 {
		t.Fatalf("first change: commits=%d, want 1", commits)
	}
	// Several more changes inside the interval: each writes the live file but the
	// commit is throttled -> still exactly one commit (coalesced).
	for i := 0; i < 5; i++ {
		text += "-x"
		rc.snapshot(ctx)
	}
	if commits != 1 {
		t.Fatalf("changes within one interval must coalesce: commits=%d, want 1", commits)
	}
	// Cross the interval boundary -> the next change commits again.
	rc.lastCommit = time.Now().Add(-2 * time.Hour)
	text += "-y"
	rc.snapshot(ctx)
	if commits != 2 {
		t.Fatalf("change after the interval must commit: commits=%d, want 2", commits)
	}
	// An unchanged transcript never commits, even past the interval (no churn on
	// a quiet turn).
	rc.lastCommit = time.Now().Add(-2 * time.Hour)
	rc.snapshot(ctx)
	if commits != 2 {
		t.Fatalf("unchanged transcript must not commit: commits=%d, want 2", commits)
	}
}
