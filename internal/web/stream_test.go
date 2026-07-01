package web

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/repo"
)

// gitAt runs a git command in dir, failing the test on error. Streaming tests
// stand up real session branches (a honeybee's isolated transcript branch) so
// the SSE handler exercises the same git-backed read the poll uses.
func gitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}

// TestSessionStreamEndsForFinishedSession locks the SSE end contract: a finished
// (non-stub) session streams its durable transcript as data: frames and then a
// single `event: end`, telling the browser to stop streaming and do one
// authoritative poll. The handler returns promptly (an ended session is not held
// open), so a buffered ResponseRecorder captures the whole exchange.
func TestSessionStreamEndsForFinishedSession(t *testing.T) {
	s, root := setup(t)
	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real (non-stub) final transcript: an ended session.
	if err := os.WriteFile(filepath.Join(sessDir, "bee-final.md"), []byte("# final transcript\nall done.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := get(t, s, "/submodule/alpha/session/bee-final/stream")
	if w.Code != 200 {
		t.Fatalf("stream status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	body := w.Body.String()
	// The transcript is delivered line-per-data-field (rejoined by the browser).
	if !strings.Contains(body, "data: # final transcript") || !strings.Contains(body, "data: all done.") {
		t.Errorf("stream did not carry the transcript as data frames:\n%s", body)
	}
	// The end event tells the client to stop streaming and poll the final once.
	if !strings.Contains(body, "event: end") {
		t.Errorf("ended session must emit an `event: end`, got:\n%s", body)
	}
}

// TestSessionStreamUnsupportedFallsBack locks the graceful degradation: when the
// ResponseWriter cannot flush (no streaming transport) the handler reports 500
// "streaming unsupported" instead of buffering forever, so the browser keeps
// polling. The flusher check precedes any transcript read, so no session file is
// needed — only a valid submodule and branch.
func TestSessionStreamUnsupportedFallsBack(t *testing.T) {
	s, _ := setup(t)
	req := httptest.NewRequest("GET", "/submodule/alpha/session/bee-x/stream", nil)
	req.SetPathValue("name", "alpha")
	req.SetPathValue("branch", "bee-x")
	rw := &noFlushRecorder{}
	s.sessionStream(rw, req)
	if rw.code != http.StatusInternalServerError {
		t.Fatalf("no-flush writer: status %d, want 500", rw.code)
	}
	if !strings.Contains(rw.buf.String(), "streaming unsupported") {
		t.Errorf("want a 'streaming unsupported' body, got: %q", rw.buf.String())
	}
}

// noFlushRecorder is a ResponseWriter that deliberately does NOT implement
// http.Flusher, standing in for a transport that cannot stream (the fallback the
// handler must detect). httptest.ResponseRecorder DOES implement Flusher, so it
// can't cover this path.
type noFlushRecorder struct {
	hdr  http.Header
	code int
	buf  bytes.Buffer
}

func (n *noFlushRecorder) Header() http.Header {
	if n.hdr == nil {
		n.hdr = http.Header{}
	}
	return n.hdr
}
func (n *noFlushRecorder) Write(b []byte) (int, error) { return n.buf.Write(b) }
func (n *noFlushRecorder) WriteHeader(c int)           { n.code = c }

// TestSessionStreamLiveStreamsAndCancels is the core streaming test: a running
// session (a stub on main naming a live branch that carries the transcript)
// streams its transcript over SSE, pushes a NEW frame when the branch advances
// (proving it re-reads on cadence and sends on change, not just once), and — the
// leak guard — returns promptly when the client disconnects (request context
// cancelled) rather than spinning a goroutine forever. It uses a real server
// because the stream is held open, which a buffered recorder cannot represent.
func TestSessionStreamLiveStreamsAndCancels(t *testing.T) {
	s, root := setup(t)
	s.streamInterval = 20 * time.Millisecond // re-read fast so the test stays quick
	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")
	sessFile := filepath.Join(sessDir, "bee-live.md")
	sessRel := "submodules/alpha/sessions/bee-live.md"
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Base commit to branch from (commits setup's ROI/PLAN scaffolding).
	gitAt(t, root, "add", "-A")
	gitAt(t, root, "commit", "-q", "-m", "seed")
	// Put the transcript's first version on an isolated stream branch, exactly as
	// a honeybee does: commit the transcript, branch it, then rewind main so main
	// carries only the stub. The server resolves the stub -> branch -> file.
	if err := os.WriteFile(sessFile, []byte("live transcript v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, root, "add", "-A")
	gitAt(t, root, "commit", "-q", "-m", "v1")
	gitAt(t, root, "branch", "bee-live-stream")
	gitAt(t, root, "reset", "-q", "--hard", "HEAD~1")
	// Main now carries the stub naming the live branch (untracked working file is
	// fine; the handler reads it directly, like the poll does). The reset dropped
	// the now-empty sessions dir with the tracked file, so recreate it.
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessFile, []byte(repo.SessionStub("bee-live-stream")), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wrap the real handler so the test can observe when it RETURNS, not merely
	// when the client's read unblocks: a disconnected client read errors whether
	// or not the server goroutine actually stopped, so closing handlerDone in a
	// deferred wrapper is what genuinely proves no server-side leak on cancel.
	handlerDone := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /submodule/{name}/session/{branch}/stream", func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		s.sessionStream(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/submodule/alpha/session/bee-live/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Read frames off the wire in the background so the test can bound every wait
	// (a blocked read must never hang the suite). On any read error the reader
	// closes lines so waiters observe the stream ending.
	lines := make(chan string, 512)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			ln, err := br.ReadString('\n')
			if ln != "" {
				lines <- ln
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	waitLine := func(substr string) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case ln, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed before seeing %q", substr)
				}
				if strings.Contains(ln, substr) {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %q", substr)
			}
		}
	}

	// First frame: the current transcript.
	waitLine("data: live transcript v1")

	// Advance the live branch (a fresh honeybee commit) via a linked worktree —
	// the branch ref moves in the shared repo, so the server's next re-read sees
	// it. This proves the stream pushes CHANGES, not just the opening snapshot.
	wt := filepath.Join(t.TempDir(), "streamwt")
	gitAt(t, root, "worktree", "add", "-q", wt, "bee-live-stream")
	if err := os.WriteFile(filepath.Join(wt, filepath.FromSlash(sessRel)), []byte("live transcript v1\nv2 more output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, wt, "add", "-A")
	gitAt(t, wt, "commit", "-q", "-m", "v2")
	waitLine("data: v2 more output")

	// Leak guard: disconnecting (cancelling the request) must make the SERVER
	// handler return promptly. handlerDone closing is the direct proof it stopped
	// (not just that the client read unblocked), so a handler that ignored ctx and
	// spun forever would fail here.
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return within 3s after the client disconnected — goroutine leak")
	}
}

// TestSessionViewStreamWiring locks the frontend contract that makes the SSE an
// interchangeable overlay on the poll: a LIVE session page opens an EventSource
// to the stream endpoint and cancels the htmx poll while it is connected; an
// ENDED session page ships no stream client (the poll alone renders the final).
func TestSessionViewStreamWiring(t *testing.T) {
	s, root := setup(t)
	ctx := context.Background()
	gitAt(t, root, "add", "-A")
	gitAt(t, root, "commit", "-q", "-m", "seed")
	gitAt(t, root, "branch", "bee-live-stream")
	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "bee-live.md"), []byte(repo.SessionStub("bee-live-stream")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "bee-final.md"), []byte("# final\ndone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Guard the premise: bee-live must actually be live, bee-final ended.
	if !s.sessionLive(ctx, sessDir, "bee-live") {
		t.Fatal("bee-live not live — test premise broken")
	}
	if s.sessionLive(ctx, sessDir, "bee-final") {
		t.Fatal("bee-final should be ended — test premise broken")
	}

	live := get(t, s, "/submodule/alpha/session/bee-live").Body.String()
	if !strings.Contains(live, "new EventSource(") ||
		!strings.Contains(live, "/submodule/alpha/session/bee-live/stream") {
		t.Errorf("live session page must open an EventSource to the stream endpoint:\n%s", live)
	}
	if !strings.Contains(live, "htmx:beforeRequest") || !strings.Contains(live, "__sseLive") {
		t.Errorf("live session page must cancel the htmx poll while streaming:\n%s", live)
	}

	ended := get(t, s, "/submodule/alpha/session/bee-final").Body.String()
	if strings.Contains(ended, "EventSource") {
		t.Errorf("ended session page must not ship a stream client (poll only):\n%s", ended)
	}
}
