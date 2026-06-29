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

func TestRecorderFlushOnChangeThrottled(t *testing.T) {
	dir := t.TempDir()
	text := "one"
	sess := &stubSession{msgs: func() []Message {
		return []Message{{ID: "m1", Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}}
	}}
	var flushes int
	rc := &recorder{
		sess:     sess,
		path:     filepath.Join(dir, "s.md"),
		header:   "# s\n",
		flush:    func(context.Context) { flushes++ },
		flushIvl: 0, // no throttle: every change flushes
		toolSt:   map[string]string{},
		partLen:  map[string]int{},
		started:  map[string]bool{},
	}
	ctx := context.Background()

	rc.snapshot(ctx) // first content -> change -> flush
	if flushes != 1 {
		t.Fatalf("first snapshot flushes=%d want 1", flushes)
	}
	if _, err := os.Stat(rc.path); err != nil {
		t.Fatalf("session file not written: %v", err)
	}
	rc.snapshot(ctx) // identical transcript -> no change -> no flush
	if flushes != 1 {
		t.Fatalf("unchanged snapshot flushes=%d want 1", flushes)
	}
	text = "one two" // transcript grows -> change -> flush
	rc.snapshot(ctx)
	if flushes != 2 {
		t.Fatalf("changed snapshot flushes=%d want 2", flushes)
	}

	// Throttle: with a long interval, a change right after a flush is suppressed.
	rc.flushIvl = time.Hour
	rc.lastFlush = time.Now()
	text = "one two three"
	rc.snapshot(ctx)
	if flushes != 2 {
		t.Fatalf("throttled snapshot flushes=%d want 2", flushes)
	}
}
