package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
)

// humanFixture builds a chat-backed Server (fake opencode client) whose alpha
// submodule PLAN.md carries a NEEDS-HUMAN task, wires the human-resolution manager
// to the same fake-backed chat manager, and serves it over httptest so path
// values populate. It returns the server, its repo root, and the live test server.
func humanFixture(t *testing.T, reply string) (*Server, string, *httptest.Server) {
	t.Helper()
	s, root := chatFixture(t, reply)
	// Rewire the human manager onto the fake-backed chat manager chatFixture swapped
	// in (New wired it to the real opencode client).
	s.humans = newHumanManager(s.chat)
	// Seed a real NEEDS-HUMAN task and commit it so headSHA/planView see it.
	planRel := "submodules/alpha/PLAN.md"
	write(t, root+"/"+planRel, "<!-- Beehive-ROI: abc123 -->\n# Plan\n\n"+
		"## needs-token [NEEDS-HUMAN] <!-- attempts=0 deps= weight=4 -->\n"+
		"Wire the external API client.\n"+
		"Human-needed: provide the API token in the secrets panel as api_token.\n")
	if err := git.New(root).Commit(context.Background(), "seed needs-human task"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Routes())
	t.Cleanup(ts.Close)
	return s, root, ts
}

// TestHumanResolveSystemPromptSeedsBlocker: the resolution system prompt carries
// the concrete blocker (task id, submodule, reason) AND the generic chat-diff
// contract for the target file (the propose markers), so the AI both understands
// the blocker and stays inside the human-approved diff loop.
func TestHumanResolveSystemPromptSeedsBlocker(t *testing.T) {
	it := PlanItem{ID: "needs-token", Desc: "Wire the external API client.", HumanReason: "provide the API token as api_token."}
	sys := humanResolveSystemPrompt("alpha", it, "submodules/alpha/ROI.md")
	for _, want := range []string{"needs-token", "alpha", "provide the API token as api_token.", "Wire the external API client.", proposeOpen, proposeClose} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
}

// TestHumanResolvePageOpensSession: opening a blocked task's resolution page
// creates (and remembers) exactly one chat session, and reloading reuses it.
func TestHumanResolvePageOpensSession(t *testing.T) {
	s, _, ts := humanFixture(t, "How can I help?")
	body := httpGet(t, ts.URL+"/human/alpha/needs-token")
	if !strings.Contains(body, "AI resolution chat") || !strings.Contains(body, "provide the API token") {
		t.Fatalf("resolution page missing chat/blocker:\n%s", body)
	}
	s.humans.mu.Lock()
	id1, ok := s.humans.bySession[sessionKey("alpha", "needs-token", "submodules/alpha/ROI.md")]
	s.humans.mu.Unlock()
	if !ok || id1 == "" {
		t.Fatal("no chat session remembered for the task")
	}
	// Reload reuses the same session (no fresh worktree).
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	s.humans.mu.Lock()
	id2 := s.humans.bySession[sessionKey("alpha", "needs-token", "submodules/alpha/ROI.md")]
	s.humans.mu.Unlock()
	if id1 != id2 {
		t.Fatalf("reload cut a new session: %s != %s", id1, id2)
	}
}

// TestHumanResolvePageStaleLink404: a link to a task that is no longer
// NEEDS-HUMAN (resolved in another tab) is a 404, never acted on.
func TestHumanResolvePageStaleLink404(t *testing.T) {
	_, _, ts := humanFixture(t, "")
	if code := httpStatus(t, ts.URL+"/human/alpha/does-not-exist"); code != http.StatusNotFound {
		t.Fatalf("unknown task status = %d, want 404", code)
	}
}

// TestHumanResolveApplyReopens: resolving flips the task NEEDS-HUMAN -> TODO,
// drops the Human-needed line, and publishes PLAN.md (the change is committed to
// HEAD, so the swarm re-selects the task).
func TestHumanResolveApplyReopens(t *testing.T) {
	_, root, ts := humanFixture(t, "")
	resp := httpPost(t, ts.URL+"/human/alpha/needs-token/resolve")
	if resp != http.StatusOK && resp != http.StatusSeeOther {
		t.Fatalf("resolve status = %d", resp)
	}
	// Committed to HEAD (not just the working tree).
	head := gitShow(t, root, "HEAD", "submodules/alpha/PLAN.md")
	p, err := plan.Parse(head)
	if err != nil {
		t.Fatal(err)
	}
	tk := p.Task("needs-token")
	if tk == nil || tk.Status != plan.StatusTODO {
		t.Fatalf("task not reopened to TODO: %+v", tk)
	}
	if tk.HumanReason() != "" {
		t.Fatalf("human reason not cleared: %q", tk.HumanReason())
	}
	if !p.Selectable(tk) {
		t.Fatal("reopened task should be selectable")
	}
}

