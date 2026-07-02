package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

const fatPlan = `<!-- Beehive-ROI: abc123 -->
# Plan

## t1 [DONE] <!-- attempts=0 deps= weight=32 -->
did the first thing to fix X.
Files: internal/x/x.go
Doc: docs/tasks/t1.md
Accept: X works; tests cover it.
Impl (bee-t1, commit abc1234, pushed origin): implemented the thing in x.go with
tests; CGO_ENABLED=0 go test ./... green, vet clean, static build OK.
Review (approved, bee-9): verified vs task + ROI, re-ran green, merged; pointer
bumped abc..def. Dependents now unblocked: t2.

## t2 [TODO] <!-- attempts=1 deps=t1 session=bee-9 heartbeat=2026-06-29T10:00:00Z -->
second, depends on t1
Files: internal/y/y.go
Doc: docs/tasks/t2.md
Accept: Y works.

## t3 [DONE] <!-- attempts=2 deps=t1 weight=16 -->
close via reconcile only.
Files: internal/z/z.go
Doc: docs/tasks/t3.md
Accept: Z shipped.
Reconciled (ROI bcda44a): SHIPPED. Closed as DONE; no further work.
`

// newSubmodule stages a submodule dir with the given PLAN.md under a temp root.
func newSubmodule(t *testing.T, planText string) repo.Submodule {
	t.Helper()
	root := t.TempDir()
	smDir := filepath.Join(root, "submodules", "beehive")
	if err := os.MkdirAll(smDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(smDir, repo.PlanFile), []byte(planText), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo.Submodule{Name: "beehive", Path: smDir}
}

func TestArchivePlanLeansAndPreserves(t *testing.T) {
	sm := newSubmodule(t, fatPlan)
	res, err := archivePlan(sm, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Archived, ",") != "t1,t3" {
		t.Fatalf("archived %v, want [t1 t3]", res.Archived)
	}
	if res.BytesTo >= res.BytesFrom {
		t.Fatalf("PLAN.md did not shrink: %d -> %d", res.BytesFrom, res.BytesTo)
	}

	// The narrative landed in the per-task archive docs, verbatim.
	arch := filepath.Join(sm.Path, "docs", "plan-archive", "t1.md")
	b, err := os.ReadFile(arch)
	if err != nil {
		t.Fatalf("archive doc missing: %v", err)
	}
	if !strings.Contains(string(b), "Impl (bee-t1") || !strings.Contains(string(b), "Review (approved, bee-9)") {
		t.Fatalf("t1 archive missing narrative:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(sm.Path, "docs", "plan-archive", "t2.md")); !os.IsNotExist(err) {
		t.Fatal("OPEN task t2 was archived")
	}

	// PLAN.md on disk lost the narrative but kept the cards + parses identically.
	leaned, err := os.ReadFile(sm.PlanPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(leaned), "Impl (bee-t1") || strings.Contains(string(leaned), "Reconciled (ROI") {
		t.Fatalf("PLAN.md still carries narrative:\n%s", leaned)
	}
	if !strings.Contains(string(leaned), "Accept: X works; tests cover it.") {
		t.Fatal("PLAN.md dropped the t1 card Accept line")
	}
	orig, _ := plan.Parse(fatPlan)
	got, _ := plan.Parse(string(leaned))
	if err := plan.SameMeta(orig, got); err != nil {
		t.Fatalf("leaned PLAN.md changed parsed metadata: %v", err)
	}
	// OPEN claim metadata intact after the on-disk rewrite.
	if tk := got.Task("t2"); tk.Session != "bee-9" || tk.Heartbeat.IsZero() {
		t.Fatalf("OPEN task claim disturbed on disk: %+v", tk)
	}
}

func TestArchivePlanIdempotent(t *testing.T) {
	sm := newSubmodule(t, fatPlan)
	if _, err := archivePlan(sm, false); err != nil {
		t.Fatal(err)
	}
	planAfter, _ := os.ReadFile(sm.PlanPath())
	archAfter, _ := os.ReadFile(filepath.Join(sm.Path, "docs", "plan-archive", "t1.md"))

	// Second pass: nothing to archive, and neither the plan nor the archive doc
	// is rewritten (byte-identical), so re-running is a true no-op.
	res, err := archivePlan(sm, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Archived) != 0 {
		t.Fatalf("second pass archived %v, want none", res.Archived)
	}
	planAgain, _ := os.ReadFile(sm.PlanPath())
	archAgain, _ := os.ReadFile(filepath.Join(sm.Path, "docs", "plan-archive", "t1.md"))
	if string(planAgain) != string(planAfter) {
		t.Fatal("second pass rewrote PLAN.md")
	}
	if string(archAgain) != string(archAfter) {
		t.Fatal("second pass rewrote the archive doc")
	}
}

func TestArchivePlanDryRunWritesNothing(t *testing.T) {
	sm := newSubmodule(t, fatPlan)
	before, _ := os.ReadFile(sm.PlanPath())
	res, err := archivePlan(sm, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Archived) != 2 {
		t.Fatalf("dry-run reported %v, want t1,t3", res.Archived)
	}
	after, _ := os.ReadFile(sm.PlanPath())
	if string(after) != string(before) {
		t.Fatal("dry-run mutated PLAN.md")
	}
	if _, err := os.Stat(filepath.Join(sm.Path, "docs", "plan-archive")); !os.IsNotExist(err) {
		t.Fatal("dry-run created the archive dir")
	}
}
