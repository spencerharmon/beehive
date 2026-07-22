package swarm

import (
	"context"
	"encoding/json"
	"errors"
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

// TestPromptDisablesQuestionTool proves every turn ships tools={question:false}
// so opencode never offers its interactive elicitation tool to a headless
// honeybee. A `question` call has no attached client to answer it, would block
// the turn until the per-turn timeout, and would discard the pass's work; the
// only sanctioned human escalation is `beehive task human` (NEEDS-HUMAN).
func TestPromptDisablesQuestionTool(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", HTTP: srv.Client()}
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	if _, err := s.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	tools, ok := body["tools"].(map[string]any)
	if !ok {
		t.Fatalf("body tools = %v (%T), want a map disabling question", body["tools"], body["tools"])
	}
	enabled, present := tools["question"]
	if !present {
		t.Fatalf("body tools missing question key: %v", tools)
	}
	if b, _ := enabled.(bool); b {
		t.Errorf("body tools[question] = %v, want false (tool disabled)", enabled)
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

// TestIdleWatchdogAbandonsStuckTurn: a turn whose transcript never advances (the
// message list is byte-identical on every poll) is abandoned with ErrTurnIdle once
// IdleTimeout elapses — the wedged-agent case (dead socket / provider hang) the
// runner reclaims for GC, instead of burning the full absolute ceiling.
func TestIdleWatchdogAbandonsStuckTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"info":{"id":"a1","time":{}},"parts":[]}`))
			return
		}
		// Always the SAME in-flight transcript: no new parts, no completion — the
		// fingerprint never advances, so the idle watchdog must fire.
		_, _ = w.Write([]byte(`[{"info":{"id":"a1","role":"assistant","time":{}},` +
			`"parts":[{"type":"text","text":"thinking"}]}]`))
	}))
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", HTTP: srv.Client(), IdleTimeout: 15 * time.Millisecond}
	oc.pollMin, oc.pollMax = time.Millisecond, 2*time.Millisecond
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	// ctx is far longer than IdleTimeout, so the watchdog (not ctx) must end it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Prompt(ctx, "hi")
	if !errors.Is(err, ErrTurnIdle) {
		t.Fatalf("stuck turn err = %v, want ErrTurnIdle", err)
	}
}

// TestIdleWatchdogSpareProgressingTurn: a turn that keeps ADVANCING its transcript
// (a new part on each poll) past several IdleTimeout windows is never abandoned —
// the productive-long-turn case — and its settled text is returned on completion.
// This is the whole point: distinguish "busy for a long time" from "wedged".
func TestIdleWatchdogSpareProgressingTurn(t *testing.T) {
	const completeOnPoll = 10
	var mu sync.Mutex
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"info":{"id":"a1","time":{}},"parts":[]}`))
			return
		}
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		if n < completeOnPoll {
			// Progress: N tool parts on poll N, so the fingerprint strictly grows
			// each poll and keeps resetting the idle clock — even though each poll is
			// spaced well past IdleTimeout below.
			parts := ""
			for i := 0; i < n; i++ {
				if i > 0 {
					parts += ","
				}
				parts += `{"type":"tool","state":{"status":"completed","output":"step"}}`
			}
			_, _ = w.Write([]byte(`[{"info":{"id":"a1","role":"assistant","time":{}},"parts":[` + parts + `]}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"info":{"id":"a1","role":"assistant","time":{"completed":1700000000000}},` +
			`"parts":[{"type":"text","text":"landed"}]}]`))
	}))
	defer srv.Close()

	// IdleTimeout is several poll intervals long; the turn progresses on EVERY poll
	// (a new part each time), so the idle clock is reset before it can elapse even
	// though the whole turn outlasts one IdleTimeout window. A broken reset would
	// fire ErrTurnIdle before completeOnPoll.
	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", HTTP: srv.Client(), IdleTimeout: 20 * time.Millisecond}
	oc.pollMin, oc.pollMax = 5*time.Millisecond, 5*time.Millisecond
	s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}

	reply, err := s.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("progressing turn wrongly abandoned: %v", err)
	}
	if reply != "landed" {
		t.Fatalf("reply = %q, want the settled text %q", reply, "landed")
	}
}

// TestFingerprintCountsAllAgentSignals proves the liveness fingerprint advances
// on EVERY distinct agent signal — not only text/output/status. Each variant is
// the same base part with exactly ONE additional signal populated (a tool name,
// call id, streamed input arg, error, or human title); every one must yield a
// fingerprint different from the base, so any of them resets the idle watchdog.
func TestFingerprintCountsAllAgentSignals(t *testing.T) {
	// base: a bare in-flight tool part with none of the extra signals set.
	base := `{"type":"tool","state":{"status":"running"}}`
	variants := map[string]string{
		"tool-name":  `{"type":"tool","tool":"bash","state":{"status":"running"}}`,
		"call-id":    `{"type":"tool","callID":"call_abc","state":{"status":"running"}}`,
		"input-arg":  `{"type":"tool","state":{"status":"running","input":{"command":"go test ./..."}}}`,
		"tool-error": `{"type":"tool","state":{"status":"error","error":"boom"}}`,
		"tool-title": `{"type":"tool","state":{"status":"running","title":"Running the suite"}}`,
	}

	fpFor := func(t *testing.T, part string) int64 {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"info":{"id":"a1","role":"assistant","time":{}},"parts":[` + part + `]}]`))
		}))
		defer srv.Close()
		oc := &Opencode{Base: srv.URL, Model: "m", HTTP: srv.Client()}
		s := &ocSession{oc: oc, id: "sess1", dir: "/wt", system: "sys"}
		_, _, fp, err := s.turnPoll(context.Background(), "a1")
		if err != nil {
			t.Fatalf("turnPoll: %v", err)
		}
		return fp
	}

	baseFP := fpFor(t, base)
	for name, part := range variants {
		if got := fpFor(t, part); got == baseFP {
			t.Errorf("signal %q did not advance the fingerprint (fp=%d == base %d); a live agent emitting only this signal would be wrongly abandoned as idle", name, got, baseFP)
		}
	}
}

// TestInputFingerprintOrderIndependent guards the one hazard the input fold could
// introduce: Go map iteration order is random, so the same input map must always
// fold to the same value or a live agent could suffer a false idle-reset churn.
func TestInputFingerprintOrderIndependent(t *testing.T) {
	in := map[string]any{"a": "one", "bb": 22, "ccc": []string{"x", "y"}, "dddd": map[string]any{"k": "v"}}
	want := inputFingerprint(in)
	for i := 0; i < 50; i++ {
		if got := inputFingerprint(in); got != want {
			t.Fatalf("inputFingerprint not stable across iterations: %d != %d", got, want)
		}
	}
	// A longer arg value (streamed input growth) MUST change it.
	if inputFingerprint(map[string]any{"a": "one"}) == inputFingerprint(map[string]any{"a": "one-longer"}) {
		t.Fatal("growing an input value did not change the fingerprint")
	}
}
