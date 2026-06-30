package web

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/repo"
)

func setup(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	g := exec.Command("git", "init")
	g.Dir = root
	if err := g.Run(); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][]string{{"user.email", "t@t"}, {"user.name", "t"}} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = root
		c.Run()
	}
	sm := filepath.Join(root, "submodules", "alpha")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, repo.ROIFile), []byte("# alpha\n"), 0o644)
	// Real H2-header PLAN.md format (internal/plan), NOT the legacy bullet form:
	// `## <id> [STATUS] <!-- attempts=N deps=a,b weight=W session=<id>
	// heartbeat=<RFC3339> -->` with a free-form body (Desc = first line, Doc =
	// the "Doc:" line). t1 carries a session+heartbeat claim.
	os.WriteFile(filepath.Join(sm, repo.PlanFile), []byte(
		"<!-- Beehive-ROI: abc123 -->\n# Plan\n\n"+
			"## t1 [TODO] <!-- attempts=0 deps=t0 weight=16 session=bee-1 heartbeat=2026-06-30T11:00:00Z -->\n"+
			"build the thing\nFiles: a.go\nDoc: br-t1.md\nAccept: works\n\n"+
			"## t2 [NEEDS-HUMAN] <!-- attempts=4 deps= -->\nstuck task\nDoc: br-t2.md\n\n"+
			"## t3 [DONE] <!-- attempts=0 deps= -->\nok done\nDoc: br-t3.md\n"), 0o644)
	r, _ := repo.Open(root)
	s, err := New(r, config.Defaults(root))
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

// renderTmpl executes a named template to a string. The polled fragments
// (session_body / session_list / editor_panel) need live session/editor data to
// reach through a handler, so their scroll-preserve wiring is asserted directly
// off the parsed template set (white-box) instead of standing up a fake session.
func renderTmpl(t *testing.T, s *Server, name string, data interface{}) string {
	t.Helper()
	var b strings.Builder
	if err := s.tmpl.ExecuteTemplate(&b, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return b.String()
}

// TestScrollPreserveScriptEmbedded locks that the save/restore script ships on a
// real full-page response (the layout footer), with no external lib reference.
// It is what keeps every polled pane from yanking the reader to the top.
func TestScrollPreserveScriptEmbedded(t *testing.T) {
	s, _ := setup(t)
	page := get(t, s, "/").Body.String() // dashboard renders the layout header+footer
	for _, want := range []string{
		"htmx:beforeSwap", "htmx:afterSwap", // save before, restore after a swap
		"data-scroll-preserve", // the pane contract the script keys off
		"data-scroll-pin",      // bottom-pin (follow live output only when pinned)
		"scrollTop",            // per-pane restore
		"window.scrollTo",      // window-scroll restore (covers page-scrolled lists)
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("embedded scroll-restore script missing %q", want)
		}
	}
}

// TestScrollPreserveWiring locks the per-fragment contract: every polled scroll
// region carries a STABLE pane id + data-scroll-preserve, and the live-growing
// panes (session transcript, editor chat) additionally pin to bottom. DOM
// scrollTop itself is not unit-testable in Go; the manual scroll check is in the
// change doc. A rename here must break this test on purpose.
func TestScrollPreserveWiring(t *testing.T) {
	s, _ := setup(t)

	// Session transcript: stable id, preserve, and pin (it grows at the bottom).
	body := renderTmpl(t, s, "session_body.html", map[string]interface{}{"Body": "turn\n"})
	for _, want := range []string{`id="session-transcript"`, "data-scroll-preserve", "data-scroll-pin"} {
		if !strings.Contains(body, want) {
			t.Fatalf("session_body.html missing %q:\n%s", want, body)
		}
	}

	// Auto-refreshing session list: preserve wiring on the polled node.
	list := renderTmpl(t, s, "session_list.html", map[string]interface{}{"Name": "alpha"})
	for _, want := range []string{`id="session-list"`, "data-scroll-preserve"} {
		if !strings.Contains(list, want) {
			t.Fatalf("session_list.html missing %q:\n%s", want, list)
		}
	}

	// Editor chat+diff: chat pins to bottom, diff just holds position.
	panel := renderTmpl(t, s, "editor_panel.html", map[string]interface{}{"ID": "e1", "File": "ROI.md"})
	for _, want := range []string{`id="editor-chat"`, `id="editor-diff"`, "data-scroll-preserve", "data-scroll-pin"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("editor_panel.html missing %q:\n%s", want, panel)
		}
	}

	// The transcript poll target shell is present (the script restores into it).
	view := renderTmpl(t, s, "session_view.html", map[string]interface{}{"Name": "alpha", "Branch": "bee-x"})
	if !strings.Contains(view, `id="session-body"`) {
		t.Fatalf("session_view.html missing #session-body poll target:\n%s", view)
	}
}

