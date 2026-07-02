package plan

import (
	"strings"
	"testing"
	"time"
)

// fatSample mirrors the real PLAN.md shape: DONE cards accreting Impl/Review/
// Reconciled/Merged closure prose, an actively claimed OPEN task, a NEEDS-REVIEW
// task, and a DONE task that still carries a (stale) claim stamp.
const fatSample = `<!-- Beehive-ROI: abc123 -->
# Plan

## t1 [DONE] <!-- attempts=0 deps= weight=32 -->
Add Fetch, Pull, Push to internal/git/git.go. FOUNDATION for the claim race.
Files: internal/git/git.go, internal/git/git_test.go.
Doc: docs/tasks/t1.md
Accept: ctx-aware wrappers with real error surfacing; unit tests.
Impl (bee-t1, commit c21a4f0, pushed origin): Fetch/Push/HardReset added; tests green under
CGO_ENABLED=0. Change doc docs/bee-t1-t1.md.
Review (approved, beehive-1): verified branch bee-t1 vs task + ROI. Accept met field-by-field.
Merged: fast-forward; pointer bumped. No dependents.

## t2 [TODO] <!-- attempts=1 deps=t1 weight=16 session=bee-9 heartbeat=2026-06-29T10:00:00Z -->
second, depends on t1
Files: internal/x/x.go
Doc: docs/tasks/t2.md
Accept: it works.

## t3 [NEEDS-REVIEW] <!-- attempts=2 deps= -->
ready for review
Review: this in-review note stays because the task is not DONE.

## t4 [DONE] <!-- attempts=0 deps= weight=8 session=bee-dead heartbeat=2026-06-29T09:00:00Z -->
Reconciled-only closure that still carries a claim stamp.
Files: internal/y/y.go
Doc: docs/tasks/t4.md
Accept: reconcile fires once.
Reconciled (ROI bcda44a): SHIPPED. ROI records the change. Closed DONE.
`

type meta struct {
	Status    Status
	Attempts  int
	Deps      string
	Weight    int
	Session   string
	Heartbeat string
}

func snapshot(p *Plan) map[string]meta {
	m := make(map[string]meta, len(p.Tasks))
	for _, t := range p.Tasks {
		hb := ""
		if !t.Heartbeat.IsZero() {
			hb = t.Heartbeat.UTC().Format(time.RFC3339)
		}
		m[t.ID] = meta{t.Status, t.Attempts, strings.Join(t.Deps, ","), t.Weight, t.Session, hb}
	}
	return m
}

func TestArchiveDoneMovesNarrativePreservesTasks(t *testing.T) {
	p, err := Parse(fatSample)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(p)

	archived := p.ArchiveDone("docs/plan-archive")

	// Exactly the two DONE tasks are archived, in plan order.
	if len(archived) != 2 || archived[0].ID != "t1" || archived[1].ID != "t4" {
		t.Fatalf("archived = %+v, want [t1 t4]", archived)
	}
	// t1's moved narrative holds the Impl/Review/Merged prose and NOT the card.
	n1 := archived[0].Narrative
	for _, want := range []string{"Impl (bee-t1", "Review (approved, beehive-1)", "Merged: fast-forward"} {
		if !strings.Contains(n1, want) {
			t.Fatalf("t1 narrative missing %q:\n%s", want, n1)
		}
	}
	if strings.Contains(n1, "Add Fetch") || strings.Contains(n1, "Files:") {
		t.Fatalf("t1 narrative leaked the lean card:\n%s", n1)
	}
	if !strings.Contains(archived[1].Narrative, "Reconciled (ROI bcda44a): SHIPPED") {
		t.Fatalf("t4 narrative wrong:\n%s", archived[1].Narrative)
	}

	leaned := p.String()

	// Materially smaller, and the fat prose is gone from PLAN.md.
	if len(leaned) >= len(fatSample) {
		t.Fatalf("archive did not shrink PLAN.md: %d -> %d", len(fatSample), len(leaned))
	}
	if len(fatSample)-len(leaned) < 150 {
		t.Fatalf("archive shrink not material: saved only %d bytes", len(fatSample)-len(leaned))
	}
	for _, gone := range []string{"Impl (bee-t1", "Review (approved, beehive-1)", "Merged: fast-forward", "Reconciled (ROI bcda44a)"} {
		if strings.Contains(leaned, gone) {
			t.Fatalf("leaned PLAN.md still contains archived prose %q", gone)
		}
	}
	// Lean cards keep the header + definition + a pointer stub.
	for _, keep := range []string{
		"## t1 [DONE] <!-- attempts=0 deps= weight=32 -->",
		"Add Fetch, Pull, Push",
		"Accept: ctx-aware wrappers",
		"Archived: docs/plan-archive/t1.md",
		"Archived: docs/plan-archive/t4.md",
	} {
		if !strings.Contains(leaned, keep) {
			t.Fatalf("leaned PLAN.md dropped required card content %q", keep)
		}
	}

	// Re-parse: identical task set/statuses/deps/weights/claims.
	p2, err := Parse(leaned)
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(p2)
	if len(after) != len(before) {
		t.Fatalf("task count changed: %d -> %d", len(before), len(after))
	}
	for id, b := range before {
		if a, ok := after[id]; !ok || a != b {
			t.Fatalf("task %s metadata changed: %+v -> %+v (present=%v)", id, b, a, ok)
		}
	}
	// The DONE task's surviving claim stamp is preserved verbatim.
	if p2.Task("t4").Session != "bee-dead" || p2.Task("t4").Heartbeat.UTC().Format(time.RFC3339) != "2026-06-29T09:00:00Z" {
		t.Fatalf("t4 claim not preserved: %+v", p2.Task("t4"))
	}
}

