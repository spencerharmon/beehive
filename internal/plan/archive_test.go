package plan

import (
	"strings"
	"testing"
)

// fatPlan mirrors the real PLAN.md shape: DONE tasks whose bodies carry a long
// Impl/Review/Reconciled narrative tail after the Files:/Doc:/Accept: card, plus
// an OPEN task holding live claim metadata that must never be disturbed.
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

func TestLeanDoneRoundTripsAndShrinks(t *testing.T) {
	orig, err := Parse(fatPlan)
	if err != nil {
		t.Fatal(err)
	}
	leaned, err := Parse(fatPlan)
	if err != nil {
		t.Fatal(err)
	}
	got := leaned.LeanDone()

	// Only the DONE tasks with narrative are archived; the TODO is not.
	if len(got) != 2 {
		t.Fatalf("archived %d tasks, want 2 (t1,t3): %v", len(got), keys(got))
	}
	if _, ok := got["t2"]; ok {
		t.Fatal("OPEN task t2 was archived")
	}
	if n := got["t1"]; len(n) == 0 || !strings.HasPrefix(n[0], "Impl (bee-t1") {
		t.Fatalf("t1 narrative wrong: %v", n)
	}
	if n := got["t3"]; len(n) != 1 || !strings.HasPrefix(n[0], "Reconciled (ROI") {
		t.Fatalf("t3 narrative wrong: %v", n)
	}

	// The leaned DONE cards keep description + Files/Doc/Accept, drop narrative.
	t1 := leaned.Task("t1")
	if last := t1.Body[len(t1.Body)-1]; !strings.HasPrefix(last, "Accept:") {
		t.Fatalf("t1 leaned body should end at Accept, got %q", last)
	}
	if strings.Contains(strings.Join(t1.Body, "\n"), "Impl (") {
		t.Fatal("t1 leaned body still contains Impl narrative")
	}

	// The OPEN task is byte-for-byte untouched, claim metadata intact.
	origT2, leanT2 := orig.Task("t2"), leaned.Task("t2")
	if strings.Join(origT2.Body, "\n") != strings.Join(leanT2.Body, "\n") {
		t.Fatalf("OPEN task body changed:\n%v\nvs\n%v", origT2.Body, leanT2.Body)
	}
	if leanT2.Session != "bee-9" || !leanT2.Heartbeat.Equal(origT2.Heartbeat) {
		t.Fatalf("OPEN task claim metadata disturbed: session=%q hb=%v", leanT2.Session, leanT2.Heartbeat)
	}

	// Parser round-trip: the leaned file parses to the SAME task metadata.
	leanedText := leaned.String()
	reparsed, err := Parse(leanedText)
	if err != nil {
		t.Fatalf("leaned plan does not parse: %v", err)
	}
	if err := SameMeta(orig, reparsed); err != nil {
		t.Fatalf("archive changed parsed metadata: %v", err)
	}

	// Materially smaller.
	if len(leanedText) >= len(fatPlan) {
		t.Fatalf("leaned (%d) not smaller than original (%d)", len(leanedText), len(fatPlan))
	}
}

func TestLeanDoneIdempotent(t *testing.T) {
	p, err := Parse(fatPlan)
	if err != nil {
		t.Fatal(err)
	}
	first := p.LeanDone()
	if len(first) == 0 {
		t.Fatal("first pass archived nothing")
	}
	afterFirst := p.String()
	// A second pass on the already-lean plan must move nothing and not alter it.
	second := p.LeanDone()
	if len(second) != 0 {
		t.Fatalf("second pass archived %d tasks, want 0 (no-op): %v", len(second), keys(second))
	}
	if got := p.String(); got != afterFirst {
		t.Fatalf("second pass mutated the plan:\n%s", got)
	}
}

// TestSplitCardBoundary proves the narrative split fires only on real section
// headers, never on description/continuation prose that merely starts with a
// section keyword (e.g. "Impl/Review ..." or "Impl note ...").
func TestSplitCardBoundary(t *testing.T) {
	body := []string{
		"Impl/Review prose is what we archive, not this description line.",
		"Impl note style continuations must also stay in the card.",
		"Files: internal/plan/plan.go",
		"Doc: docs/tasks/d1.md",
		"Accept: works.",
		"Impl (bee-d1, commit deadbee): did the work.",
		"Review: approved and merged.",
	}
	card, narrative := splitCard(body)
	if len(card) != 5 {
		t.Fatalf("card len %d, want 5 (desc x2 + Files/Doc/Accept): %v", len(card), card)
	}
	if !strings.HasPrefix(card[0], "Impl/Review prose") || !strings.HasPrefix(card[1], "Impl note") {
		t.Fatalf("false-positive split on prose: card=%v", card)
	}
	if len(narrative) != 2 || !strings.HasPrefix(narrative[0], "Impl (bee-d1") {
		t.Fatalf("narrative wrong: %v", narrative)
	}

	// A body with no narrative section is all card, nothing to archive.
	lean := []string{"desc", "Files: a", "Doc: b", "Accept: c"}
	if c, n := splitCard(lean); n != nil || len(c) != 4 {
		t.Fatalf("lean body split: card=%v narrative=%v", c, n)
	}
}

// TestSameMetaDetectsChange guards the invariant checker itself: a status flip
// on the re-parsed plan must be reported.
func TestSameMetaDetectsChange(t *testing.T) {
	a, _ := Parse(fatPlan)
	b, _ := Parse(fatPlan)
	if err := SameMeta(a, b); err != nil {
		t.Fatalf("identical plans reported different: %v", err)
	}
	b.Task("t1").Status = StatusReview
	if err := SameMeta(a, b); err == nil {
		t.Fatal("SameMeta missed a status change")
	}
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