func TestDashboard(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "alpha") {
		t.Fatalf("dashboard %d: %s", w.Code, w.Body)
	}
}

func TestPlanAndHuman(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/submodule/alpha/plan")
	b := w.Body.String()
	// Real H2-header plan: ROI stamp, ids, the derived Doc, the deps cell, and the
	// derived claim label must all surface (none of which the old bullet parser,
	// fed a real plan, produced — it parsed zero rows).
	for _, want := range []string{"abc123", "t1", "TODO", "NEEDS-HUMAN", "DONE", "br-t1.md", "t0"} {
		if !strings.Contains(b, want) {
			t.Fatalf("plan view missing %q:\n%s", want, b)
		}
	}
	h := get(t, s, "/human")
	if !strings.Contains(h.Body.String(), "t2") || strings.Contains(h.Body.String(), "t1<") {
		t.Fatalf("human: %s", h.Body)
	}
}

// TestParsePlanRealFormat is the core of web-plan-parser-unify: the web parser is
// now a thin adapter over internal/plan, so a REAL H2-header PLAN.md (the format
// the runner actually writes, with session/heartbeat claim metadata) parses —
// where the old bullet parser yielded zero items. It asserts task count,
// statuses, deps, heartbeat, the derived Doc, and the NEEDS-HUMAN/pending counts,
// and that active vs stale is derived from session+heartbeat freshness vs the TTL
// (there is no IN-PROGRESS status). now is fixed so active/stale is deterministic.
func TestParsePlanRealFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "PLAN.md")
	// t1: claimed, heartbeat 30m before now + ttl 60m => ACTIVE.
	// t2: claimed, heartbeat 3h before now => STALE (past TTL).
	// t3: NEEDS-HUMAN, unclaimed. t4: DONE, unclaimed.
	src := "<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n" +
		"## t1 [TODO] <!-- attempts=0 deps=t0,t9 weight=16 session=bee-A heartbeat=2026-06-30T11:30:00Z -->\n" +
		"implement t1\nFiles: a.go\nDoc: docs/tasks/t1.md\n\n" +
		"## t2 [NEEDS-REVIEW] <!-- attempts=1 deps= session=bee-B heartbeat=2026-06-30T09:00:00Z -->\n" +
		"review me\nDoc: docs/tasks/t2.md\n\n" +
		"## t3 [NEEDS-HUMAN] <!-- attempts=4 deps= -->\nstuck\n\n" +
		"## t4 [DONE] <!-- attempts=0 deps= -->\ndone\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	now := mustTime(t, "2026-06-30T12:00:00Z")
	ttl := time.Hour

	p, err := parsePlan(path, now, ttl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.ROIStamp != "deadbeef" {
		t.Fatalf("ROIStamp = %q, want deadbeef", p.ROIStamp)
	}
	if len(p.Items) != 4 {
		t.Fatalf("got %d items, want 4: %+v", len(p.Items), p.Items)
	}
	byID := map[string]PlanItem{}
	for _, it := range p.Items {
		byID[it.ID] = it
	}

	t1 := byID["t1"]
	if t1.Status != StatusTODO {
		t.Fatalf("t1 status = %q, want TODO", t1.Status)
	}
	if len(t1.Deps) != 2 || t1.Deps[0] != "t0" || t1.Deps[1] != "t9" {
		t.Fatalf("t1 deps = %v, want [t0 t9]", t1.Deps)
	}
	if t1.Desc != "implement t1" {
		t.Fatalf("t1 desc = %q, want 'implement t1'", t1.Desc)
	}
	if t1.Doc != "docs/tasks/t1.md" {
		t.Fatalf("t1 doc = %q", t1.Doc)
	}
	if !t1.Heartbeat.Equal(mustTime(t, "2026-06-30T11:30:00Z")) {
		t.Fatalf("t1 heartbeat = %v", t1.Heartbeat)
	}
	// Active vs stale is the session+heartbeat freshness, NOT a status.
	if !t1.Active || t1.Stale {
		t.Fatalf("t1 should be ACTIVE (fresh claim): active=%v stale=%v", t1.Active, t1.Stale)
	}
	if got := t1.Claim(); got != "active bee-A" {
		t.Fatalf("t1 claim = %q, want 'active bee-A'", got)
	}

	t2 := byID["t2"]
	if t2.Status != StatusReview {
		t.Fatalf("t2 status = %q, want NEEDS-REVIEW", t2.Status)
	}
	if t2.Active || !t2.Stale {
		t.Fatalf("t2 should be STALE (expired claim): active=%v stale=%v", t2.Active, t2.Stale)
	}
	if got := t2.Claim(); got != "stale bee-B" {
		t.Fatalf("t2 claim = %q, want 'stale bee-B'", got)
	}

	// t3 NEEDS-HUMAN, t4 DONE: both unclaimed => neither active nor stale.
	for _, id := range []string{"t3", "t4"} {
		if it := byID[id]; it.Active || it.Stale || it.Claim() != "" {
			t.Fatalf("%s unclaimed but active=%v stale=%v claim=%q", id, it.Active, it.Stale, it.Claim())
		}
	}

	// Count semantics the views use: pending = not DONE; human = NEEDS-HUMAN.
	pending, human := 0, 0
	for _, it := range p.Items {
		if it.Status != StatusDone {
			pending++
		}
		if it.Status == StatusHuman {
			human++
		}
	}
	if pending != 3 {
		t.Fatalf("pending = %d, want 3 (t1,t2,t3)", pending)
	}
	if human != 1 {
		t.Fatalf("needs-human = %d, want 1 (t3)", human)
	}
}

