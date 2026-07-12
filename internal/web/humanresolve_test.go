package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
)

// humanFixture builds a Server whose alpha submodule PLAN.md carries a
// NEEDS-HUMAN task and whose resolution manager is wired onto a fake opencode
// client (so no real model is called). It returns the server, its repo root, and
// a live httptest server so path values populate.
func humanFixture(t *testing.T, reply string) (*Server, string, *httptest.Server) {
	t.Helper()
	client := &fakeChatClient{reply: reply}
	s, root := chatFixtureClient(t, client)
	// Rewire the resolution agent manager onto the same fake-backed client.
	s.humans = newResolveManager(root, client, 0)
	// Seed a real NEEDS-HUMAN task and commit it so headSHA/planView see it.
	planRel := "submodules/alpha/PLAN.md"
	write(t, root+"/"+planRel, "<!-- Beehive-ROI: abc123 -->\n# Plan\n\n"+
		"## needs-token [NEEDS-HUMAN] <!-- attempts=0 deps= weight=4 category=secret -->\n"+
		"Wire the external API client.\n"+
		"Human-needed: provide the API token in the secrets panel as api_token.\n")
	if err := git.New(root).Commit(context.Background(), "seed needs-human task"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Routes())
	t.Cleanup(ts.Close)
	return s, root, ts
}

