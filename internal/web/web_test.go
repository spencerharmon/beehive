package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
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
	os.WriteFile(filepath.Join(sm, repo.PlanFile), []byte(
		"<!-- Beehive-ROI: abc123 -->\n- TODO t1 build deps: t0 doc: br-t1.md\n- NEEDS-HUMAN t2 stuck\n- DONE t3 ok\n"), 0o644)
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
	if !strings.Contains(b, "abc123") || !strings.Contains(b, "t1") {
		t.Fatalf("plan: %s", b)
	}
	h := get(t, s, "/human")
	if !strings.Contains(h.Body.String(), "t2") || strings.Contains(h.Body.String(), "t1<") {
		t.Fatalf("human: %s", h.Body)
	}
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
