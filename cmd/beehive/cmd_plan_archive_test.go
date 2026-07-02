package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
)

const fatPlan = `<!-- Beehive-ROI: abc123 -->
# Plan

## done1 [DONE] <!-- attempts=0 deps= weight=32 -->
Add the thing to internal/x. FOUNDATION for the next thing.
Files: internal/x/x.go, internal/x/x_test.go.
Doc: docs/tasks/done1.md
Accept: it does the thing with tests.
Impl (bee-done1, commit abc1234, pushed origin): implemented the thing across three
files with a bounded backoff and surfaced errors. Tests green under CGO_ENABLED=0.
Review (approved, beehive-42): verified branch bee-done1 vs task + ROI. Accept met
field-by-field. Re-ran go test ./... GREEN. MERGED; pointer bumped. No dependents.

## todo1 [TODO] <!-- attempts=0 deps= weight=32 session=bee-live heartbeat=2026-07-02T14:00:00Z -->
Still in flight; must not be archived.
Files: internal/y/y.go.
Doc: docs/tasks/todo1.md
Accept: the guard propagates.
`

func TestArchivePlanCmd(t *testing.T) {
	root := t.TempDir()
	g := git.New(root)
	ctx := context.Background()
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if _, err := g.Run(ctx, a...); err != nil {
			t.Fatalf("git %v: %v", a, err)
		}
	}
	sm := filepath.Join(root, "submodules", "sm")
	if err := os.MkdirAll(sm, 0o755); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	if err := os.WriteFile(planPath, []byte(fatPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.CommitPaths(ctx, "seed plan", "submodules/sm/PLAN.md"); err != nil {
		t.Fatal(err)
	}

	n, err := archivePlan(ctx, root, "sm")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("archived %d tasks, want 1 (only done1)", n)
	}

	// The archive file holds the moved narrative.
	arch := filepath.Join(sm, "docs", "plan-archive", "done1.md")
	ab, err := os.ReadFile(arch)
	if err != nil {
		t.Fatalf("archive file: %v", err)
	}
	if !strings.Contains(string(ab), "Impl (bee-done1") || !strings.Contains(string(ab), "Review (approved") {
		t.Fatalf("archive missing narrative:\n%s", ab)
	}

	// PLAN.md is leaned: narrative gone, card + pointer kept, OPEN task intact,
	// still parses, materially smaller.
	lb, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	ltext := string(lb)
	if strings.Contains(ltext, "Impl (bee-done1") || strings.Contains(ltext, "Review (approved") {
		t.Fatalf("PLAN.md still holds narrative:\n%s", ltext)
	}
	if !strings.Contains(ltext, "Archived: docs/plan-archive/done1.md") {
		t.Fatalf("PLAN.md missing archive pointer:\n%s", ltext)
	}
	if !strings.Contains(ltext, "Still in flight; must not be archived.") {
		t.Fatalf("OPEN task body altered:\n%s", ltext)
	}
	if len(ltext) >= len(fatPlan) {
		t.Fatalf("PLAN.md not shrunk: %d -> %d bytes", len(fatPlan), len(ltext))
	}
	pp, err := plan.Parse(ltext)
	if err != nil {
		t.Fatalf("leaned PLAN.md no longer parses: %v", err)
	}
	if len(pp.Tasks) != 2 || pp.Task("done1").Status != plan.StatusDone || pp.Task("todo1").Session != "bee-live" {
		t.Fatalf("leaned plan lost task/claim state: %+v", pp.Tasks)
	}

	// The write was published (committed), not left dirty in the tree.
	out, err := g.Run(ctx, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("archive left uncommitted changes:\n%s", out)
	}

	// Idempotent: a second run archives nothing and creates no new commit.
	head1, err := g.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	n2, err := archivePlan(ctx, root, "sm")
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second run archived %d, want 0 (no-op)", n2)
	}
	head2, err := g.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(head1) != strings.TrimSpace(head2) {
		t.Fatalf("idempotent re-run created a commit: %s -> %s", head1, head2)
	}
}
