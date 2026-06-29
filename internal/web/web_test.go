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

func TestSubmoduleAdd(t *testing.T) {
	s, root := setup(t)
	form := url.Values{"name": {"beta"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/submodule/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "submodules", "beta")); err != nil {
		t.Fatal("beta not created")
	}
}
