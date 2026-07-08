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
// inventory and refuses apply (no mutation path at all). It also locks that the
// inventory is per-submodule: no hive-wide "root" deploy-env line (blue/green is
// not a global concept).
func TestSkillResourcesReportOnly(t *testing.T) {
	s, _ := setup(t)
	plan := postForm(t, s, "/skills/resources/plan", url.Values{})
	if plan.Code != http.StatusOK {
		t.Fatalf("resources plan: got %d", plan.Code)
	}
	b := plan.Body.String()
	if !strings.Contains(b, "alpha") {
		t.Fatalf("resources plan missing submodule inventory:\n%s", b)
	}
	// The coordination root is not a deploy target: no "root: active ..." line.
	if strings.Contains(b, "root:") {
		t.Fatalf("resources plan must not present a hive-wide root deploy line:\n%s", b)
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

// TestSkillInfraConventionsAppliesExactPlan proves the non-destructive, diff-
// previewing skill normalizes each SUBMODULE's OWN INFRASTRUCTURE.md and never the
// hive coordination root (blue/green is per-submodule, not a global concept). With
// alpha lacking the markers and bravo already declaring them, the dry-run proposes
// the markers only for alpha's submodules/alpha/INFRASTRUCTURE.md; apply (no
// confirm) writes exactly that; bravo is byte-for-byte untouched; the root is never
// given deploy markers; and a second dry-run is a no-op.
func TestSkillInfraConventionsAppliesExactPlan(t *testing.T) {
	s, root := setup(t)
	// bravo already declares its own markers -> a no-op the skill must skip.
	bravo := filepath.Join(root, "submodules", "bravo")
	if err := os.MkdirAll(bravo, 0o755); err != nil {
		t.Fatal(err)
	}
	const bravoInfra = "# infra\nActive: green\nEnvironments: blue, green\n"
	if err := os.WriteFile(filepath.Join(bravo, repo.InfraFile), []byte(bravoInfra), 0o644); err != nil {
		t.Fatal(err)
	}
	rootInfra := filepath.Join(root, repo.InfraFile)
	alphaTarget := filepath.ToSlash(filepath.Join("submodules", "alpha", repo.InfraFile))

	plan := postForm(t, s, "/skills/infra-conventions/plan", url.Values{})
	if plan.Code != http.StatusOK {
		t.Fatalf("plan: got %d", plan.Code)
	}
	pb := plan.Body.String()
	// The proposed markers, scoped to alpha's OWN INFRASTRUCTURE.md path.
	for _, want := range []string{"Active: blue", "Environments: blue, green", alphaTarget} {
		if !strings.Contains(pb, want) {
			t.Fatalf("plan missing %q:\n%s", want, pb)
		}
	}
	// bravo already declares its markers -> never proposed (per-submodule scan).
	if strings.Contains(pb, "bravo") {
		t.Fatalf("plan must not propose the already-conventional bravo:\n%s", pb)
	}

	done := postForm(t, s, "/skills/infra-conventions/apply", url.Values{})
	if done.Code != http.StatusOK {
		t.Fatalf("apply: got %d body=%s", done.Code, done.Body)
	}
	// alpha's OWN doc got exactly the conventional markers.
	got, err := os.ReadFile(filepath.Join(root, "submodules", "alpha", repo.InfraFile))
	if err != nil {
		t.Fatalf("read applied alpha infra: %v", err)
	}
	if want := "Active: blue\nEnvironments: blue, green\n"; string(got) != want {
		t.Fatalf("alpha applied content = %q, want %q", string(got), want)
	}
	// bravo is byte-for-byte untouched by alpha's normalization.
	if bb, _ := os.ReadFile(filepath.Join(bravo, repo.InfraFile)); string(bb) != bravoInfra {
		t.Fatalf("bravo INFRASTRUCTURE.md changed: %q", bb)
	}
	// The coordination root's INFRASTRUCTURE.md stays EMPTY: infra-conventions
	// never writes blue/green deploy markers to the hive root (repo.Init seeds it
	// empty; a global write would have filled in "Active: ..."). This is the exact
	// inversion of the old global behavior.
	if rb, err := os.ReadFile(rootInfra); err != nil || string(rb) != "" {
		t.Fatalf("root INFRASTRUCTURE.md must stay empty (no blue/green markers), got %q err=%v", rb, err)
	}

	again := postForm(t, s, "/skills/infra-conventions/plan", url.Values{})
	if b := again.Body.String(); !strings.Contains(b, "already") {
		t.Fatalf("second plan should be a no-op:\n%s", b)
	}
}
