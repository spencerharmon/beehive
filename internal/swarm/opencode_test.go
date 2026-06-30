package swarm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureBody returns an httptest server that records the JSON body of the last
// POST to /session/<id>/message and replies with a single text part. The reply
// carries info.time.completed, marking the turn already finished, so Prompt takes
// the synchronous short-circuit and does not poll (these tests assert request-
// body threading, not the turn-idle wait — that is TestPromptWaitsForTurnIdle).
func captureBody(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Errorf("server: bad json body: %v", err)
		}
		*got = m
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"id":"a1","time":{"completed":1700000000000}},"parts":[{"type":"text","text":"ok"}]}`))
	}))
}

// TestPromptThreadsModelKnobs proves the resolved temperature/max-tokens config
// is threaded into the opencode request body when set.
func TestPromptThreadsModelKnobs(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", Temperature: 0.42, MaxTokens: 1234, HTTP: srv.Client()}
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	reply, err := s.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q, want ok", reply)
	}
	if v, ok := body["temperature"].(float64); !ok || v != 0.42 {
		t.Errorf("body temperature = %v (ok=%v), want 0.42", body["temperature"], ok)
	}
	if v, ok := body["maxTokens"].(float64); !ok || v != 1234 {
		t.Errorf("body maxTokens = %v (ok=%v), want 1234", body["maxTokens"], ok)
	}
	// Model still split into providerID/modelID as before.
	model, _ := body["model"].(map[string]any)
	if model["providerID"] != "anthropic" || model["modelID"] != "claude-3" {
		t.Errorf("body model = %v, want anthropic/claude-3 split", body["model"])
	}
}

// TestPromptOmitsUnsetKnobs proves an unset (zero) knob is omitted entirely, so
// the request stays byte-identical to the pre-feature default path and the
// backend's own default applies.
func TestPromptOmitsUnsetKnobs(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", HTTP: srv.Client()} // Temperature/MaxTokens zero
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	if _, err := s.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if _, present := body["temperature"]; present {
		t.Errorf("temperature present in body but should be omitted when unset: %v", body["temperature"])
	}
	if _, present := body["maxTokens"]; present {
		t.Errorf("maxTokens present in body but should be omitted when unset: %v", body["maxTokens"])
	}
}

// TestPromptWaitsForTurnIdle proves the turn-idle poll: opencode's POST accepts a
// turn WITHOUT finishing it (the reply has no info.time.completed), so Prompt must
// poll GET /session/<id>/message until the assistant message reports completed and
// only then return its settled text. A premature return is the fire-and-forget bug
// that let the runner's completion check run mid-turn (every turn "done" in ms).
func TestPromptWaitsForTurnIdle(t *testing.T) {
	const idleOnPoll = 3 // the assistant message goes completed on the 3rd GET poll
	var mu sync.Mutex
	var posts, polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			mu.Lock()
			posts++
			mu.Unlock()
			// Accept only: echo the assistant stub with NO completed timestamp.
			_, _ = w.Write([]byte(`{"info":{"id":"a1","time":{}},"parts":[]}`))
			return
		}
		// GET /session/<id>/message poll.
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		if n < idleOnPoll {
			// Busy: the assistant message exists but is still in flight (no completed).
			_, _ = w.Write([]byte(`[{"info":{"id":"a1","role":"assistant","time":{}},` +
				`"parts":[{"type":"text","text":"partial"}]}]`))
			return
		}
		// Idle: the turn finished, with its final text.
		_, _ = w.Write([]byte(`[{"info":{"id":"a1","role":"assistant","time":{"completed":1700000000000}},` +
			`"parts":[{"type":"text","text":"final answer"}]}]`))
	}))
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", HTTP: srv.Client()}
	oc.pollMin, oc.pollMax = time.Millisecond, 2*time.Millisecond // keep the wait loop fast
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	reply, err := s.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	// The settled (completed) text, not the in-flight "partial".
	if reply != "final answer" {
		t.Fatalf("reply = %q, want the settled idle text %q", reply, "final answer")
	}
	mu.Lock()
	gotPosts, gotPolls := posts, polls
	mu.Unlock()
	if gotPosts != 1 {
		t.Fatalf("posts = %d, want exactly one accept POST", gotPosts)
	}
	// It must have blocked across the busy polls until the idle one (>= idleOnPoll),
	// not returned on the first poll — that is the no-premature-completion guarantee.
	if gotPolls < idleOnPoll {
		t.Fatalf("polls = %d; Prompt returned before the idle poll (%d) — it did not wait for turn idle", gotPolls, idleOnPoll)
	}
}

// TestPromptIdlePollHonorsCancel proves a turn that never settles is bounded by
// ctx (the runner's per-turn timeout / WallCap): the poll loop returns the ctx
// error instead of spinning forever, and surfaces it rather than swallowing it.
func TestPromptIdlePollHonorsCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"info":{"id":"a1","time":{}},"parts":[]}`))
			return
		}
		// Always busy: the turn never completes.
		_, _ = w.Write([]byte(`[{"info":{"id":"a1","role":"assistant","time":{}},"parts":[]}]`))
	}))
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", HTTP: srv.Client()}
	oc.pollMin, oc.pollMax = time.Millisecond, 2*time.Millisecond
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Prompt(ctx, "hi"); err == nil {
		t.Fatal("Prompt must surface the ctx cancellation of a never-idle turn, got nil error")
	}
}

