package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
)

// humanFixture builds a Server whose alpha submodule PLAN.md carries a
// NEEDS-HUMAN task and whose resolution manager is wired onto a fake opencode
// client (so no real model is called). It returns the server, its repo root, and
// a live httptest server so path values populate.
func humanFixture(t *testing.T, reply string) (*Server, string, *httptest.Server) {
	t.Helper()
	s, root := editorFixtureClient(t, &fakeAgentClient{reply: reply})
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
func waitIdle(t *testing.T, s *Server, sub, id string) *editor.Session {
	t.Helper()
	for i := 0; i < 200; i++ {
		if sess, ok := s.humans.find(sub, id); ok && !sess.Busy() {
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
	if sess, ok := s.humans.find("alpha", "needs-token"); ok {
		id1 = sess.ID
	}
	s.humans.mu.Unlock()
	if id1 == "" {
		t.Fatal("no agent session remembered for the task")
	}
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	s.humans.mu.Lock()
	sess2, _ := s.humans.find("alpha", "needs-token")
	id2 := sess2.ID
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
	// The agent edits a beehive-layer file in its worktree; a turn commits it, and
	// Publish lands it on main. editFn writes INFRASTRUCTURE.md on the turn so the
	// real commit+publish path runs on real content.
	client := &fakeAgentClient{
		reply: "Documented the process in INFRASTRUCTURE.md.",
		editFn: func(cwd string) {
			p := filepath.Join(cwd, "submodules", "alpha", "INFRASTRUCTURE.md")
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte("# alpha infra\ndocumented the process\n"), 0o644)
		},
	}
	s, root := editorFixtureClient(t, client)
	it := PlanItem{ID: "needs-token", Desc: "Wire the client.", HumanReason: "token"}
	sess, err := s.humans.session(context.Background(), "alpha", it)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(context.Background(), "document the process"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if sess.State(context.Background()) != "dirty" {
		t.Fatalf("expected a pending change after the turn")
	}
	if err := resolvePublish(context.Background(), sess); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got := gitShow(t, root, "HEAD", "submodules/alpha/INFRASTRUCTURE.md")
	if !strings.Contains(got, "documented the process") {
		t.Fatalf("published content not on main HEAD: %q", got)
	}
	if sess.State(context.Background()) != "live" {
		t.Fatal("session should be live (published) after publish")
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
	if err := resolvePublish(context.Background(), sess); err != errNothingToPub {
		t.Fatalf("publish with no change = %v, want errNothingToPub", err)
	}
}

// TestResolveMessageRunsTurnAndCommits: a chat message runs a background agent
// turn that records the reply; the panel renders it once idle.
func TestResolveMessageRunsTurnAndCommits(t *testing.T) {
	s, _, ts := humanFixture(t, "I inspected the blocker; add api_token in the Secrets panel.")
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	sess0, _ := s.humans.find("alpha", "needs-token")
	resp := httpPostForm(t, ts.URL+"/human/alpha/needs-token/message/"+sess0.ID,
		url.Values{"message": {"what does this need?"}})
	if resp != http.StatusOK {
		t.Fatalf("message status = %d", resp)
	}
	sess := waitIdle(t, s, "alpha", "needs-token")
	found := false
	for _, tn := range sess.Log() {
		if tn.Role == "agent" && strings.Contains(tn.Text, "Secrets panel") {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent reply not recorded: %+v", sess.Log())
	}
}

// TestHumanResolveDiscardResetsSession: discard tears down the agent worktree and
// opens a fresh session (a different id) for the same task.
func TestHumanResolveDiscardResetsSession(t *testing.T) {
	s, _, ts := humanFixture(t, "")
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	sessBefore, _ := s.humans.find("alpha", "needs-token")
	before := sessBefore.ID
	if code := httpPost(t, ts.URL+"/human/alpha/needs-token/discard/"+before); code != http.StatusOK && code != http.StatusSeeOther {
		t.Fatalf("discard status = %d", code)
	}
	sessAfter, _ := s.humans.find("alpha", "needs-token")
	after := ""
	if sessAfter != nil {
		after = sessAfter.ID
	}
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

// httpGetJSON GETs u and decodes the JSON body into v, returning the status code.
func httpGetJSON(t *testing.T, u string, v interface{}) int {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode %s: %v", u, err)
		}
	}
	return resp.StatusCode
}

// httpPostJSON POSTs body (marshaled as JSON, or nil for an empty body) to u
// and decodes the JSON response into v, returning the status code.
func httpPostJSON(t *testing.T, u string, body interface{}, v interface{}) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	resp, err := http.Post(u, "application/json", rd)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	defer resp.Body.Close()
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode %s: %v", u, err)
		}
	}
	return resp.StatusCode
}

// TestHumanJSONListsAcrossSubmodules: GET /human.json returns the SAME
// hive-wide NEEDS-HUMAN scan the HTML /human page renders (humanRows), never
// a duplicated scan.
func TestHumanJSONListsAcrossSubmodules(t *testing.T) {
	_, _, ts := humanFixture(t, "")
	var out struct {
		Tasks []struct {
			Sub      string `json:"sub"`
			ID       string `json:"id"`
			Reason   string `json:"reason"`
			Category string `json:"category"`
		} `json:"tasks"`
	}
	if code := httpGetJSON(t, ts.URL+"/human.json", &out); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	found := false
	for _, tk := range out.Tasks {
		if tk.Sub == "alpha" && tk.ID == "needs-token" {
			found = true
			if tk.Category != "secret" {
				t.Fatalf("category = %q, want secret", tk.Category)
			}
			if !strings.Contains(tk.Reason, "api_token") {
				t.Fatalf("reason missing blocker text: %q", tk.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("needs-token task missing from /human.json: %+v", out.Tasks)
	}
}

// TestHumanTaskJSONContext: GET /human.json/{sub}/{id} returns one task's
// context, and a stale/unknown link is a 404 — mirroring humanTask exactly.
func TestHumanTaskJSONContext(t *testing.T) {
	_, _, ts := humanFixture(t, "")
	var out struct {
		Sub        string `json:"sub"`
		ID         string `json:"id"`
		Desc       string `json:"desc"`
		Reason     string `json:"reason"`
		Category   string `json:"category"`
		HasSession bool   `json:"has_session"`
	}
	if code := httpGetJSON(t, ts.URL+"/human.json/alpha/needs-token", &out); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.Sub != "alpha" || out.ID != "needs-token" || out.Category != "secret" {
		t.Fatalf("unexpected task context: %+v", out)
	}
	if out.HasSession {
		t.Fatal("has_session should be false before any session is opened")
	}
	if code := httpStatus(t, ts.URL+"/human.json/alpha/does-not-exist"); code != http.StatusNotFound {
		t.Fatalf("unknown task status = %d, want 404", code)
	}
}

// TestAPIHumanSessionOpensAndReuses: POST /api/human/{sub}/{id}/session opens
// (or returns the existing) resolution session, matching humanResolvePage's
// session reuse behavior, and returns panel data alongside the sid.
func TestAPIHumanSessionOpensAndReuses(t *testing.T) {
	_, _, ts := humanFixture(t, "How can I help?")
	var out1 struct {
		SessID string `json:"SessID"`
	}
	if code := httpPostJSON(t, ts.URL+"/api/human/alpha/needs-token/session", nil, &out1); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out1.SessID == "" {
		t.Fatal("no sid returned")
	}
	var out2 struct {
		SessID string `json:"SessID"`
	}
	if code := httpPostJSON(t, ts.URL+"/api/human/alpha/needs-token/session", nil, &out2); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out2.SessID != out1.SessID {
		t.Fatalf("second session open cut a new session: %s != %s", out1.SessID, out2.SessID)
	}
}

// TestAPIHumanPanelMirrorsHTML: GET /api/human/{sub}/{id}/panel/{sid} reuses
// resolvePanelData, so its shape matches the HTML panel's data exactly.
func TestAPIHumanPanelMirrorsHTML(t *testing.T) {
	s, _, ts := humanFixture(t, "How can I help?")
	it := PlanItem{ID: "needs-token", Desc: "Wire the client.", HumanReason: "token"}
	sess, err := s.humans.session(context.Background(), "alpha", it)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		SessID string `json:"SessID"`
		Busy   bool   `json:"Busy"`
	}
	if code := httpGetJSON(t, ts.URL+"/api/human/alpha/needs-token/panel/"+sess.ID, &out); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out.SessID != sess.ID {
		t.Fatalf("panel sid = %q, want %q", out.SessID, sess.ID)
	}
	if code := httpStatus(t, ts.URL+"/api/human/alpha/needs-token/panel/does-not-exist"); code != http.StatusNotFound {
		t.Fatalf("unknown sid status = %d, want 404", code)
	}
}

// TestAPIHumanMessageRunsTurn: POST /api/human/{sub}/{id}/message/{sid} runs
// one agent turn and returns the refreshed panel, mirroring
// humanResolveMessage.
func TestAPIHumanMessageRunsTurn(t *testing.T) {
	s, _, ts := humanFixture(t, "I inspected the blocker; add api_token in the Secrets panel.")
	_ = httpGet(t, ts.URL+"/human/alpha/needs-token")
	sess0, _ := s.humans.find("alpha", "needs-token")
	var out map[string]interface{}
	code := httpPostJSON(t, ts.URL+"/api/human/alpha/needs-token/message/"+sess0.ID,
		map[string]string{"message": "what does this need?"}, &out)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	sess := waitIdle(t, s, "alpha", "needs-token")
	found := false
	for _, tn := range sess.Log() {
		if tn.Role == "agent" && strings.Contains(tn.Text, "Secrets panel") {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent reply not recorded: %+v", sess.Log())
	}
}

// TestAPIHumanPublishAndResolveJSON: the JSON publish/discard/resolve mirrors
// behave like their HTML counterparts (TestResolvePublishLandsChangesOnMain /
// TestHumanResolveDiscardResetsSession / TestHumanResolveApplyReopens), never
// duplicating the underlying resolvePublish/session/plan.Task.Resolve logic.
func TestAPIHumanPublishAndResolveJSON(t *testing.T) {
	client := &fakeAgentClient{
		reply: "Documented the process in INFRASTRUCTURE.md.",
		editFn: func(cwd string) {
			p := filepath.Join(cwd, "submodules", "alpha", "INFRASTRUCTURE.md")
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte("# alpha infra\ndocumented via api\n"), 0o644)
		},
	}
	s, root := editorFixtureClient(t, client)
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

	it := PlanItem{ID: "needs-token", Desc: "Wire the client.", HumanReason: "token"}
	sess, err := s.humans.session(context.Background(), "alpha", it)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(context.Background(), "document the process"); err != nil {
		t.Fatalf("chat: %v", err)
	}

	var pubOut map[string]interface{}
	if code := httpPostJSON(t, ts.URL+"/api/human/alpha/needs-token/publish/"+sess.ID, nil, &pubOut); code != http.StatusOK {
		t.Fatalf("publish status = %d: %+v", code, pubOut)
	}
	if e, ok := pubOut["error"]; ok && e != nil && e != "" {
		t.Fatalf("publish returned error: %v", e)
	}
	got := gitShow(t, root, "HEAD", "submodules/alpha/INFRASTRUCTURE.md")
	if !strings.Contains(got, "documented via api") {
		t.Fatalf("published content not on main HEAD: %q", got)
	}

	// Discard should return null (task no longer needs a fresh session once
	// published — the resolution work is done, but it is still NEEDS-HUMAN
	// until explicitly resolved, so a fresh sid IS expected here).
	var discardOut struct {
		Sid interface{} `json:"sid"`
	}
	if code := httpPostJSON(t, ts.URL+"/api/human/alpha/needs-token/discard/"+sess.ID, nil, &discardOut); code != http.StatusOK {
		t.Fatalf("discard status = %d", code)
	}
	if discardOut.Sid == nil {
		t.Fatal("expected a fresh sid since the task is still NEEDS-HUMAN")
	}

	var resolveOut map[string]interface{}
	if code := httpPostJSON(t, ts.URL+"/api/human/alpha/needs-token/resolve", nil, &resolveOut); code != http.StatusOK {
		t.Fatalf("resolve status = %d: %+v", code, resolveOut)
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

	// A second resolve is a JSON 409, matching TestHumanResolveApplyRejectsNonHuman.
	var conflictOut map[string]interface{}
	if code := httpPostJSON(t, ts.URL+"/api/human/alpha/needs-token/resolve", nil, &conflictOut); code != http.StatusConflict {
		t.Fatalf("second resolve status = %d, want 409", code)
	}
	if _, ok := conflictOut["error"]; !ok {
		t.Fatalf("conflict response missing error field: %+v", conflictOut)
	}
}
