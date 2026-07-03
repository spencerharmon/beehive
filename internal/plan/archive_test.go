package plan

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// fatPlan mirrors the real PLAN.md shape: DONE tasks carrying long Impl/Review/
// Reconciled narrative after their card, interleaved with OPEN tasks (some with an
// active session+heartbeat claim, some with in-flight Impl/Reconciled prose that is
// task INPUT, not archivable history), plus one already-lean DONE task.
const fatPlan = `<!-- Beehive-ROI: deadbeef -->
# Plan

## alpha [DONE] <!-- attempts=0 deps= weight=32 -->
Add Foo to bar. FOUNDATION for baz.
Files: internal/foo/foo.go, internal/foo/foo_test.go.
Doc: docs/tasks/alpha.md
Accept: ctx-aware wrappers with real error surfacing; unit tests.
Impl (bee-alpha, commit c21a4f0, pushed origin): closed two spec gaps. Added --prune.
Tests green under CGO_ENABLED=0; vet clean; static build OK.
Review (approved, beehive-123): verified branch bee-alpha vs task + ROI. Accept met.
Merged; pointer bumped. No dependents to unlock.

## beta [TODO] <!-- attempts=1 deps=alpha session=beehive-999 heartbeat=2026-07-03T00:00:00Z -->
Implement the beta thing that depends on alpha.
Files: internal/beta/beta.go.
Doc: docs/tasks/beta.md
Accept: beta works end to end.
Reconciled (ROI deadbeef): re-tiered; in-flight claim + body preserved, only the tier moved.

## gamma [NEEDS-REVIEW] <!-- attempts=0 deps= weight=16 session=beehive-777 heartbeat=2026-07-03T01:00:00Z -->
The gamma card body.
Files: internal/gamma/gamma.go.
Doc: docs/tasks/gamma.md
Accept: gamma parses.
Impl (bee-gamma, commit abc123): implemented; awaiting review. This Impl note is reviewer INPUT.

## delta [DONE] <!-- attempts=0 deps= -->
Delta with only a Reconciled closing note.
Files: internal/delta/delta.go.
Doc: docs/tasks/delta.md
Accept: delta done.
Reconciled (ROI cafe): SHIPPED elsewhere; recorded in "Shipped since this ROI". Closed DONE.

## epsilon [DONE] <!-- attempts=0 deps= weight=8 -->
Epsilon is already lean (no narrative).
Files: internal/epsilon/epsilon.go.
Doc: docs/tasks/epsilon.md
Accept: epsilon ok.
`

// snap captures the parser-visible identity of every task: the fields the archive
// MUST preserve (id/status/attempts/deps/weight/session/heartbeat). Body is
// deliberately excluded — leaning bodies is the whole point.
type snap struct {
	ID        string
	Status    Status
	Attempts  int
	Deps      []string
	Weight    int
	Session   string
	Heartbeat time.Time
}

func snapshot(p *Plan) []snap {
	out := make([]snap, len(p.Tasks))
	for i, t := range p.Tasks {
		out[i] = snap{t.ID, t.Status, t.Attempts, t.Deps, t.Weight, t.Session, t.Heartbeat}
	}
	return out
}

func TestArchiveDonePreservesParseAndShrinks(t *testing.T) {
	p, err := Parse(fatPlan)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(p)
	beforeBytes := len(p.String())

	got := p.ArchiveDone()

	// Only the two fat DONE tasks are archived; epsilon (lean DONE) and the two
	// OPEN tasks are not.
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	if !reflect.DeepEqual(ids, []string{"alpha", "delta"}) {
		t.Fatalf("archived ids = %v, want [alpha delta]", ids)
	}

	// Re-parse the leaned plan: the parser-visible task set/statuses/deps/weights/
	// claims must be byte-for-byte identical (only bodies changed).
	re, err := Parse(p.String())
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshot(re); !reflect.DeepEqual(before, after) {
		t.Fatalf("archive altered task metadata:\nbefore=%+v\nafter =%+v", before, after)
	}

	if got := len(p.String()); got >= beforeBytes {
		t.Fatalf("archive did not shrink bytes: before=%d after=%d", beforeBytes, got)
	}
}

func TestArchiveDoneMovesNarrativeKeepsCard(t *testing.T) {
	p, err := Parse(fatPlan)
	if err != nil {
		t.Fatal(err)
	}
	got := p.ArchiveDone()

	// alpha keeps exactly its card (description + Files/Doc/Accept); its
	// Impl/Review prose is gone from the body and captured in the archive.
	alpha := p.Task("alpha")
	wantCard := []string{
		"Add Foo to bar. FOUNDATION for baz.",
		"Files: internal/foo/foo.go, internal/foo/foo_test.go.",
		"Doc: docs/tasks/alpha.md",
		"Accept: ctx-aware wrappers with real error surfacing; unit tests.",
	}
	if !reflect.DeepEqual(alpha.Body, wantCard) {
		t.Fatalf("alpha card body = %q\nwant %q", alpha.Body, wantCard)
	}
	for _, line := range alpha.Body {
		if narrativeRe.MatchString(line) {
			t.Fatalf("alpha card still carries a narrative line: %q", line)
		}
	}

	var alphaArch *Archived
	for i := range got {
		if got[i].ID == "alpha" {
			alphaArch = &got[i]
		}
	}
	if alphaArch == nil {
		t.Fatal("alpha not archived")
	}
	wantNarr := []string{
		"Impl (bee-alpha, commit c21a4f0, pushed origin): closed two spec gaps. Added --prune.",
		"Tests green under CGO_ENABLED=0; vet clean; static build OK.",
		"Review (approved, beehive-123): verified branch bee-alpha vs task + ROI. Accept met.",
		"Merged; pointer bumped. No dependents to unlock.",
	}
	if !reflect.DeepEqual(alphaArch.Narrative, wantNarr) {
		t.Fatalf("alpha narrative = %q\nwant %q", alphaArch.Narrative, wantNarr)
	}

	// delta's sole narrative is a Reconciled block — also archived, card retained.
	delta := p.Task("delta")
	if strings.Join(delta.Body, "\n") != "Delta with only a Reconciled closing note.\nFiles: internal/delta/delta.go.\nDoc: docs/tasks/delta.md\nAccept: delta done." {
		t.Fatalf("delta card not preserved: %q", delta.Body)
	}
}

