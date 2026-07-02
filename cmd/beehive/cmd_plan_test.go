package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
)

// fatPlan is a submodule PLAN.md with one fat DONE card (description +
// Files/Doc/Accept + Impl/Review narrative) and one claimed OPEN TODO. Archiving
// must lift only the DONE narrative and leave the OPEN card — claim and all —
// byte-intact.
const fatPlan = `<!-- Beehive-ROI: abc123 -->
# Plan

## alpha [DONE] <!-- attempts=0 deps= weight=32 -->
Add Fetch, Pull, Push to git. FOUNDATION for the claim race.
Files: internal/git/git.go, internal/git/git_test.go.
Doc: docs/tasks/alpha.md
Accept: ctx-aware wrappers with real error surfacing; unit tests.
Impl (bee-alpha, commit c21a4f0, pushed origin): added --prune to Fetch and a new
Pull running pull --ff-only. Tests green under CGO_ENABLED=0.
Review (approved, beehive-123): verified branch bee-alpha vs task + ROI. Merged.

## beta [TODO] <!-- attempts=1 deps=alpha session=bee-9 heartbeat=2026-06-29T10:00:00Z -->
Make the claim lock real. After Commit and Heartbeat: pull main, verify our stamp.
Files: internal/claim/claim.go.
Doc: docs/tasks/beta.md
Accept: two-claimer race yields exactly one winner.
`

// initPlanRepo lays down a minimal beehive repo (AGENTS.md + one submodule with
// PLAN.md) as a committed git tree, returning the root and a git handle so a test
// can drive archivePlan against a real working tree.
func initPlanRepo(t *testing.T, planBody string) (string, *git.Repo, context.Context) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	r := git.New(root)
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if _, err := r.Run(ctx, a...); err != nil {
			t.Fatalf("git setup %v: %v", a, err)
		}
	}
	writeAt(t, filepath.Join(root, "AGENTS.md"), "# beehive test repo\n")
	writeAt(t, filepath.Join(root, "submodules", "beehive", "PLAN.md"), planBody)
	if _, err := r.Run(ctx, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := r.Run(ctx, "commit", "-q", "-m", "baseline"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return root, r, ctx
}

func writeAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArchivePlanLeansAndCommits(t *testing.T) {
	root, r, ctx := initPlanRepo(t, fatPlan)
	head0, err := r.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	res, err := archivePlan(ctx, root, "")
	if err != nil {
		t.Fatalf("archivePlan: %v", err)
	}

	// It reports exactly the DONE card, on the sole submodule, and a real shrink.
	if res.Submodule != "beehive" {
		t.Fatalf("submodule = %q, want beehive", res.Submodule)
	}
	if strings.Join(res.IDs, ",") != "alpha" {
		t.Fatalf("archived IDs = %v, want [alpha]", res.IDs)
	}
	if res.AfterBytes >= res.BeforeBytes {
		t.Fatalf("no shrink: before=%d after=%d", res.BeforeBytes, res.AfterBytes)
	}

	// The on-disk PLAN.md keeps the DONE card but drops its narrative...
	planPath := filepath.Join(root, "submodules", "beehive", "PLAN.md")
	leaned := readAt(t, planPath)
	for _, keep := range []string{
		"## alpha [DONE] <!-- attempts=0 deps= weight=32 -->",
		"Add Fetch, Pull, Push to git.",
		"Files: internal/git/git.go, internal/git/git_test.go.",
		"Accept: ctx-aware wrappers with real error surfacing; unit tests.",
	} {
		if !strings.Contains(leaned, keep) {
			t.Fatalf("leaned PLAN.md dropped %q:\n%s", keep, leaned)
		}
	}
	for _, gone := range []string{"Impl (bee-alpha", "Review (approved, beehive-123)", "pull --ff-only"} {
		if strings.Contains(leaned, gone) {
			t.Fatalf("leaned PLAN.md still carries narrative %q:\n%s", gone, leaned)
		}
	}
	// ...and leaves the OPEN task's card and claim byte-intact.
	for _, keep := range []string{
		"## beta [TODO] <!-- attempts=1 deps=alpha session=bee-9 heartbeat=2026-06-29T10:00:00Z -->",
		"Make the claim lock real.",
		"Accept: two-claimer race yields exactly one winner.",
	} {
		if !strings.Contains(leaned, keep) {
			t.Fatalf("leaned PLAN.md altered the OPEN task, missing %q:\n%s", keep, leaned)
		}
	}

	// The archive doc holds the header + preamble + moved narrative.
	doc := readAt(t, filepath.Join(root, "submodules", "beehive", "docs", "plan-archive", "alpha.md"))
	for _, want := range []string{
		"## alpha [DONE] <!-- attempts=0 deps= weight=32 -->",
		"beehive plan archive",
		"Impl (bee-alpha, commit c21a4f0, pushed origin)",
		"Review (approved, beehive-123)",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("archive doc missing %q:\n%s", want, doc)
		}
	}

	// The run committed its paths: HEAD advanced and the tree is clean.
	head1, err := r.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head1 == head0 {
		t.Fatal("archivePlan did not create a commit")
	}
	if clean, err := r.Clean(ctx); err != nil || !clean {
		t.Fatalf("tree not clean after archive (clean=%v err=%v)", clean, err)
	}

	// Idempotent: a second run finds nothing, writes nothing, commits nothing.
	res2, err := archivePlan(ctx, root, "")
	if err != nil {
		t.Fatalf("second archivePlan: %v", err)
	}
	if len(res2.IDs) != 0 {
		t.Fatalf("second run archived %v, want none", res2.IDs)
	}
	if now := readAt(t, planPath); now != leaned {
		t.Fatalf("second run mutated PLAN.md:\n%s", now)
	}
	head2, err := r.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head2 != head1 {
		t.Fatal("second run created a spurious commit")
	}
}

func readAt(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
