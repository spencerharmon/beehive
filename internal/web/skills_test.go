package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
)

// gitOut runs a git command in dir and returns its trimmed output, failing the
// test on error. Test fixtures build the leaked/drifted git state the maintenance
// skills reclaim (and read back HEAD/remote to assert what apply mutated).
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestSkillRegistryAndUnknown locks the registry: the four shipped skills are all
// registered and a lookup of an unknown name errors on both plan and apply
// (accept: "unknown skill errors").
func TestSkillRegistryAndUnknown(t *testing.T) {
	s, _ := setup(t)
	have := map[string]bool{}
	for _, sk := range s.skills.list() {
		have[sk.Name()] = true
	}
	for _, want := range []string{"resources", "infra-conventions", "gc", "cleanup-stale"} {
		if !have[want] {
			t.Errorf("registry missing skill %q (have %v)", want, have)
		}
	}
	if _, err := s.skills.plan(context.Background(), "bogus"); !errors.Is(err, errUnknownSkill) {
		t.Fatalf("plan(bogus) err = %v, want errUnknownSkill", err)
	}
	if _, err := s.skills.apply(context.Background(), "bogus", true); !errors.Is(err, errUnknownSkill) {
		t.Fatalf("apply(bogus) err = %v, want errUnknownSkill", err)
	}
}

