package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/repo"
)

// TestChatDiffPollUpdatesIntegration is the end-to-end regression gate for the
// "(Bug, still broken) Chat-diff polling doesn't update — the operator only sees
// changes on a manual refresh" ROI item (chat-diff-poll-update-integration).
//
// It drives a REAL editor session end-to-end through the actual HTTP poll/refresh
// path — GET /edit (open) → GET /editor/{id} (the shell the browser loads) → POST
// /editor/{id}/chat (a turn started exactly as the composer form does, running in
// the background) → GET /editor/{id}/panel (the fragment the shell auto-refreshes)
// — and asserts that a reply and its diff which land AFTER the page is loaded
// appear via the poll path WITHOUT a manual reload.
//
// Why it FAILS against the pre-fix behavior: the refresh used to be re-armed by a
// hidden self-perpetuating node INSIDE the polled panel body (editor_panel.html,
// present only while .Busy), while the shell fetched the panel just ONCE on load
// (hx-trigger="load"). That body-embedded continuation is fatally fragile on the
// real path: (a) idiomorph (hx-swap="morph:innerHTML") PRESERVES the hidden node
// across a swap, so its one-shot `load` never re-fires; and (b) renderConditional
// answers an unchanged tick with "304 Not Modified" and an EMPTY body (proven
// below), which delivers no continuation node at all. Either way the poll dies on
// the first idle/unchanged tick, so a reply that lands a moment later is never
// fetched — the operator must refresh by hand. The fix moves the poll onto the
// PERSISTENT shell node (#editor: hx-trigger="load, every 1500ms"), which is never
// itself swapped, so the recurring timer keeps firing regardless of what any tick
// returns.
//
// The assertion at (A) is the failing gate: pre-fix the shell carries a one-shot
// "load" trigger and no recurring timer, so the diff never auto-updates; post-fix
// it carries the self-sustaining "load, every 1500ms" that makes updates appear
// without a refresh.
func TestChatDiffPollUpdatesIntegration(t *testing.T) {
	const (
		reply  = "applied your change."
		marker = "POLL-UPDATE-MARKER-42"
	)
	file := "submodules/alpha/" + repo.ROIFile

	// A real editor.Session backed by a fake agent transport: the turn appends a
	// line to the coordination file (so a genuine diff is produced) and returns a
	// reply, exactly as a live turn would once the operator sends a message.
	fc := &fakeAgentClient{
		reply: reply,
		editFn: func(cwd string) {
			p := filepath.Join(cwd, filepath.FromSlash(file))
			b, _ := os.ReadFile(p)
			_ = os.WriteFile(p, append(b, []byte(marker+"\n")...), 0o644)
		},
	}
	s, _ := editorFixtureClient(t, fc)
	id := openEditorSession(t, s, file)
	// Drain the background turn before the fixture's TempDir is torn down: Close
	// cancels any in-flight turn and blocks on the worktree lock the turn's commit
	// holds, so no git subprocess is still writing the worktree at cleanup. This
	// cleanup is registered AFTER the fixture's, so it runs BEFORE TempDir removal.
	t.Cleanup(func() { _ = s.editors.Close(context.Background(), id) })

	// (A) FAILING GATE — the page the browser loads must auto-refresh the diff
	// panel on a self-sustaining timer, so a post-load reply/diff appears without a
	// manual reload. Pre-fix this is a one-shot "load" (the diff never updates on
	// its own); post-fix it is the recurring "load, every 1500ms".
	page := get(t, s, "/editor/"+id).Body.String()
	if !strings.Contains(page, `hx-get="/editor/`+id+`/panel"`) {
		t.Fatalf("editor page does not wire the panel poll:\n%s", page)
	}
	if !strings.Contains(page, `hx-trigger="load, every 1500ms"`) {
		t.Fatalf("REGRESSION: the chat-diff editor page does not auto-refresh the panel on a self-sustaining timer, so a reply/diff that lands after load never appears without a manual refresh. Want hx-trigger=\"load, every 1500ms\" on the #editor poll node:\n%s", page)
	}

	// Prove an idle/unchanged panel tick is a 304 with an EMPTY body — the exact
	// condition that dropped the old body-embedded continuation node. The recurring
	// shell trigger above is what survives it.
	first := getPanel(t, s, id, "")
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || etag == "" {
		t.Fatalf("first /panel GET: code=%d etag=%q, want 200 + strong ETag", first.Code, etag)
	}
	again := getPanel(t, s, id, etag)
	if again.Code != http.StatusNotModified || strings.TrimSpace(again.Body.String()) != "" {
		t.Fatalf("unchanged /panel tick must be 304 with an empty body (this is what dropped the old in-body poll node); got code=%d body=%q", again.Code, again.Body.String())
	}

	// Start a turn exactly as the composer form does (background), then poll the
	// panel — the real refresh path — until the reply and its diff land. This is
	// the end-to-end proof that the poll path DELIVERS the post-load update.
	if w := postForm(t, s, "/editor/"+id+"/chat", url.Values{"message": {"add a marker line"}}); w.Code != http.StatusOK {
		t.Fatalf("POST /chat = %d: %s", w.Code, w.Body)
	}

	deadline := time.Now().Add(10 * time.Second)
	var body string
	for {
		body = getPanel(t, s, id, "").Body.String()
		if strings.Contains(body, reply) && strings.Contains(body, marker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the reply and diff never appeared via the poll path (no manual reload):\n%s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The reply must be rendered in the chat log, and the appended line must show
	// as an ADDED diff row — both delivered purely by polling /panel.
	if !strings.Contains(body, reply) {
		t.Fatalf("assistant reply missing from the polled panel:\n%s", body)
	}
	if !strings.Contains(body, `class="ln add"`) || !strings.Contains(body, marker) {
		t.Fatalf("the post-load diff update did not appear as an added row via the poll path:\n%s", body)
	}
}

// getPanel issues GET /editor/{id}/panel, optionally presenting inm in
// If-None-Match so the test can exercise the renderConditional 304 path.
func getPanel(t *testing.T, s *Server, id, inm string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/editor/"+id+"/panel", nil)
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	s.Routes().ServeHTTP(w, req)
	return w
}
