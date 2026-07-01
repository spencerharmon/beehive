package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
)

const cliPlanSample = `<!-- Beehive-ROI: abc123 -->
# Plan

## a1 [DONE] <!-- attempts=0 deps= weight=16 -->
Do a1.
Files: a.go
Doc: docs/tasks/a1.md
Accept: works.
Impl (bee-a1, commit abc): implemented across
two lines of narrative.
Review (approved, bee-9): merged; pointer bumped.

## a2 [TODO] <!-- attempts=0 deps=a1 session=bee-7 heartbeat=2026-06-29T10:00:00Z -->
Do a2.
Files: b.go
Doc: docs/tasks/a2.md
Accept: works too.
`

func TestArchivePlan(t *testing.T) {
	root := t.TempDir()
	const sub = "beehive"
	subDir := filepath.Join(root, "submodules", sub)
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(subDir, "PLAN.md")
	if err := os.WriteFile(planPath, []byte(cliPlanSample), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, changed, before, after, err := archivePlan(root, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "a1" {
		t.Fatalf("archived ids = %v, want [a1]", ids)
	}
	if after >= before {
		t.Fatalf("PLAN.md not shrunk: %d -> %d bytes", before, after)
	}

	// The narrative landed at the exact archive path.
	db, err := os.ReadFile(filepath.Join(subDir, plan.ArchivePath("a1")))
	if err != nil {
		t.Fatalf("archive doc not written: %v", err)
	}
	if !strings.Contains(string(db), "Impl (bee-a1, commit abc)") ||
		!strings.Contains(string(db), "Review (approved, bee-9)") {
		t.Fatalf("archive doc missing narrative:\n%s", db)
	}

	// changed paths are repo-relative and cover exactly PLAN.md + the doc.
	want := map[string]bool{
		filepath.Join("submodules", sub, "PLAN.md"):              false,
		filepath.Join("submodules", sub, plan.ArchivePath("a1")): false,
	}
	for _, c := range changed {
		seen, ok := want[c]
		if !ok {
			t.Fatalf("unexpected changed path %q", c)
		}
		if seen {
			t.Fatalf("duplicate changed path %q", c)
		}
		want[c] = true
	}
	for c, seen := range want {
		if !seen {
			t.Fatalf("missing changed path %q", c)
		}
	}

	// PLAN.md leaned in place: DONE narrative gone, pointer added, OPEN claim kept.
	leaned, _ := os.ReadFile(planPath)
	ls := string(leaned)
	if strings.Contains(ls, "Impl (bee-a1") || strings.Contains(ls, "Review (approved, bee-9)") {
		t.Fatalf("leaned PLAN.md still has a1 narrative:\n%s", ls)
	}
	if !strings.Contains(ls, "Archived: docs/plan-archive/a1.md") {
		t.Fatal("leaned PLAN.md missing archive pointer")
	}
	if !strings.Contains(ls, "session=bee-7 heartbeat=2026-06-29T10:00:00Z") {
		t.Fatal("OPEN task claim metadata was altered")
	}

	// Idempotent: a second run is a clean no-op.
	ids2, _, before2, after2, err := archivePlan(root, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids2) != 0 || before2 != after2 {
		t.Fatalf("second run not a no-op: ids=%v %d->%d bytes", ids2, before2, after2)
	}
}
