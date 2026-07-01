package swarm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// TestWithModelNoop proves the tiering no-op contract: WithModel("") and
// WithModel(current) return the SAME client (no allocation, no mutation), so a
// host with no per-kind route keeps using exactly one client value.
func TestWithModelNoop(t *testing.T) {
	oc := &Opencode{Base: "http://x", Model: "strong/model", Temperature: 0.4, MaxTokens: 99}
	for _, m := range []string{"", "strong/model"} {
		got, ok := oc.WithModel(m).(*Opencode)
		if !ok || got != oc {
			t.Fatalf("WithModel(%q) = %v (ok=%v); want the same *Opencode receiver", m, got, ok)
		}
	}
}

// TestWithModelTiers proves WithModel returns an independent copy pinned to the
// tiered model while leaving the receiver's Model (and every other knob) intact,
// and that a Prompt on the tiered client sends the tiered model to the backend.
func TestWithModelTiers(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "strong/model", Temperature: 0.4, MaxTokens: 99, HTTP: srv.Client()}
	cheap, ok := oc.WithModel("cheap/mini").(*Opencode)
	if !ok {
		t.Fatal("WithModel did not return *Opencode")
	}
	if cheap == oc {
		t.Fatal("WithModel returned the receiver; a real retier must be a copy")
	}
	if oc.Model != "strong/model" {
		t.Fatalf("receiver Model mutated to %q; WithModel must not touch the receiver", oc.Model)
	}
	if cheap.Model != "cheap/mini" {
		t.Fatalf("tiered client Model = %q, want cheap/mini", cheap.Model)
	}
	// Other knobs carry over unchanged into the copy.
	if cheap.Temperature != 0.4 || cheap.MaxTokens != 99 || cheap.Base != oc.Base {
		t.Fatalf("tiered copy dropped knobs: %+v", cheap)
	}
	// The tiered model actually reaches the request body.
	s := &ocSession{oc: cheap, id: "s1", dir: "/wt", system: "sys"}
	if _, err := s.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	model, _ := body["model"].(map[string]any)
	if model["providerID"] != "cheap" || model["modelID"] != "mini" {
		t.Fatalf("request model = %v, want cheap/mini split", body["model"])
	}
}
