package plan

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// archiveSample mixes fat DONE tasks (t1 Impl+Review, t3 Reconciled-only) with
// OPEN tasks that must be left byte-identical: t2 is TODO and CARRIES a
// `Reconciled (...)` narrative block (mirrors the real publish-advance-guard
// in-flight task) plus live session+heartbeat claim metadata, and t4 is
// NEEDS-REVIEW. Archiving must touch only the DONE tasks.
const archiveSample = `<!-- Beehive-ROI: abc123 -->
# Plan

## t1 [DONE] <!-- attempts=0 deps= weight=32 -->
Add Foo to bar.go. FOUNDATION for t2.
Files: bar.go, bar_test.go.
Doc: docs/tasks/t1.md
Accept: ctx-aware wrapper with real tests.
Impl (bee-t1, commit abc1234, pushed origin): closed two spec gaps across
many lines of narrative prose that is audit history, not task input.
Review (approved, bee-99): verified vs task + ROI; merged; pointer bumped.
Caveat: none worth noting.

## t2 [TODO] <!-- attempts=1 deps=t1 weight=32 session=bee-9 heartbeat=2026-06-29T10:00:00Z -->
Second task, depends on t1.
Files: baz.go
Doc: docs/tasks/t2.md
Accept: it works.
Reconciled (ROI 84fb034): still in-flight; this narrative stays because t2 is OPEN.

## t3 [DONE] <!-- attempts=0 deps=t1 weight=16 -->
Make the claim real.
Files: claim.go
Doc: docs/tasks/t3.md
Accept: race yields exactly one winner.
Reconciled (ROI bcda44a): SHIPPED. Closed as DONE; no further work.

## t4 [NEEDS-REVIEW] <!-- attempts=2 deps= -->
Ready for review; body preserved verbatim.
Files: qux.go
Doc: docs/tasks/t4.md
Accept: reviewer merges or kicks.
`

// snapshot captures the parser-visible state Archive must preserve.
func snapshot(p *Plan) map[string]string {
	m := map[string]string{"__roi__": p.ROI}
	for _, t := range p.Tasks {
		hb := ""
		if !t.Heartbeat.IsZero() {
			hb = t.Heartbeat.UTC().Format(time.RFC3339)
		}
		m[t.ID] = fmt.Sprintf("st=%s att=%d deps=%s w=%d sess=%s hb=%s",
			t.Status, t.Attempts, strings.Join(t.Deps, ","), t.Weight, t.Session, hb)
	}
	return m
}

func TestArchiveLeansDoneTasksRoundTrip(t *testing.T) {
	p, err := Parse(archiveSample)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(p)
	t2Body := strings.Join(p.Task("t2").Body, "\n")
	t4Body := strings.Join(p.Task("t4").Body, "\n")

	got := p.Archive()
	if len(got) != 2 || got[0].ID != "t1" || got[1].ID != "t3" {
		t.Fatalf("archived = %+v, want [t1 t3] in plan order", got)
	}

	// t1's Impl/Review/Caveat prose moved out; the lean card + pointer remains.
	t1 := p.Task("t1")
	if narrativeStart(t1.Body) != len(t1.Body) {
		t.Fatalf("t1 still carries narrative: %q", t1.Body)
	}
	if last := t1.Body[len(t1.Body)-1]; last != "Archived: docs/plan-archive/t1.md" {
		t.Fatalf("t1 missing archive pointer, last line = %q", last)
	}
	for _, line := range t1.Body {
		if strings.HasPrefix(line, "Impl ") || strings.HasPrefix(line, "Review ") {
			t.Fatalf("t1 card still contains narrative line %q", line)
		}
	}
	if n := strings.Join(got[0].Narrative, "\n"); !strings.HasPrefix(n, "Impl (bee-t1") ||
		!strings.Contains(n, "Review (approved, bee-99)") || !strings.HasSuffix(n, "Caveat: none worth noting.") {
		t.Fatalf("t1 narrative wrong:\n%s", n)
	}
	// t3 reconciled-only narrative moved out.
	if n := strings.Join(got[1].Narrative, "\n"); n != "Reconciled (ROI bcda44a): SHIPPED. Closed as DONE; no further work." {
		t.Fatalf("t3 narrative = %q", n)
	}

	// OPEN tasks are byte-identical, including t2's own Reconciled block + claim.
	if now := strings.Join(p.Task("t2").Body, "\n"); now != t2Body {
		t.Fatalf("t2 (TODO) body changed:\n%s", now)
	}
	if now := strings.Join(p.Task("t4").Body, "\n"); now != t4Body {
		t.Fatalf("t4 (NEEDS-REVIEW) body changed:\n%s", now)
	}

	// Round-trip: the leaned plan re-parses to the identical task set/metadata.
	leaned := p.String()
	rp, err := Parse(leaned)
	if err != nil {
		t.Fatalf("leaned plan does not parse: %v", err)
	}
	after := snapshot(rp)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("task set changed by archive:\nbefore=%v\nafter =%v", before, after)
	}
	if len(leaned) >= len(archiveSample) {
		t.Fatalf("archive did not shrink PLAN.md: %d -> %d bytes", len(archiveSample), len(leaned))
	}
}