// TestHumanResolveApplyRejectsNonHuman: resolving a task that is not NEEDS-HUMAN
// is a 409 conflict and does not rewrite PLAN.md (a double-submit or stale link
// can never reset an in-flight task).
func TestHumanResolveApplyRejectsNonHuman(t *testing.T) {
	_, root, ts := humanFixture(t, "")
	// First resolve moves it to TODO.
	_ = httpPost(t, ts.URL+"/human/alpha/needs-token/resolve")
	before := gitShow(t, root, "HEAD", "submodules/alpha/PLAN.md")
	// Second resolve on the now-TODO task must be rejected without a new commit.
	if code := httpPost(t, ts.URL+"/human/alpha/needs-token/resolve"); code != http.StatusConflict {
		t.Fatalf("second resolve status = %d, want 409", code)
	}
	after := gitShow(t, root, "HEAD", "submodules/alpha/PLAN.md")
	if before != after {
		t.Fatal("rejected resolve still changed HEAD PLAN.md")
	}
}

// TestHumanResolveRetargetsPerFile: pointing the resolution chat at a different
// beehive-layer file via ?path= opens (and remembers) a DISTINCT session rather
// than silently reusing the first file's session. This is the retargeting fix:
// the session is keyed by (task, path), so "document this in INFRASTRUCTURE.md"
// reaches a chat over that file, not the default ROI.md.
func TestHumanResolveRetargetsPerFile(t *testing.T) {
	s, _, ts := humanFixture(t, "ok")
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token") // default ROI.md target
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token?path=submodules/alpha/INFRASTRUCTURE.md")
	s.humans.mu.Lock()
	roi := s.humans.bySession[sessionKey("alpha", "needs-token", "submodules/alpha/ROI.md")]
	infra := s.humans.bySession[sessionKey("alpha", "needs-token", "submodules/alpha/INFRASTRUCTURE.md")]
	s.humans.mu.Unlock()
	if roi == "" || infra == "" {
		t.Fatalf("missing session: roi=%q infra=%q", roi, infra)
	}
	if roi == infra {
		t.Fatal("retargeting to a different file reused the same session")
	}
	sess, ok := s.chat.get(infra)
	if !ok || sess.Path != "submodules/alpha/INFRASTRUCTURE.md" {
		t.Fatalf("retargeted session not over the requested file: %+v", sess)
	}
}

// TestHumanResolveForgetDropsAllTargets: resolving forgets every per-file session
// for the task, not just the default one, so a later re-escalation starts clean.
func TestHumanResolveForgetDropsAllTargets(t *testing.T) {
	s, _, ts := humanFixture(t, "ok")
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token?path=submodules/alpha/ARTIFACTS.md")
	s.humans.forget("alpha", "needs-token")
	s.humans.mu.Lock()
	n := len(s.humans.bySession)
	s.humans.mu.Unlock()
	if n != 0 {
		t.Fatalf("forget left %d sessions, want 0", n)
	}
}

// TestHumanResolvePageRendersTargetSelector: the resolution page surfaces the
// retarget controls (the beehive-layer files the chat can be pointed at) so the
// operator is never stuck on ROI.md.
func TestHumanResolvePageRendersTargetSelector(t *testing.T) {
	_, _, ts := humanFixture(t, "ok")
	body := httpGet(t, ts.URL+"/human/alpha/needs-token")
	for _, want := range []string{"AI chat target", "submodules/alpha/INFRASTRUCTURE.md", "submodules/alpha/ARTIFACTS.md"} {
		if !strings.Contains(body, want) {
			t.Fatalf("resolution page missing target control %q", want)
		}
	}
}

// TestHumanResolveSystemPromptStatesCapabilities: the prompt must accurately tell
// the AI it can retarget beehive-layer files and CANNOT run commands or edit the
// submodule's repo/ source — so it never dead-ends on "I can only edit ROI.md".
func TestHumanResolveSystemPromptStatesCapabilities(t *testing.T) {
	it := PlanItem{ID: "needs-token", Desc: "Wire the client.", HumanReason: "provide the token."}
	sys := humanResolveSystemPrompt("alpha", it, "submodules/alpha/ROI.md")
	for _, want := range []string{"retarget", "INFRASTRUCTURE.md", "submodules/alpha/repo/", "WORK task", "NO tools", "NEVER pasted"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing capability statement %q:\n%s", want, sys)
		}
	}
}

// ---- tiny HTTP helpers (no redirect-follow so we can read the resolve 303) ----

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func httpGet(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func httpStatus(t *testing.T, u string) int {
	t.Helper()
	resp, err := noRedirect().Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func httpPost(t *testing.T, u string) int {
	t.Helper()
	resp, err := noRedirect().PostForm(u, url.Values{})
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