// TestParsePlanMissingFile: an absent PLAN.md is an empty plan, not an error
// (a freshly-added, pre-bootstrap submodule).
func TestParsePlanMissingFile(t *testing.T) {
	p, err := parsePlan(filepath.Join(t.TempDir(), "nope.md"), time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("missing file should be empty plan, got err %v", err)
	}
	if len(p.Items) != 0 || p.ROIStamp != "" {
		t.Fatalf("missing file should yield empty plan, got %+v", p)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return ts
}

func TestExplorerAndUnknown(t *testing.T) {
	s, _ := setup(t)
	if get(t, s, "/submodule/alpha").Code != 200 {
		t.Fatal("explorer")
	}
	if get(t, s, "/submodule/none").Code != 404 {
		t.Fatal("want 404")
	}
}

func TestROIRoundTrip(t *testing.T) {
	s, root := setup(t)
	form := url.Values{"body": {"# new intent\n"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/roi/alpha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Routes().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("roi post %d: %s", w.Code, w.Body)
	}
	b, _ := os.ReadFile(filepath.Join(root, "submodules", "alpha", repo.ROIFile))
	if string(b) != "# new intent\n" {
		t.Fatalf("roi not written: %q", b)
	}
}

func TestEnvDeploy(t *testing.T) {
	s, root := setup(t)
	form := url.Values{"target": {"green"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/env/deploy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Routes().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("deploy %d", w.Code)
	}
	b, _ := os.ReadFile(filepath.Join(root, repo.InfraFile))
	if !strings.Contains(string(b), "Active: green") {
		t.Fatalf("env: %q", b)
	}
}

func TestSecretsEmpty(t *testing.T) {
	s, _ := setup(t)
	keys, err := listSecretKeys(context.Background(), "x", filepath.Join(t.TempDir(), repo.SecretsFile))
	if err != nil || keys != nil {
		t.Fatalf("want empty: %v %v", keys, err)
	}
	if get(t, s, "/secrets").Code != 200 {
		t.Fatal("secrets get")
	}
}

func TestBranchesStamp(t *testing.T) {
	s, root := setup(t)
	rd := filepath.Join(root, "submodules", "alpha", "repo")
	os.MkdirAll(rd, 0o755)
	for _, a := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", a...)
		c.Dir = rd
		c.Run()
	}
	os.WriteFile(filepath.Join(rd, "f.txt"), []byte("x"), 0o644)
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-m", "work\n\nBeehive: t1 br-t1.md"}} {
		c := exec.Command("git", a...)
		c.Dir = rd
		c.Run()
	}
	w := get(t, s, "/submodule/alpha/branches")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "br-t1.md") {
		t.Fatalf("branches %d: %s", w.Code, w.Body)
	}
}

