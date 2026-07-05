package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
)

// seedStaleWorktrees plants two unregistered edit-*/beehive-* directories under
// .worktrees (the stale-worktree cruft cleanup-stale removes) plus a
// non-matching directory that must never be touched, after a base commit so git
// worktree queries resolve a HEAD. Returns the stale names and the keep name.
func seedStaleWorktrees(t *testing.T, root string) (stale []string, keep string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "seed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	hygGit(t, root, "add", "-A")
	hygGit(t, root, "commit", "-q", "-m", "seed")
	wtDir := filepath.Join(root, ".worktrees")
	stale = []string{"edit-roi-alpha-999", "beehive-1782800000-222"}
	keep = "random-keep"
	for _, n := range append(append([]string{}, stale...), keep) {
		if err := os.MkdirAll(filepath.Join(wtDir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return stale, keep
}

// TestSkillsPageListsSkills locks that the index renders every registered skill
// with a dry-run control, so each maintenance action is discoverable.
func TestSkillsPageListsSkills(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("skills page: got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"cleanup-stale", "gc", "resources", "infra-conventions", "Dry-run"} {
		if !strings.Contains(body, want) {
			t.Fatalf("skills page missing %q:\n%s", want, body)
		}
	}
}

// TestSkillUnknownIs404 is the "unknown skill errors" acceptance: neither
// dry-run nor apply resolves an unregistered name.
func TestSkillUnknownIs404(t *testing.T) {
	s, _ := setup(t)
	if w := postForm(t, s, "/skills/nope/plan", url.Values{}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown plan: got %d want 404", w.Code)
	}
	if w := postForm(t, s, "/skills/nope/apply", url.Values{"confirm": {"on"}}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown apply: got %d want 404", w.Code)
	}
}

// TestSkillResourcesReportOnly proves a report-only skill produces a dry-run
// inventory and refuses apply (no mutation path at all).
func TestSkillResourcesReportOnly(t *testing.T) {
	s, _ := setup(t)
	plan := postForm(t, s, "/skills/resources/plan", url.Values{})
	if plan.Code != http.StatusOK {
		t.Fatalf("resources plan: got %d", plan.Code)
	}
	if b := plan.Body.String(); !strings.Contains(b, "alpha") {
		t.Fatalf("resources plan missing submodule inventory:\n%s", b)
	}
	if w := postForm(t, s, "/skills/resources/apply", url.Values{"confirm": {"on"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("report-only apply: got %d want 400", w.Code)
	}
}

// TestSkillCleanupStaleConfirmGateAndApply is the destructive-skill acceptance:
// the dry-run lists exactly the stale dirs without mutating, an unconfirmed
// apply refuses (mutating nothing), and only a confirmed apply performs
// precisely the proposed removals while sparing the non-matching directory.
func TestSkillCleanupStaleConfirmGateAndApply(t *testing.T) {
	s, root := setup(t)
	stale, keep := seedStaleWorktrees(t, root)
	exists := func(n string) bool {
		_, err := os.Stat(filepath.Join(root, ".worktrees", n))
		return err == nil
	}

	// Dry-run: lists exactly the stale dirs as remove actions; mutates nothing.
	plan := postForm(t, s, "/skills/cleanup-stale/plan", url.Values{})
	if plan.Code != http.StatusOK {
		t.Fatalf("plan: got %d", plan.Code)
	}
	pb := plan.Body.String()
	if !strings.Contains(pb, "remove") {
		t.Fatalf("plan missing remove action:\n%s", pb)
	}
	for _, n := range stale {
		if !strings.Contains(pb, n) {
			t.Fatalf("plan missing stale %q:\n%s", n, pb)
		}
		if !exists(n) {
			t.Fatalf("dry-run must not remove %q", n)
		}
	}

	// Apply WITHOUT confirm: the gate refuses, asks to confirm, mutates nothing.
	gate := postForm(t, s, "/skills/cleanup-stale/apply", url.Values{})
	if gate.Code != http.StatusOK {
		t.Fatalf("unconfirmed apply: got %d want 200", gate.Code)
	}
	if b := gate.Body.String(); !strings.Contains(b, "Confirmation required") {
		t.Fatalf("unconfirmed apply must ask to confirm:\n%s", b)
	}
	for _, n := range stale {
		if !exists(n) {
			t.Fatalf("unconfirmed apply must not remove %q", n)
		}
	}

	// Apply WITH confirm: removes exactly the stale dirs, keeps the non-matching.
	done := postForm(t, s, "/skills/cleanup-stale/apply", url.Values{"confirm": {"on"}})
	if done.Code != http.StatusOK {
		t.Fatalf("confirmed apply: got %d", done.Code)
	}
	if b := done.Body.String(); !strings.Contains(b, "applied") {
		t.Fatalf("confirmed apply must report applied:\n%s", b)
	}
	for _, n := range stale {
		if exists(n) {
			t.Fatalf("confirmed apply must remove %q", n)
		}
	}
	if !exists(keep) {
		t.Fatalf("confirmed apply must keep non-matching %q", keep)
	}
}

// TestSkillInfraConventionsAppliesExactPlan proves a non-destructive, diff-
// previewing skill: the dry-run shows the proposed markers, apply (no confirm
// needed) writes exactly that content, and a second dry-run is a no-op.
func TestSkillInfraConventionsAppliesExactPlan(t *testing.T) {
	s, root := setup(t)
	plan := postForm(t, s, "/skills/infra-conventions/plan", url.Values{})
	if plan.Code != http.StatusOK {
		t.Fatalf("plan: got %d", plan.Code)
	}
	pb := plan.Body.String()
	for _, want := range []string{"Active: blue", "Environments: blue, green"} {
		if !strings.Contains(pb, want) {
			t.Fatalf("plan diff missing %q:\n%s", want, pb)
		}
	}
	done := postForm(t, s, "/skills/infra-conventions/apply", url.Values{})
	if done.Code != http.StatusOK {
		t.Fatalf("apply: got %d body=%s", done.Code, done.Body)
	}
	got, err := os.ReadFile(filepath.Join(root, repo.InfraFile))
	if err != nil {
		t.Fatalf("read applied infra: %v", err)
	}
	if want := "Active: blue\nEnvironments: blue, green\n"; string(got) != want {
		t.Fatalf("applied content = %q, want %q", string(got), want)
	}
	again := postForm(t, s, "/skills/infra-conventions/plan", url.Values{})
	if b := again.Body.String(); !strings.Contains(b, "already") {
		t.Fatalf("second plan should be a no-op:\n%s", b)
	}
}
