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

// TestRecorderCoalescesCommitsPerInterval is the producer half of
// remote-host-session-view: while a remote honeybee runs, the transcript streams
// to the session branch as commits, but a burst of transcript changes inside one
// throttle interval must COALESCE into a single commit (don't spam origin), while
// a change in a later interval commits again. A fake clock makes the throttle
// boundary deterministic (no sleeping on wall time).
func TestRecorderCoalescesCommitsPerInterval(t *testing.T) {
	dir := t.TempDir()
	text := "one"
	sess := &stubSession{msgs: func() []Message {
		return []Message{{ID: "m1", Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}}
	}}
	var commits int
	base := time.Unix(1_700_000_000, 0)
	cur := base
	rc := &recorder{
		sess:      sess,
		path:      filepath.Join(dir, "s.md"),
		header:    "# s\n",
		toolSt:    map[string]string{},
		partLen:   map[string]int{},
		started:   map[string]bool{},
		commit:    func(context.Context) { commits++ },
		commitIvl: time.Second,
		now:       func() time.Time { return cur },
	}
	ctx := context.Background()

	// First change at t0: lastCommit zero -> commit (1).
	rc.snapshot(ctx)
	// Two more DISTINCT transcript changes still inside the first 1s interval:
	// both reach the throttle (md changed) but must be coalesced -> no new commit.
	text = "one two"
	cur = base.Add(300 * time.Millisecond)
	rc.snapshot(ctx)
	text = "one two three"
	cur = base.Add(900 * time.Millisecond)
	rc.snapshot(ctx)
	if commits != 1 {
		t.Fatalf("changes within one interval should coalesce to a single commit, got %d", commits)
	}

	// Cross the interval boundary: the next change commits again (2).
	text = "one two three four"
	cur = base.Add(1100 * time.Millisecond)
	rc.snapshot(ctx)
	if commits != 2 {
		t.Fatalf("a change in the next interval should commit again, got %d", commits)
	}

	// An UNCHANGED transcript never reaches the throttle, even past the interval.
	cur = base.Add(5 * time.Second)
	rc.snapshot(ctx)
	if commits != 2 {
		t.Fatalf("unchanged transcript must not commit, got %d", commits)
	}
}
