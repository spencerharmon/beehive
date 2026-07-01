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

// TestRecorderCoalescesCommitsPerInterval locks the "commits coalesced per
// interval" contract remote-host-session-view depends on: a beehived following an
// off-box run must be fed at most one stream commit per commitIvl, and only when
// the transcript actually changed — never one commit per poll, never an empty
// commit for an unchanged transcript. A controlled clock makes the throttle
// deterministic (no wall-clock flakiness).
func TestRecorderCoalescesCommitsPerInterval(t *testing.T) {
	dir := t.TempDir()
	text := "one"
	sess := &stubSession{msgs: func() []Message {
		return []Message{{ID: "m1", Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}}
	}}
	clock := time.Unix(1000, 0)
	commits := 0
	rc := &recorder{
		sess:      sess,
		path:      filepath.Join(dir, "s.md"),
		header:    "# s\n",
		commit:    func(context.Context) { commits++ },
		commitIvl: time.Second,
		now:       func() time.Time { return clock },
		toolSt:    map[string]string{},
		partLen:   map[string]int{},
		started:   map[string]bool{},
	}
	ctx := context.Background()

	// First change ever streams one commit (lastCommit is zero == "commit now").
	rc.snapshot(ctx)
	if commits != 1 {
		t.Fatalf("first change: commits=%d, want 1", commits)
	}
	// Two further changes within the same interval coalesce into no new commit.
	text = "one two"
	rc.snapshot(ctx)
	text = "one two three"
	rc.snapshot(ctx)
	if commits != 1 {
		t.Fatalf("within interval: commits=%d, want 1 (coalesced)", commits)
	}
	// The interval elapses and the transcript changed: exactly one more commit.
	clock = clock.Add(time.Second)
	text = "one two three four"
	rc.snapshot(ctx)
	if commits != 2 {
		t.Fatalf("after interval + change: commits=%d, want 2", commits)
	}
	// The interval elapses but the transcript is unchanged: no empty commit.
	clock = clock.Add(10 * time.Second)
	rc.snapshot(ctx)
	if commits != 2 {
		t.Fatalf("after interval, unchanged: commits=%d, want 2 (no empty commit)", commits)
	}
}