func TestArchiveIdempotent(t *testing.T) {
	p, err := Parse(archiveSample)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Archive()) != 2 {
		t.Fatal("first archive should lean 2 DONE tasks")
	}
	first := p.String()
	if again := p.Archive(); len(again) != 0 {
		t.Fatalf("second archive moved %d task(s); want no-op", len(again))
	}
	if p.String() != first {
		t.Fatal("second archive changed the plan bytes; want idempotent no-op")
	}
	// A freshly parsed already-lean plan also archives nothing.
	rp, _ := Parse(first)
	if again := rp.Archive(); len(again) != 0 {
		t.Fatalf("re-parsed lean plan archived %d task(s)", len(again))
	}
}

func TestNarrativeStart(t *testing.T) {
	leaders := []string{
		"Impl (bee-x, commit abc): did it.",
		"Impl: did it.",
		"Review (approved, bee-9): ok.",
		"Review: ok.",
		"Reconciled (ROI abc): shipped.",
		"Arbitration (BINDING, bee-9): merged.",
	}
	for _, lead := range leaders {
		body := []string{"description first line", lead, "trailing narrative"}
		if got := narrativeStart(body); got != 1 {
			t.Fatalf("narrativeStart with leader %q = %d, want 1", lead, got)
		}
	}
	// A lean card has no narrative: card fields, a description word that merely
	// starts with a leader ("Deferred", "Implementation"), and the pointer must
	// NOT be mistaken for a section leader.
	card := []string{
		"Deferred (sub-task of x). Do the thing.",
		"Files: a.go",
		"Doc: docs/tasks/x.md",
		"Accept: it works.",
		"Implementation notes are in the change doc.",
		"Archived: docs/plan-archive/x.md",
	}
	if got := narrativeStart(card); got != len(card) {
		t.Fatalf("lean card flagged narrative at index %d: %q", got, card[got])
	}
}

func TestArchiveDoc(t *testing.T) {
	a := Archived{ID: "foo", Narrative: []string{"Impl (bee-x): line one", "Review: line two"}}
	doc := a.Doc()
	if !strings.HasPrefix(doc, "# foo\n") {
		t.Fatalf("doc missing id heading:\n%s", doc)
	}
	if !strings.Contains(doc, "beehive plan archive") {
		t.Fatalf("doc missing provenance note:\n%s", doc)
	}
	if !strings.HasSuffix(doc, "Impl (bee-x): line one\nReview: line two\n") {
		t.Fatalf("doc missing verbatim narrative:\n%s", doc)
	}
}

func TestArchivePath(t *testing.T) {
	if got := ArchivePath("git-remote-ops"); got != "docs/plan-archive/git-remote-ops.md" {
		t.Fatalf("ArchivePath = %q", got)
	}
}