// waitIdle polls until the task's resolution session finishes its background turn.
func waitIdle(t *testing.T, s *Server, sub, id string) *resolveSession {
	t.Helper()
	key := taskKey(sub, id)
	for i := 0; i < 200; i++ {
		s.humans.mu.Lock()
		sess := s.humans.byTask[key]
		s.humans.mu.Unlock()
		if sess != nil && !sess.isBusy() {
			return sess
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("resolution session never went idle")
	return nil
}

// TestResolveSystemPromptSeedsBlockerAndBoundaries: the resolution agent's system
// prompt carries the concrete blocker AND the accurate capability/boundary
// contract \u2014 tool authority, the repo/ code boundary, the no-secrets rule, and
// the explicit ways a NEEDS-HUMAN task clears (Secrets panel, Publish, Mark
// resolved) \u2014 so the agent drives a real unblock instead of dead-ending.
func TestResolveSystemPromptSeedsBlockerAndBoundaries(t *testing.T) {
	it := PlanItem{ID: "needs-token", Desc: "Wire the external API client.", HumanReason: "provide the API token as api_token."}
	sys := resolveSystemPrompt("alpha", it)
	for _, want := range []string{
		"needs-token", "alpha", "provide the API token as api_token.", "Wire the external API client.",
		"NEEDS-HUMAN", "submodules/alpha/repo/", "WORK task", "Secrets panel",
		"Publish", "Mark resolved", "secret", "read, grep, bash, edit, write",
		"STAY INSIDE YOUR WORKING DIRECTORY", "absolute paths outside it",
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
	// Regression: the prompt must NOT direct the agent to read the live source via
	// an absolute path outside the worktree — that wedges the turn on an
	// unanswerable opencode permission prompt (the resolution-agent hang).
	for _, bad := range []string{"live source at", "%[5]s"} {
		if strings.Contains(sys, bad) {
			t.Fatalf("system prompt still contains out-of-tree directive %q", bad)
		}
	}
}

// TestHumanResolvePageCategoryAffordance: the resolve page leads with the
// category badge + the one-line categorical ask and shows ONLY that category's
// affordance. The seeded task is `secret`, so the page carries the secret badge,
// the secret ask, and the Secrets-panel step — and NOT the contradiction/
// architecture guidance meant for other categories.
func TestHumanResolvePageCategoryAffordance(t *testing.T) {
	_, _, ts := humanFixture(t, "How can I help?")
	body := httpGet(t, ts.URL+"/human/alpha/needs-token")
	for _, want := range []string{
		"cat-secret", "secret",
		"credential only you can provide",
		"Secrets panel",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("secret resolve page missing %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{
		"which intent wins", "hard-to-reverse design decision",
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("secret resolve page leaked another category's affordance %q", bad)
		}
	}
}

// TestHumanResolvePageOpensSession: opening a blocked task's resolution page
// creates (and remembers) exactly one agent session, and reloading reuses it.
func TestHumanResolvePageOpensSession(t *testing.T) {
	s, _, ts := humanFixture(t, "How can I help?")
	body := httpGet(t, ts.URL+"/human/alpha/needs-token")
	if !strings.Contains(body, "AI resolution agent") || !strings.Contains(body, "provide the API token") {
		t.Fatalf("resolution page missing agent/blocker:\n%s", body)
	}
	s.humans.mu.Lock()
	id1 := ""
	if sess := s.humans.byTask["alpha/needs-token"]; sess != nil {
		id1 = sess.ID
	}
	s.humans.mu.Unlock()
	if id1 == "" {
		t.Fatal("no agent session remembered for the task")
	}
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	s.humans.mu.Lock()
	id2 := s.humans.byTask["alpha/needs-token"].ID
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

// TestHumanResolveBreadcrumb (breadcrumb-coverage-gap): the per-task resolution
// workspace hangs off the GLOBAL NEEDS-HUMAN queue, not a submodule, so its trail
// is dashboard > human > <sub>/<id> — the "human" ancestor a working link back to
// the queue (the old ad hoc "← NEEDS-HUMAN" back-link it replaces), the <sub>/<id>
// leaf the aria-current current page. The deliberately-kept "plan" convenience
// link stays reachable below the trail (it is a sibling cross-link, not an
// ancestor, so it does not belong in the trail itself).
func TestHumanResolveBreadcrumb(t *testing.T) {
	_, _, ts := humanFixture(t, "")
	body := httpGet(t, ts.URL+"/human/alpha/needs-token")
	bc := breadcrumbHTML(t, body)
	if bc == "" {
		t.Fatalf("human resolve page missing breadcrumb landmark:\n%s", body)
	}
	for _, want := range []string{
		`<a href="/">dashboard</a>`,
		`<a href="/human">human</a>`,
		`<span aria-current="page">alpha/needs-token</span>`,
	} {
		if !strings.Contains(bc, want) {
			t.Fatalf("human resolve breadcrumb missing %q:\n%s", want, bc)
		}
	}
	if n := strings.Count(bc, "aria-current"); n != 1 {
		t.Fatalf("aria-current count = %d, want 1:\n%s", n, bc)
	}
	// The old ad hoc "← NEEDS-HUMAN" back-link is now the trail's "human" crumb.
	if strings.Contains(body, "← NEEDS-HUMAN") {
		t.Fatalf("human resolve still carries the old ad hoc back-link:\n%s", body)
	}
	// The kept "plan" convenience link survives (below the trail).
	if !strings.Contains(body, `href="/submodule/alpha/plan"`) {
		t.Fatalf("human resolve dropped the kept plan convenience link:\n%s", body)
	}
}

// TestResolvePublishLandsChangesOnMain: a coordination-layer change made in the
// agent's worktree is published to the hive main by the Publish action, so it is
// live for the swarm. (The fake client makes no edits, so the test writes the
// change into the worktree directly, exercising the real commit+publish path.)
func TestResolvePublishLandsChangesOnMain(t *testing.T) {
	s, root, _ := humanFixture(t, "")
	it := PlanItem{ID: "needs-token", Desc: "Wire the client.", HumanReason: "token"}
	sess, err := s.humans.session(context.Background(), "alpha", it)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the agent editing a beehive-layer file in its worktree.
	target := filepath.Join(sess.wtPath, "submodules", "alpha", "INFRASTRUCTURE.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# alpha infra\ndocumented the process\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Commit the agent's work onto the branch (runTurn does this after a turn).
	if err := sess.wt.Commit(context.Background(), "resolve: agent turn"); err != nil {
		t.Fatal(err)
	}
	has, err := sess.hasChanges(context.Background())
	if err != nil || !has {
		t.Fatalf("expected pending change, has=%v err=%v", has, err)
	}
	remote, err := s.humans.publishRemote(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.publish(context.Background(), remote); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got := gitShow(t, root, "HEAD", "submodules/alpha/INFRASTRUCTURE.md")
	if !strings.Contains(got, "documented the process") {
		t.Fatalf("published content not on main HEAD: %q", got)
	}
	if !sess.isPublished() {
		t.Fatal("session not marked published")
	}
}

// TestResolvePublishNothingIsRejected: Publish with no change is a clean error,
// never an empty commit on main.
func TestResolvePublishNothingIsRejected(t *testing.T) {
	s, _, _ := humanFixture(t, "")
	it := PlanItem{ID: "needs-token"}
	sess, err := s.humans.session(context.Background(), "alpha", it)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.publish(context.Background(), ""); err != errNothingToPub {
		t.Fatalf("publish with no change = %v, want errNothingToPub", err)
	}
}

// TestResolveMessageRunsTurnAndCommits: a chat message runs a background agent
// turn that records the reply; the panel renders it once idle.
func TestResolveMessageRunsTurnAndCommits(t *testing.T) {
	s, _, ts := humanFixture(t, "I inspected the blocker; add api_token in the Secrets panel.")
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	resp := httpPostForm(t, ts.URL+"/human/alpha/needs-token/message/"+s.humans.byTask["alpha/needs-token"].ID,
		url.Values{"message": {"what does this need?"}})
	if resp != http.StatusOK {
		t.Fatalf("message status = %d", resp)
	}
	sess := waitIdle(t, s, "alpha", "needs-token")
	found := false
	for _, tn := range sess.logCopy() {
		if tn.Role == "agent" && strings.Contains(tn.Text, "Secrets panel") {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent reply not recorded: %+v", sess.logCopy())
	}
}

// TestHumanResolveDiscardResetsSession: discard tears down the agent worktree and
// opens a fresh session (a different id) for the same task.
func TestHumanResolveDiscardResetsSession(t *testing.T) {
	s, _, ts := humanFixture(t, "")
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	s.humans.mu.Lock()
	before := s.humans.byTask["alpha/needs-token"].ID
	s.humans.mu.Unlock()
	if code := httpPost(t, ts.URL+"/human/alpha/needs-token/discard/"+before); code != http.StatusOK && code != http.StatusSeeOther {
		t.Fatalf("discard status = %d", code)
	}
	s.humans.mu.Lock()
	after := s.humans.byTask["alpha/needs-token"].ID
	s.humans.mu.Unlock()
	if after == before {
		t.Fatalf("discard did not reset the session: still %s", after)
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
	_ = httpPost(t, ts.URL+"/human/alpha/needs-token/resolve")
	before := gitShow(t, root, "HEAD", "submodules/alpha/PLAN.md")
	if code := httpPost(t, ts.URL+"/human/alpha/needs-token/resolve"); code != http.StatusConflict {
		t.Fatalf("second resolve status = %d, want 409", code)
	}
	after := gitShow(t, root, "HEAD", "submodules/alpha/PLAN.md")
	if before != after {
		t.Fatal("rejected resolve still changed HEAD PLAN.md")
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

func httpPostForm(t *testing.T, u string, form url.Values) int {
	t.Helper()
	resp, err := noRedirect().PostForm(u, form)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