func TestArchiveDoneLeavesOpenTasksUntouched(t *testing.T) {
	p, err := Parse(fatPlan)
	if err != nil {
		t.Fatal(err)
	}
	// Capture OPEN-task bodies + claims before.
	betaBody := append([]string(nil), p.Task("beta").Body...)
	gammaBody := append([]string(nil), p.Task("gamma").Body...)
	betaSess, betaHB := p.Task("beta").Session, p.Task("beta").Heartbeat
	gammaSess, gammaHB := p.Task("gamma").Session, p.Task("gamma").Heartbeat

	p.ArchiveDone()

	// A TODO task's Reconciled note is task context, NOT archivable history: kept.
	if !reflect.DeepEqual(p.Task("beta").Body, betaBody) {
		t.Fatalf("beta (TODO) body changed: %q", p.Task("beta").Body)
	}
	// A NEEDS-REVIEW task's Impl note is reviewer INPUT: kept.
	if !reflect.DeepEqual(p.Task("gamma").Body, gammaBody) {
		t.Fatalf("gamma (NEEDS-REVIEW) body changed: %q", p.Task("gamma").Body)
	}
	// Claim metadata on OPEN tasks must be exactly preserved (never raced).
	if p.Task("beta").Session != betaSess || !p.Task("beta").Heartbeat.Equal(betaHB) {
		t.Fatal("beta claim metadata altered")
	}
	if p.Task("gamma").Session != gammaSess || !p.Task("gamma").Heartbeat.Equal(gammaHB) {
		t.Fatal("gamma claim metadata altered")
	}
}

func TestArchiveDoneIdempotent(t *testing.T) {
	p, err := Parse(fatPlan)
	if err != nil {
		t.Fatal(err)
	}
	if first := p.ArchiveDone(); len(first) != 2 {
		t.Fatalf("first pass archived %d, want 2", len(first))
	}
	leaned := p.String()

	// Re-parse and archive again: nothing left to move, output byte-identical.
	re, err := Parse(leaned)
	if err != nil {
		t.Fatal(err)
	}
	if second := re.ArchiveDone(); second != nil {
		t.Fatalf("second pass archived %v, want nil (no-op)", second)
	}
	if re.String() != leaned {
		t.Fatalf("second pass changed bytes:\n%q\nvs\n%q", re.String(), leaned)
	}
}

// TestArchiveDoneNoFalseSplit proves a DONE task whose DESCRIPTION merely mentions
// "Impl/Review" (not the "Impl:"/"Impl (" section form) is split at the REAL marker,
// so the description line stays on the card and is not swept into the archive.
func TestArchiveDoneNoFalseSplit(t *testing.T) {
	src := "## zeta [DONE] <!-- attempts=0 deps= -->\n" +
		"Impl/Review prose handling is the whole point of this card.\n" +
		"Files: internal/zeta/zeta.go.\n" +
		"Doc: docs/tasks/zeta.md\n" +
		"Accept: zeta ok.\n" +
		"Impl (bee-zeta, commit z1): actually implemented; tests green.\n" +
		"Review (approved): fine.\n"
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	got := p.ArchiveDone()
	if len(got) != 1 || got[0].ID != "zeta" {
		t.Fatalf("archived = %v, want one zeta", got)
	}
	wantCard := []string{
		"Impl/Review prose handling is the whole point of this card.",
		"Files: internal/zeta/zeta.go.",
		"Doc: docs/tasks/zeta.md",
		"Accept: zeta ok.",
	}
	if !reflect.DeepEqual(p.Task("zeta").Body, wantCard) {
		t.Fatalf("zeta card = %q\nwant %q", p.Task("zeta").Body, wantCard)
	}
	wantNarr := []string{
		"Impl (bee-zeta, commit z1): actually implemented; tests green.",
		"Review (approved): fine.",
	}
	if !reflect.DeepEqual(got[0].Narrative, wantNarr) {
		t.Fatalf("zeta narrative = %q\nwant %q", got[0].Narrative, wantNarr)
	}
}

func TestRenderArchiveDeterministic(t *testing.T) {
	a := Archived{ID: "alpha", Narrative: []string{"Impl (bee-alpha): did it.", "Review (approved): ok."}}
	want := "# alpha - archived PLAN.md narrative\n\nImpl (bee-alpha): did it.\nReview (approved): ok.\n"
	if got := RenderArchive(a); got != want {
		t.Fatalf("RenderArchive =\n%q\nwant\n%q", got, want)
	}
	if RenderArchive(a) != RenderArchive(a) {
		t.Fatal("RenderArchive not deterministic")
	}
	// Empty narrative renders just the header (no dangling body).
	if got := RenderArchive(Archived{ID: "x"}); got != "# x - archived PLAN.md narrative\n\n" {
		t.Fatalf("empty-narrative render = %q", got)
	}
}