// TestResourcesSkillReportOnly proves the report-only contract: resources returns
// a read-only rig report (hive root + each submodule), is neither actionable nor
// destructive, records no pending plan, and so has nothing to apply.
func TestResourcesSkillReportOnly(t *testing.T) {
	s, root := setup(t)
	if err := os.WriteFile(filepath.Join(root, "submodules", "alpha", repo.InfraFile),
		[]byte("# infra\nActive: green\nEnvironments: blue, green\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := s.skills.plan(context.Background(), "resources")
	if err != nil {
		t.Fatal(err)
	}
	if p.Actionable() || p.Destructive {
		t.Fatalf("resources must be report-only: actionable=%v destructive=%v", p.Actionable(), p.Destructive)
	}
	joined := strings.Join(p.Report, "\n")
	for _, want := range []string{"hive root", "alpha", "green"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("resources report missing %q:\n%s", want, joined)
		}
	}
	// A report-only dry-run stores no pending plan, so apply is a no-op error.
	if _, err := s.skills.apply(context.Background(), "resources", true); !errors.Is(err, errNoPendingPlan) {
		t.Fatalf("apply(resources) err = %v, want errNoPendingPlan", err)
	}
}

// TestInfraConventionsSkill proves the convention checker flags a real breach
// (Active not among Environments) and reports conformance once fixed, staying
// read-only throughout.
func TestInfraConventionsSkill(t *testing.T) {
	s, root := setup(t)
	infra := filepath.Join(root, "submodules", "alpha", repo.InfraFile)
	ctx := context.Background()

	if err := os.WriteFile(infra, []byte("# infra\nActive: red\nEnvironments: blue, green\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := s.skills.plan(ctx, "infra-conventions")
	if err != nil {
		t.Fatal(err)
	}
	if p.Actionable() || p.Destructive {
		t.Fatalf("infra-conventions must be report-only: %+v", p)
	}
	joined := strings.Join(p.Report, "\n")
	if !strings.Contains(joined, "alpha") || !strings.Contains(joined, "not one of Environments") {
		t.Fatalf("expected a membership violation for alpha:\n%s", joined)
	}

	// Fix the file: the report now states conformance instead of a violation.
	if err := os.WriteFile(infra, []byte("# infra\nActive: blue\nEnvironments: blue, green\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p2, err := s.skills.plan(ctx, "infra-conventions")
	if err != nil {
		t.Fatal(err)
	}
	if j := strings.Join(p2.Report, "\n"); !strings.Contains(j, "conform") {
		t.Fatalf("expected conformance report after fix:\n%s", j)
	}
}

// TestGCSkillReclaimsStaleWorktree drives the gc destructive path end to end: a
// deterministic dry-run that mutates nothing, a confirm gate (no destructive
// action without approval), and a confirmed apply that performs exactly the
// proposed removal and consumes the plan.
func TestGCSkillReclaimsStaleWorktree(t *testing.T) {
	s, root := setup(t)
	commitAll(t, root, "seed") // a HEAD so `git worktree list` is stable
	stale := filepath.Join(root, ".worktrees", "beehive-leaked")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Dry-run: deterministic (two runs equal) and non-mutating (dir survives).
	p1, err := s.skills.plan(ctx, "gc")
	if err != nil {
		t.Fatal(err)
	}
	if !p1.Actionable() || !p1.Destructive {
		t.Fatalf("gc plan should be actionable+destructive: %+v", p1)
	}
	if len(p1.Actions) != 1 || p1.Actions[0].Kind != actRemoveWorktreeDir || p1.Actions[0].Target != "beehive-leaked" {
		t.Fatalf("gc action = %+v, want one remove of beehive-leaked", p1.Actions)
	}
	p2, err := s.skills.plan(ctx, "gc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p1, p2) {
		t.Fatalf("gc dry-run not deterministic:\n%+v\nvs\n%+v", p1, p2)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("dry-run mutated the filesystem (dir gone): %v", err)
	}

	// No destructive action without confirmation: refused, dir survives.
	if _, err := s.skills.apply(ctx, "gc", false); !errors.Is(err, errSkillNeedsConfirm) {
		t.Fatalf("apply(confirm=false) err = %v, want errSkillNeedsConfirm", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("refused apply still removed the dir: %v", err)
	}

	// Confirmed apply performs exactly the proposed change.
	res, err := s.skills.apply(ctx, "gc", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 1 || res.Failed != 0 {
		t.Fatalf("apply result = %+v, want 1 done / 0 failed", res)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("confirmed apply did not remove the dir (err=%v)", err)
	}
	// The plan is consumed: a second apply has nothing pending.
	if _, err := s.skills.apply(ctx, "gc", true); !errors.Is(err, errNoPendingPlan) {
		t.Fatalf("second apply err = %v, want errNoPendingPlan", err)
	}
}

// TestCleanupStaleRevertsRemote drives cleanup-stale's remote path: a stray
// remote is previewed, survives the non-mutating dry-run, is refused without
// confirmation, and is removed by the confirmed apply — while origin is never a
// target.
func TestCleanupStaleRevertsRemote(t *testing.T) {
	s, root := setup(t)
	gitOut(t, root, "remote", "add", "stray", "https://example.invalid/x.git")
	ctx := context.Background()

	p, err := s.skills.plan(ctx, "cleanup-stale")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Actionable() || !p.Destructive {
		t.Fatalf("cleanup-stale plan should be actionable+destructive: %+v", p)
	}
	found := false
	for _, a := range p.Actions {
		if a.Kind == actRevertRemote && a.Target == "stray" {
			found = true
		}
		if a.Target == "origin" {
			t.Fatalf("origin must never be a revert target: %+v", a)
		}
	}
	if !found {
		t.Fatalf("expected a revert-remote action for 'stray': %+v", p.Actions)
	}
	if out := gitOut(t, root, "remote"); !strings.Contains(out, "stray") {
		t.Fatalf("dry-run removed the remote: %q", out)
	}

	if _, err := s.skills.apply(ctx, "cleanup-stale", false); !errors.Is(err, errSkillNeedsConfirm) {
		t.Fatalf("apply(confirm=false) err = %v, want errSkillNeedsConfirm", err)
	}
	if out := gitOut(t, root, "remote"); !strings.Contains(out, "stray") {
		t.Fatalf("refused apply removed the remote: %q", out)
	}

	if _, err := s.skills.apply(ctx, "cleanup-stale", true); err != nil {
		t.Fatal(err)
	}
	if out := gitOut(t, root, "remote"); strings.Contains(out, "stray") {
		t.Fatalf("confirmed apply did not remove the stray remote: %q", out)
	}
}

// TestCleanupStaleResyncsDriftedCheckout drives cleanup-stale's resync path: a
// declared submodule whose checkout HEAD drifted off its recorded gitlink is
// previewed with the FULL recorded SHA, survives the non-mutating dry-run, and is
// reset to the recorded commit by the confirmed apply.
func TestCleanupStaleResyncsDriftedCheckout(t *testing.T) {
	s, root := setup(t)
	sub := filepath.Join(root, "submodules", "beta", "repo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Declare beta so its drift is a resync target (not an orphan gitlink).
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"),
		[]byte("[submodule \"submodules/beta/repo\"]\n\tpath = submodules/beta/repo\n\turl = ./none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Real nested repo; record the gitlink at c1, then drift the checkout to c2.
	gitOut(t, sub, "init", "-q")
	gitOut(t, sub, "config", "user.email", "t@t")
	gitOut(t, sub, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, sub, "add", "-A")
	gitOut(t, sub, "commit", "-qm", "c1")
	c1 := gitOut(t, sub, "rev-parse", "HEAD")
	// Stage the gitlink at c1 into root's index (mode 160000).
	gitOut(t, root, "add", "submodules/beta/repo")
	// Drift the checkout: a second commit moves HEAD off the recorded gitlink.
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, sub, "add", "-A")
	gitOut(t, sub, "commit", "-qm", "c2")
	c2 := gitOut(t, sub, "rev-parse", "HEAD")
	if c1 == c2 {
		t.Fatal("precondition: c1 and c2 must differ")
	}
	ctx := context.Background()

	p, err := s.skills.plan(ctx, "cleanup-stale")
	if err != nil {
		t.Fatal(err)
	}
	var resync *SkillAction
	for i := range p.Actions {
		if p.Actions[i].Kind == actResyncCheckout && p.Actions[i].Target == "submodules/beta/repo" {
			resync = &p.Actions[i]
		}
	}
	if resync == nil {
		t.Fatalf("expected a resync action for the drifted checkout: %+v", p.Actions)
	}
	if resync.SHA != c1 {
		t.Fatalf("resync SHA = %q, want recorded gitlink %q", resync.SHA, c1)
	}
	if head := gitOut(t, sub, "rev-parse", "HEAD"); head != c2 {
		t.Fatalf("dry-run moved the checkout: %q", head)
	}

	if _, err := s.skills.apply(ctx, "cleanup-stale", true); err != nil {
		t.Fatal(err)
	}
	if head := gitOut(t, sub, "rev-parse", "HEAD"); head != c1 {
		t.Fatalf("checkout not resynced: head=%q want recorded %q", head, c1)
	}
}

// TestSkillsHTTPSurface locks the chat-skills web surface: the index lists every
// skill (and the nav link), a dry-run POST returns the preview fragment with a
// confirm-gated Apply control and mutates nothing, an unknown skill is a 404, a
// destructive apply without confirmation is a 400 that mutates nothing, and a
// confirmed apply performs the change.
func TestSkillsHTTPSurface(t *testing.T) {
	s, root := setup(t)
	commitAll(t, root, "seed")

	page := get(t, s, "/skills")
	if page.Code != 200 {
		t.Fatalf("GET /skills = %d", page.Code)
	}
	body := page.Body.String()
	for _, want := range []string{"resources", "infra-conventions", "gc", "cleanup-stale", `href="/skills"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("skills page missing %q:\n%s", want, body)
		}
	}

	leaked := filepath.Join(root, ".worktrees", "beehive-leaked")
	if err := os.MkdirAll(leaked, 0o755); err != nil {
		t.Fatal(err)
	}

	// Dry-run fragment: the action plus a destructive, confirm-gated Apply control.
	plan := postForm(t, s, "/skills/gc/plan", url.Values{})
	if plan.Code != 200 {
		t.Fatalf("POST gc/plan = %d: %s", plan.Code, plan.Body)
	}
	pb := plan.Body.String()
	for _, want := range []string{"beehive-leaked", `hx-post="/skills/gc/apply"`, `hx-vals='{"confirm":"yes"}'`, "hx-confirm="} {
		if !strings.Contains(pb, want) {
			t.Fatalf("gc dry-run fragment missing %q:\n%s", want, pb)
		}
	}
	if _, err := os.Stat(leaked); err != nil {
		t.Fatalf("dry-run removed the dir: %v", err)
	}

	// Unknown skill -> 404.
	if w := postForm(t, s, "/skills/bogus/plan", url.Values{}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown skill plan = %d, want 404", w.Code)
	}

	// Destructive apply without confirmation -> 400, dir survives.
	if w := postForm(t, s, "/skills/gc/apply", url.Values{}); w.Code != http.StatusBadRequest {
		t.Fatalf("gc apply without confirm = %d, want 400", w.Code)
	}
	if _, err := os.Stat(leaked); err != nil {
		t.Fatalf("refused apply removed the dir: %v", err)
	}

	// Confirmed apply -> 200, dir removed, result fragment renders the outcome.
	w := postForm(t, s, "/skills/gc/apply", url.Values{"confirm": {"yes"}})
	if w.Code != 200 {
		t.Fatalf("gc apply confirm = %d: %s", w.Code, w.Body)
	}
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Fatalf("confirmed apply did not remove the dir (err=%v)", err)
	}
	if rb := w.Body.String(); !strings.Contains(rb, "done") {
		t.Fatalf("apply result fragment missing outcome:\n%s", rb)
	}
}
