package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// sseServer is an httptest server that streams the given opencode events as SSE
// frames (flushing each so the client sees them incrementally) and then holds the
// connection open until the client cancels — mirroring opencode's long-lived
// /event channel. Each event is a map matching the envelope in stream.go.
func sseServer(t *testing.T, events []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("server: ResponseWriter is not a Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		for _, ev := range events {
			b, err := json.Marshal(ev)
			if err != nil {
				t.Errorf("server: marshal event: %v", err)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		<-r.Context().Done() // hold open like the real event channel
	}))
}

func msgUpdated(id, role, sid string) map[string]any {
	return map[string]any{"type": "message.updated", "properties": map[string]any{
		"info": map[string]any{"id": id, "role": role, "sessionID": sid}}}
}

func partUpdated(partID, msgID, sid, text string) map[string]any {
	return map[string]any{"type": "message.part.updated", "properties": map[string]any{
		"part": map[string]any{"id": partID, "messageID": msgID, "sessionID": sid, "type": "text", "text": text}}}
}

func sessionIdle(sid string) map[string]any {
	return map[string]any{"type": "session.idle", "properties": map[string]any{"sessionID": sid}}
}

// TestStreamAssemblesTokensIncrementally proves the streaming read path: a fake
// opencode event channel emits an assistant message then growing text parts, and
// the store assembles them incrementally (an intermediate render carries the
// shorter text, the final render the whole text). session.idle fires onIdle, and
// events for a DIFFERENT session are filtered out.
func TestStreamAssemblesTokensIncrementally(t *testing.T) {
	srv := sseServer(t, []map[string]any{
		msgUpdated("m1", "assistant", "sess1"),
		partUpdated("pX", "mX", "other", "LEAK"), // different session: must be ignored
		partUpdated("p1", "m1", "sess1", "Hello"),
		partUpdated("p1", "m1", "sess1", "Hello, world"), // cumulative replace
		sessionIdle("other"),                             // different session: not our idle
		sessionIdle("sess1"),
	})
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, HTTP: srv.Client()}
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var updates [][]Message
	idle := make(chan struct{}, 1)
	onUpdate := func(msgs []Message) { mu.Lock(); updates = append(updates, msgs); mu.Unlock() }
	onIdle := func() {
		select {
		case idle <- struct{}{}:
		default:
		}
	}

	errc := make(chan error, 1)
	go func() { errc <- s.stream(ctx, onUpdate, onIdle) }()

	select {
	case <-idle:
	case <-time.After(2 * time.Second):
		t.Fatal("never observed session.idle for our session")
	}
	cancel()
	select {
	case <-errc:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return after ctx cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) == 0 {
		t.Fatal("no onUpdate calls")
	}
	// Final assembled transcript: one assistant message reading the whole text.
	last := updates[len(updates)-1]
	if len(last) != 1 || last[0].Role != "assistant" {
		t.Fatalf("final update = %+v, want a single assistant message", last)
	}
	if got := messageText(last[0]); got != "Hello, world" {
		t.Fatalf("assembled text = %q, want %q", got, "Hello, world")
	}
	// Incremental: some earlier render carried only the shorter prefix.
	sawShort := false
	for _, u := range updates {
		if len(u) == 1 && messageText(u[0]) == "Hello" {
			sawShort = true
		}
		for _, m := range u {
			if contains(messageText(m), "LEAK") {
				t.Fatalf("rendered content from a different session: %q", messageText(m))
			}
		}
	}
	if !sawShort {
		t.Fatal("tokens did not assemble incrementally (no intermediate 'Hello' render)")
	}
}

// TestStreamUnsupportedFallsBack proves a server without a usable event channel
// yields ErrNoStream (the caller then polls): both a non-2xx answer and a 200 that
// is not an SSE stream.
func TestStreamUnsupportedFallsBack(t *testing.T) {
	t.Run("non-2xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no such route", http.StatusNotFound)
		}))
		defer srv.Close()
		s := &ocSession{oc: &Opencode{Base: srv.URL, HTTP: srv.Client()}, id: "sess1", dir: "/wt"}
		err := s.stream(context.Background(), func([]Message) {}, func() {})
		if !errors.Is(err, ErrNoStream) {
			t.Fatalf("err = %v, want ErrNoStream", err)
		}
	})
	t.Run("wrong-content-type", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		}))
		defer srv.Close()
		s := &ocSession{oc: &Opencode{Base: srv.URL, HTTP: srv.Client()}, id: "sess1", dir: "/wt"}
		err := s.stream(context.Background(), func([]Message) {}, func() {})
		if !errors.Is(err, ErrNoStream) {
			t.Fatalf("err = %v, want ErrNoStream", err)
		}
	})
}

