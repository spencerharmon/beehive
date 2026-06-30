package swarm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureBody returns an httptest server that records the JSON body of the last
// POST to /session/<id>/message and replies with a single text part.
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
		_, _ = w.Write([]byte(`{"parts":[{"type":"text","text":"ok"}]}`))
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
