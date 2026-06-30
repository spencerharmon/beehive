package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