// sseFrame formats one opencode /event SSE frame: a single data line carrying the
// compact JSON event, terminated by the blank line that ends an SSE event.
func sseFrame(json string) string { return "data: " + json + "\n\n" }

// TestStreamPromptAssemblesDeltas is the core of the streaming read path: with
// Stream set, Prompt subscribes to /event, and opencode's CUMULATIVE part-text
// updates are surfaced as incremental suffix deltas via OnDelta while the full
// reply assembles to the final text. session.idle ends the turn (stream-end ==
// turn-idle), exactly like the poll path's completed marker.
func TestStreamPromptAssemblesDeltas(t *testing.T) {
	frames := []string{
		`{"type":"message.part.updated","properties":{"part":{"id":"p1","sessionID":"sess1","messageID":"a1","type":"text","text":"Hello"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"p1","sessionID":"sess1","messageID":"a1","type":"text","text":"Hello, "}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"p1","sessionID":"sess1","messageID":"a1","type":"text","text":"Hello, world"}}}`,
		// A part from ANOTHER session must be ignored (the bus is global).
		`{"type":"message.part.updated","properties":{"part":{"id":"x9","sessionID":"other","messageID":"z","type":"text","text":"leak"}}}`,
		`{"type":"session.idle","properties":{"sessionID":"sess1"}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/event" {
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			for _, f := range frames {
				_, _ = io.WriteString(w, sseFrame(f))
				if fl != nil {
					fl.Flush()
				}
			}
			return
		}
		// Accept the turn fire-and-forget (no completed): the stream drives idle.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"id":"a1","time":{}},"parts":[]}`))
	}))
	defer srv.Close()

	var deltas []string
	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", HTTP: srv.Client(), Stream: true,
		OnDelta: func(sess, d string) {
			if sess != "sess1" {
				t.Errorf("OnDelta session = %q, want sess1", sess)
			}
			deltas = append(deltas, d)
		}}
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	reply, err := s.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Hello, world" {
		t.Fatalf("reply = %q, want the assembled %q", reply, "Hello, world")
	}
	// Deltas are the incremental suffixes, not one lump, and concatenate to the full text.
	want := []string{"Hello", ", ", "world"}
	if len(deltas) != len(want) {
		t.Fatalf("deltas = %v, want %v (the cross-session frame must be ignored)", deltas, want)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Fatalf("delta[%d] = %q, want %q (deltas=%v)", i, deltas[i], want[i], deltas)
		}
	}
	if got := strings.Join(deltas, ""); got != reply {
		t.Fatalf("deltas concatenated = %q, want to equal the reply %q", got, reply)
	}
}

// TestStreamPromptFallsBackToPoll proves graceful degradation: a server with no
// /event stream (404) makes Stream-enabled Prompt fall back to the proven
// post+poll turn-idle path and still return the settled text — and it posts the
// turn EXACTLY ONCE (openEvents runs before the post, so the abandoned stream
// attempt never double-drives the turn).
func TestStreamPromptFallsBackToPoll(t *testing.T) {
	var mu sync.Mutex
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/event" {
			http.Error(w, "no event stream on this server", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			mu.Lock()
			posts++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"info":{"id":"a1","time":{}},"parts":[]}`))
			return
		}
		// Poll: the turn is already idle with its final text.
		_, _ = w.Write([]byte(`[{"info":{"id":"a1","role":"assistant","time":{"completed":1700000000000}},` +
			`"parts":[{"type":"text","text":"final answer"}]}]`))
	}))
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", HTTP: srv.Client(), Stream: true}
	oc.pollMin, oc.pollMax = time.Millisecond, 2*time.Millisecond
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	reply, err := s.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("a non-streaming server must fall back without error, got %v", err)
	}
	if reply != "final answer" {
		t.Fatalf("reply = %q, want %q from the poll fallback", reply, "final answer")
	}
	mu.Lock()
	gotPosts := posts
	mu.Unlock()
	if gotPosts != 1 {
		t.Fatalf("posts = %d, want exactly one — the stream fallback must not double-post the turn", gotPosts)
	}
}

// TestStreamPromptHonorsCancel proves a stream that never goes idle is bounded by
// ctx and leaks no goroutines: the reader runs in Prompt's own goroutine, so a
// cancel unblocks the underlying read, surfaces the error, and the goroutine
// count settles back to baseline (no background reader was spawned).
func TestStreamPromptHonorsCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/event" {
			w.Header().Set("Content-Type", "text/event-stream")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush() // send headers so the subscribe returns, then hold open (never idle)
			}
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"id":"a1","time":{}},"parts":[]}`))
	}))
	defer srv.Close()

	// DisableKeepAlives so no idle-connection goroutine lingers to confound the
	// leak check; each request's connection is released when it ends.
	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", Stream: true,
		HTTP: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.Prompt(ctx, "hi")
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let it subscribe, post, and block on the stream
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel mid-stream must surface an error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt hung on the stream after cancel — it did not honor ctx")
	}
	// The streaming path spawns no goroutine of its own, so after teardown the
	// count returns to baseline (give the transport a moment to release the conn).
	settled := false
	for i := 0; i < 100; i++ {
		if runtime.NumGoroutine() <= base+1 {
			settled = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !settled {
		t.Fatalf("goroutines did not settle after cancel (base=%d now=%d): the stream leaked a goroutine",
			base, runtime.NumGoroutine())
	}
}
