package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/web"
)

// TestDaemonServesResolvedRegistry exercises the daemon's real serving path end
// to end for the bare single-host install: config.ResolveRegistry synthesizes a
// one-entry registry from --repo, web.NewMulti builds the server, and Routes
// serves the dashboard. It replaces the old serveTarget bridge tests — routing is
// now web.Multi's job (see internal/web/web_test.go) and the daemon just hands it
// the resolved registry. It also locks the back-compat guarantee: a single
// registered repo keeps today's flat routes with NO /repo/ switcher, so an
// unconfigured host is unchanged by the registry indirection.
func TestDaemonServesResolvedRegistry(t *testing.T) {
	// No repos.yaml under the host config dir: a bare single-repo install.
	t.Setenv("BEEHIVE_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	// The read-only dashboard/hygiene views run git on the root.
	for _, a := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", a...)
		c.Dir = root
		if err := c.Run(); err != nil {
			t.Fatalf("git %v: %v", a, err)
		}
	}
	sm := filepath.Join(root, "submodules", "alpha")
	if err := os.MkdirAll(sm, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sm, repo.ROIFile), []byte("# alpha\n"), 0o644)
	os.WriteFile(filepath.Join(sm, repo.PlanFile), []byte(
		"<!-- Beehive-ROI: abc -->\n# Plan\n\n"+
			"## t1 [TODO] <!-- attempts=0 deps= -->\ndo it\nDoc: d.md\n"), 0o644)

	reg, err := config.ResolveRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reg.Repos); got != 1 {
		t.Fatalf("bare install should synthesize a one-entry registry, got %d", got)
	}
	s, err := web.NewMulti(reg)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Routes()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard = %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "alpha") {
		t.Fatalf("dashboard should list the submodule; body=%s", w.Body)
	}

	// Single-repo mode keeps flat routes: the multi-repo /repo/ switch endpoint
	// is not exposed, so a switch attempt 404s.
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest("POST", "/repo/alpha", nil))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("single-repo mode must not expose the /repo/ switch, got %d", w2.Code)
	}
}