func TestArchiveDoneLeavesOpenTasksByteIdentical(t *testing.T) {
	p, _ := Parse(fatSample)
	p.ArchiveDone("docs/plan-archive")
	// The active OPEN claim and its body are untouched.
	t2 := p.Task("t2")
	if t2.Session != "bee-9" || t2.Heartbeat.UTC().Format(time.RFC3339) != "2026-06-29T10:00:00Z" {
		t.Fatalf("archive disturbed the active OPEN claim: %+v", t2)
	}
	if strings.Join(t2.Body, "\n") != "second, depends on t1\nFiles: internal/x/x.go\nDoc: docs/tasks/t2.md\nAccept: it works." {
		t.Fatalf("archive altered OPEN task body:\n%q", strings.Join(t2.Body, "\n"))
	}
	if t2.Archived() {
		t.Fatal("OPEN task must not receive an Archived pointer")
	}
	// A NEEDS-REVIEW body that merely LOOKS like narrative (a `Review:` line) is
	// left intact because the task is not DONE.
	t3 := p.Task("t3")
	if strings.Join(t3.Body, "\n") != "ready for review\nReview: this in-review note stays because the task is not DONE." {
		t.Fatalf("archive altered NEEDS-REVIEW task body:\n%q", strings.Join(t3.Body, "\n"))
	}
}

func TestArchiveDoneIdempotent(t *testing.T) {
	p, _ := Parse(fatSample)
	if got := p.ArchiveDone("docs/plan-archive"); len(got) != 2 {
		t.Fatalf("first archive moved %d, want 2", len(got))
	}
	once := p.String()
	// Second pass over the already-leaned plan is a pure no-op.
	if got := p.ArchiveDone("docs/plan-archive"); len(got) != 0 {
		t.Fatalf("re-archive moved %d tasks, want 0 (no-op)", len(got))
	}
	if twice := p.String(); twice != once {
		t.Fatalf("re-archive mutated the plan:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
	// Parse->archive of the leaned file is also a no-op (stub sentinel survives a round trip).
	reparsed, _ := Parse(once)
	if got := reparsed.ArchiveDone("docs/plan-archive"); len(got) != 0 {
		t.Fatalf("archive after reparse moved %d, want 0", len(got))
	}
}

// TestArchiveDoneMarkerAnchoring proves the `[:(]` anchor: a description line
// beginning with "Reviewer"/"Implementation" is NOT mistaken for a narrative
// marker, so the split starts at the real "Impl:" note and the definition stays.
func TestArchiveDoneMarkerAnchoring(t *testing.T) {
	src := "## m1 [DONE] <!-- attempts=0 deps= -->\n" +
		"Reviewer tooling: implement the review pane. Merge later.\n" +
		"Files: internal/review/review.go\n" +
		"Doc: docs/tasks/m1.md\n" +
		"Accept: works.\n" +
		"Impl: the real narrative starts here.\n"
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	archived := p.ArchiveDone("docs/plan-archive")
	if len(archived) != 1 || archived[0].Narrative != "Impl: the real narrative starts here." {
		t.Fatalf("marker anchoring wrong: %+v", archived)
	}
	body := strings.Join(p.Task("m1").Body, "\n")
	if !strings.Contains(body, "Reviewer tooling: implement the review pane. Merge later.") ||
		!strings.Contains(body, "Accept: works.") ||
		strings.Contains(body, "the real narrative starts here") {
		t.Fatalf("lean card wrong after anchoring split:\n%s", body)
	}
}

// TestArchiveDoneNoNarrativeSkipped: a terse DONE task with no closure prose is
// not stubbed (nothing to move), keeping the operation a true no-op for it.
func TestArchiveDoneNoNarrativeSkipped(t *testing.T) {
	p, _ := Parse("## d1 [DONE] <!-- attempts=0 deps= -->\njust a one-line done task\nFiles: x.go\n")
	if got := p.ArchiveDone("docs/plan-archive"); len(got) != 0 {
		t.Fatalf("archived a narrative-free DONE task: %+v", got)
	}
	if p.Task("d1").Archived() {
		t.Fatal("narrative-free DONE task must not get a pointer stub")
	}
}