// TestStreamCtxCancelReturnsPromptly proves cancelling ctx unblocks the streaming
// read and returns promptly. The read runs inline on the caller's goroutine (no
// background reader), so a prompt return is exactly the "no goroutine leak"
// guarantee: nothing outlives ctx.
func TestStreamCtxCancelReturnsPromptly(t *testing.T) {
	var once sync.Once
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer srv.Close()

	s := &ocSession{oc: &Opencode{Base: srv.URL, HTTP: srv.Client()}, id: "sess1", dir: "/wt"}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.stream(ctx, func([]Message) {}, func() {}) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream never connected to the event channel")
	}
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return promptly after ctx cancel (blocked reader leaked)")
	}
}

// streamSession is a Session that also implements streamer, so the recorder takes
// the streaming path. emit is invoked synchronously at the start of stream; when
// err is nil, stream then blocks until ctx is done (mirroring the long-lived event
// channel). Messages returns pollMsgs for the poll fallback.
type streamSession struct {
	emit     func(onUpdate func([]Message), onIdle func())
	err      error
	pollMsgs []Message
}

func (s *streamSession) Prompt(ctx context.Context, text string) (string, error) { return "", nil }
func (s *streamSession) Close() error                                            { return nil }
func (s *streamSession) Messages(ctx context.Context) ([]Message, error)         { return s.pollMsgs, nil }
func (s *streamSession) stream(ctx context.Context, onUpdate func([]Message), onIdle func()) error {
	if s.emit != nil {
		s.emit(onUpdate, onIdle)
	}
	if s.err != nil {
		return s.err
	}
	<-ctx.Done()
	return ctx.Err()
}

// TestRecorderPrefersStream proves the recorder renders from the streaming path
// when the session supports it: streamed content lands in the live file even
// though the poll message list is empty, and a turn going idle forces a commit
// past the (1h) throttle.
func TestRecorderPrefersStream(t *testing.T) {
	dir := t.TempDir()
	sess := &streamSession{
		emit: func(onUpdate func([]Message), onIdle func()) {
			onUpdate([]Message{{ID: "m1", Role: "assistant", Parts: []Part{{ID: "p1", Type: "text", Text: "streamed hello"}}}})
			onIdle()
		},
		// pollMsgs left nil: if the recorder polled instead, the file would be header-only.
	}
	var commits int
	rc := &recorder{
		sess: sess, path: filepath.Join(dir, "s.md"), header: "# s\n",
		toolSt: map[string]string{}, partLen: map[string]int{}, started: map[string]bool{},
		commit: func(context.Context) { commits++ }, commitIvl: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rc.loop(ctx); close(done) }()

	waitForContains(t, rc.path, "streamed hello")
	cancel()
	<-done

	// One render commit (first render, throttle not yet armed) + one idle flush
	// commit (bypasses the 1h throttle). Without the idle flush, commits would be 1.
	if commits < 2 {
		t.Fatalf("commits = %d; want >=2 (render + idle flush past the throttle)", commits)
	}
}

// TestRecorderFallsBackToPollOnErrNoStream proves that when the session reports no
// event channel, the recorder falls back to polling and still streams the
// transcript to the live file.
func TestRecorderFallsBackToPollOnErrNoStream(t *testing.T) {
	dir := t.TempDir()
	sess := &streamSession{
		err:      ErrNoStream,
		pollMsgs: []Message{{ID: "m1", Role: "assistant", Parts: []Part{{ID: "p1", Type: "text", Text: "polled hello"}}}},
	}
	rc := &recorder{
		sess: sess, path: filepath.Join(dir, "s.md"), header: "# s\n",
		toolSt: map[string]string{}, partLen: map[string]int{}, started: map[string]bool{},
		pollIvl: 5 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rc.loop(ctx); close(done) }()

	waitForContains(t, rc.path, "polled hello")
	cancel()
	<-done
}

// waitForContains polls path until it contains sub or the deadline passes.
func waitForContains(t *testing.T, path, sub string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && contains(string(b), sub) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	b, _ := os.ReadFile(path)
	t.Fatalf("timed out waiting for %q in %s; file has: %q", sub, path, b)
}