// srcRepo makes a throwaway git repo with one commit on branch main, usable as a
// (file-protocol) submodule url for an offline `git submodule add`.
func srcRepo(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	os.MkdirAll(p, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", a...)
		c.Dir = p
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	os.WriteFile(filepath.Join(p, "f.txt"), []byte("x"), 0o644)
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-qm", "init"}} {
		c := exec.Command("git", a...)
		c.Dir = p
		c.Run()
	}
	return p
}

func postForm(t *testing.T, s *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Routes().ServeHTTP(w, req)
	return w
}

// TestSubmoduleAdd proves the web add now creates a real tracked git submodule
// (.gitmodules entry + checked-out repo/), not the old inert bare dir.
func TestSubmoduleAdd(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	s, root := setup(t)
	src := srcRepo(t, "beta")

	w := postForm(t, s, "/submodule/add", url.Values{"url": {src}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add %d: %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "submodules", "beta", "repo", "f.txt")); err != nil {
		t.Fatalf("submodule repo not checked out: %v", err)
	}
	gm, _ := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if !strings.Contains(string(gm), "submodules/beta/repo") {
		t.Fatalf(".gitmodules missing tracked entry:\n%s", gm)
	}
}

func TestSubmoduleAddRejectsBadInput(t *testing.T) {
	s, _ := setup(t)
	if w := postForm(t, s, "/submodule/add", url.Values{}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing url: %d", w.Code)
	}
	if w := postForm(t, s, "/submodule/add", url.Values{"url": {"git@h:o/r.git"}, "name": {"../evil"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", w.Code)
	}
}

// TestSubmoduleLink proves the web link now writes schema-valid YAML through the
// cycle-checked links API (not a raw `from: [to]` append).
func TestSubmoduleLink(t *testing.T) {
	s, root := setup(t)
	if w := postForm(t, s, "/submodule/link", url.Values{"from": {"x"}, "to": {"y"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("link %d: %s", w.Code, w.Body)
	}
	l, err := links.Load(filepath.Join(root, repo.LinksFile))
	if err != nil {
		t.Fatalf("links file not schema-valid: %v", err)
	}
	if len(l.Deps) != 1 || l.Deps[0].From != "x" || l.Deps[0].To != "y" {
		t.Fatalf("deps = %v, want one x->y edge", l.Deps)
	}
}

func TestSubmoduleLinkRejectsCycle(t *testing.T) {
	s, root := setup(t)
	if w := postForm(t, s, "/submodule/link", url.Values{"from": {"a"}, "to": {"b"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("a->b: %d", w.Code)
	}
	if w := postForm(t, s, "/submodule/link", url.Values{"from": {"b"}, "to": {"c"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("b->c: %d", w.Code)
	}
	// c->a closes the cycle and must be rejected (not persisted).
	if w := postForm(t, s, "/submodule/link", url.Values{"from": {"c"}, "to": {"a"}}); w.Code < 400 {
		t.Fatalf("cycle c->a should be rejected, got %d", w.Code)
	}
	l, _ := links.Load(filepath.Join(root, repo.LinksFile))
	if l.HasCycle() {
		t.Fatal("rejected cycle edge was persisted")
	}
}

// TestAssetsStyleServed locks the design-system contract: the stylesheet is
// still embedded and served at /assets/style.css, exposes a token root with a
// dark-mode override, and defines a status pill class per task state plus the
// `.active` overlay. Downstream views (dashboard-cards, plan-view-pills) emit
// these exact class names, so a rename here must break this test on purpose.
func TestAssetsStyleServed(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/assets/style.css")
	if w.Code != 200 {
		t.Fatalf("style.css status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Fatalf("content-type = %q, want text/css", ct)
	}
	body := w.Body.String()
	must := []string{
		":root",                      // token root
		"prefers-color-scheme: dark", // dark mode overrides
		".status-todo",
		".status-needs-review",
		".status-needs-arbitration",
		".status-done",
		".status-needs-human",
		".active", // session+heartbeat overlay (no IN-PROGRESS status)
	}
	for _, m := range must {
		if !strings.Contains(body, m) {
			t.Fatalf("style.css missing %q", m)
		}
	}
}

// TestRenderMarkdownSanitized is the core of editor-markdown-render: markdown
// renders to the expected HTML (headings/lists/code/emphasis) AND repo content
// is sanitized — a <script> block is dropped (not passed through) and a
// javascript: link protocol is stripped. The renderer is pure-Go/CGO-free
// (goldmark, no html.WithUnsafe).
func TestRenderMarkdownSanitized(t *testing.T) {
	src := "# Title\n\nIntro **bold** text.\n\n- one\n- two\n\n```\ncode block\n```\n\nInline `x` here.\n\n<script>alert('xss')</script>\n\n[evil](javascript:alert(1))\n\n[ok](https://example.com)\n"
	out := string(renderMarkdown(src))
	for _, want := range []string{"<h1>Title</h1>", "<li>one</li>", "<code>", "<strong>bold</strong>", `href="https://example.com"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered markdown missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>") {
		t.Fatalf("script tag was not sanitized out:\n%s", out)
	}
	if strings.Contains(out, "javascript:") {
		t.Fatalf("dangerous link protocol was not stripped:\n%s", out)
	}
}

// TestExplorerRendersMarkdown proves the explorer VIEW pane now renders doc
// markdown to HTML inside a .markdown container instead of dumping raw source
// into a <pre>. The alpha ROI ("# alpha") must surface as an <h1>.
func TestExplorerRendersMarkdown(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/submodule/alpha")
	if w.Code != 200 {
		t.Fatalf("explorer %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<div class="markdown">`) {
		t.Fatalf("explorer not rendering into a .markdown container:\n%s", body)
	}
	if !strings.Contains(body, "<h1>alpha</h1>") {
		t.Fatalf("ROI markdown heading not rendered:\n%s", body)
	}
	if strings.Contains(body, "<pre># alpha") {
		t.Fatalf("explorer still dumping raw markdown source:\n%s", body)
	}
}

// TestROIViewRendersAndRawVerbatim locks the dual contract for the editor: the
// ROI VIEW shows a rendered preview while the editable textarea keeps the RAW
// source verbatim (the edit round-trip must not be lost to rendering).
func TestROIViewRendersAndRawVerbatim(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/roi/alpha")
	if w.Code != 200 {
		t.Fatalf("roi get %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="markdown"`) || !strings.Contains(body, "<h1>alpha</h1>") {
		t.Fatalf("ROI view missing rendered preview:\n%s", body)
	}
	if !strings.Contains(body, "<textarea") || !strings.Contains(body, "# alpha") {
		t.Fatalf("ROI editable textarea missing verbatim raw source:\n%s", body)
	}
}

// commitRepoAt inits a git repo at dir and lays down one commit per message
// (each appended to f.txt so every commit changes the tree, even on repeats).
// A message may carry a stamp body, e.g. "subj\n\nBeehive: <task> <doc>".
func commitRepoAt(t *testing.T, dir string, msgs ...string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	body := ""
	for _, m := range msgs {
		body += m + "\n"
		os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0o644)
		git("add", "-A")
		git("commit", "-q", "-m", m)
	}
}

// TestSectionByDate is the unit core of the "sectioned" requirement: commits
// (newest-first, as git log emits them) group into one section per date,
// preserving order and membership, with no cross-date bleed.
func TestSectionByDate(t *testing.T) {
	cs := []Commit{
		{SHA: "a", Date: "2026-06-30", Subject: "x"},
		{SHA: "b", Date: "2026-06-30", Subject: "y"},
		{SHA: "c", Date: "2026-06-29", Subject: "z"},
	}
	secs := sectionByDate(cs)
	if len(secs) != 2 {
		t.Fatalf("want 2 date sections, got %d: %+v", len(secs), secs)
	}
	if secs[0].Date != "2026-06-30" || len(secs[0].Commits) != 2 {
		t.Fatalf("section 0 = %+v, want 2026-06-30 with 2 commits", secs[0])
	}
	if secs[1].Date != "2026-06-29" || len(secs[1].Commits) != 1 || secs[1].Commits[0].SHA != "c" {
		t.Fatalf("section 1 = %+v, want 2026-06-29 with only c", secs[1])
	}
}

// TestCommitGraphPagination drives commitGraph against a real repo: it parses
// SHA/subject and the Beehive doc stamp, and offset/limit bound the page
// (newest-first). It reads ONE repoDir, so it can never crawl submodules.
func TestCommitGraphPagination(t *testing.T) {
	rd := filepath.Join(t.TempDir(), "repo")
	commitRepoAt(t, rd, "c1", "c2", "c3 subj\n\nBeehive: task3 docs/bee-c3.md")
	page1, err := commitGraph(context.Background(), rd, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || page1[0].Subject != "c3 subj" || page1[1].Subject != "c2" {
		t.Fatalf("page1 = %+v, want [c3 subj, c2]", page1)
	}
	if page1[0].SHA == "" || len(page1[0].SHA) > 12 {
		t.Fatalf("SHA not parsed/bounded: %q", page1[0].SHA)
	}
	if page1[0].DocTask != "task3" || page1[0].DocPath != "docs/bee-c3.md" {
		t.Fatalf("doc stamp not split: task=%q path=%q", page1[0].DocTask, page1[0].DocPath)
	}
	page2, err := commitGraph(context.Background(), rd, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].Subject != "c1" {
		t.Fatalf("page2 = %+v, want [c1]", page2)
	}
}

// TestBranchesSectionedScoped proves the rendered branch view is sectioned (a
// .card section with a date <h2>) AND scoped to ONE submodule: alpha's page
// shows alpha's commit and never beta's (no cross-submodule crawl).
func TestBranchesSectionedScoped(t *testing.T) {
	s, root := setup(t)
	commitRepoAt(t, filepath.Join(root, "submodules", "alpha", "repo"), "alpha-only-commit")
	commitRepoAt(t, filepath.Join(root, "submodules", "beta", "repo"), "beta-only-commit")

	w := get(t, s, "/submodule/alpha/branches")
	if w.Code != 200 {
		t.Fatalf("branches %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<section class="card">`) || !strings.Contains(body, "<h2>") {
		t.Fatalf("branch view is not sectioned:\n%s", body)
	}
	if !strings.Contains(body, "alpha-only-commit") {
		t.Fatalf("alpha commit missing from its own branch view:\n%s", body)
	}
	if strings.Contains(body, "beta-only-commit") {
		t.Fatalf("branch view crawled into another submodule (beta commit leaked):\n%s", body)
	}
}

// TestBranchesDocLink proves the commit-stamp linkage: a stamp whose doc exists
// under the submodule's docs/ renders a link to a doc view that serves the
// rendered markdown; a stamp whose doc is absent renders no (dead) link.
func TestBranchesDocLink(t *testing.T) {
	s, root := setup(t)
	docsDir := filepath.Join(root, "submodules", "alpha", "docs")
	os.MkdirAll(docsDir, 0o755)
	os.WriteFile(filepath.Join(docsDir, "bee-doc.md"), []byte("# Doc Heading\n\nbody text\n"), 0o644)
	commitRepoAt(t, filepath.Join(root, "submodules", "alpha", "repo"),
		"has doc\n\nBeehive: dtask docs/bee-doc.md",
		"no doc\n\nBeehive: mtask docs/missing.md")

	w := get(t, s, "/submodule/alpha/branches")
	body := w.Body.String()
	if !strings.Contains(body, `href="/submodule/alpha/doc/bee-doc.md"`) {
		t.Fatalf("resolved change-doc link missing:\n%s", body)
	}
	if strings.Contains(body, "/doc/missing.md") {
		t.Fatalf("absent change doc must not be linked:\n%s", body)
	}

	d := get(t, s, "/submodule/alpha/doc/bee-doc.md")
	if d.Code != 200 {
		t.Fatalf("doc view %d: %s", d.Code, d.Body)
	}
	if db := d.Body.String(); !strings.Contains(db, "<h1>Doc Heading</h1>") || !strings.Contains(db, `class="markdown"`) {
		t.Fatalf("doc view did not render markdown:\n%s", db)
	}
}

// TestDocViewGuards locks the doc handler's safety: a traversal-unsafe filename
// and a missing doc both 404 (never a read outside submodules/<sm>/docs/).
func TestDocViewGuards(t *testing.T) {
	s, _ := setup(t)
	if w := get(t, s, "/submodule/alpha/doc/a%20b.md"); w.Code != http.StatusNotFound {
		t.Fatalf("unsafe doc name: got %d, want 404", w.Code)
	}
	if w := get(t, s, "/submodule/alpha/doc/nope.md"); w.Code != http.StatusNotFound {
		t.Fatalf("missing doc: got %d, want 404", w.Code)
	}
}

// TestEditorDiffAddDelClasses confirms the chat-diff pane renders unified-diff
// rows with add/del/eq classes (styled by the design-system --diff-* tokens).
func TestEditorDiffAddDelClasses(t *testing.T) {
	s, _ := setup(t)
	rows := []editor.DiffRow{
		{Kind: "eq", HTML: template.HTML("context")},
		{Kind: "del", HTML: template.HTML("old")},
		{Kind: "add", HTML: template.HTML("new")},
	}
	out := renderTmpl(t, s, "editor_panel.html", map[string]interface{}{"ID": "e1", "File": "ROI.md", "Rows": rows})
	for _, want := range []string{`class="ln eq"`, `class="ln del"`, `class="ln add"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("editor diff missing %q:\n%s", want, out)
		}
	}
}

// TestSessionLivenessBranchGone is the regression for sessions that kept showing
// "running" / "(waiting for session output…)" long after the honeybee exited. A
// stub whose stream branch is gone is an ended session (its finalize never
// replaced the stub), not a live one.
func TestSessionLivenessBranchGone(t *testing.T) {
	s, root := setup(t)
	ctx := context.Background()
	gitRun := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// A base commit so we have something to branch from.
	os.WriteFile(filepath.Join(root, "seed"), []byte("x"), 0o644)
	gitRun("add", "-A")
	gitRun("commit", "-q", "-m", "seed")
	// Fresh stream branch for the "live" session (tip time ~now).
	gitRun("branch", "bee-live-stream")

	sessDir := filepath.Join(root, "submodules", "alpha", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(sessDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bee-live.md", repo.SessionStub("bee-live-stream")) // branch exists, fresh -> live
	write("bee-dead.md", repo.SessionStub("bee-dead-stream")) // branch gone -> NOT live
	write("bee-final.md", "# final transcript\nall done.\n")  // non-stub -> NOT live

	infos := s.sessionInfos(ctx, sessDir, time.Now())
	got := map[string]bool{}
	for _, in := range infos {
		got[in.ID] = in.Live
	}
	if !got["bee-live"] {
		t.Errorf("bee-live: want Live=true (fresh stream branch)")
	}
	if got["bee-dead"] {
		t.Errorf("bee-dead: want Live=false (stream branch gone), got true — the orphaned-stub bug")
	}
	if got["bee-final"] {
		t.Errorf("bee-final: want Live=false (finished non-stub), got true")
	}

	// Body of the ended session must say so, not pretend it is still starting up.
	w := get(t, s, "/submodule/alpha/session/bee-dead/body")
	if w.Code != 200 {
		t.Fatalf("body status %d", w.Code)
	}
	if b := w.Body.String(); !strings.Contains(b, "session ended") {
		t.Errorf("ended-session body should explain the branch is gone, got: %q", b)
	}
}
