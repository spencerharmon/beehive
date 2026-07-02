package plan

import (
	"strings"
	"testing"
	"time"
)

// fatSample is a plan with one fat DONE card (description + Files/Doc/Accept +
// Impl/Review narrative), one claimed OPEN TODO, and one NEEDS-REVIEW task that
// carries an Impl note a reviewer still needs. It round-trips through
// Parse/String unchanged, so any post-archive size/content delta is the moved
// narrative alone.
const fatSample = `<!-- Beehive-ROI: abc123 -->
# Plan

## alpha [DONE] <!-- attempts=0 deps= weight=32 -->
Add Fetch, Pull, Push to git. FOUNDATION for the claim race.
Files: internal/git/git.go, internal/git/git_test.go.
Doc: docs/tasks/alpha.md
Accept: ctx-aware wrappers with real error surfacing; unit tests.
Impl (bee-alpha, commit c21a4f0, pushed origin): added --prune to Fetch and a new
Pull running pull --ff-only. Tests green under CGO_ENABLED=0.
Review (approved, beehive-123): verified branch bee-alpha vs task + ROI. Accept met
field-by-field. Merged and pushed origin main. Pointer bumped 1a53a93 -> 84b48a1.

## beta [TODO] <!-- attempts=1 deps=alpha weight=16 session=bee-9 heartbeat=2026-06-29T10:00:00Z -->
Make the claim lock real. After Commit and Heartbeat: pull main, verify our stamp.
Files: internal/claim/claim.go.
Doc: docs/tasks/beta.md
Accept: two-claimer race yields exactly one winner.

## gamma [NEEDS-REVIEW] <!-- attempts=0 deps= -->
Wire selection to the graph.
Files: internal/select/select.go.
Doc: docs/tasks/gamma.md
Accept: linked-submodule dep gates selection.
Impl: select owns the combined graph; plan stays links-free. Tests green.
`

func TestArchiveLeansDoneKeepsCardAndRoundTrips(t *testing.T) {
	p, err := Parse(fatSample)
	if err != nil {
		t.Fatal(err)
	}
	if p.String() != fatSample {
		t.Fatalf("fixture does not round-trip; fix the test sample first:\n%q", p.String())
	}
	before := p.String()

	arch := p.Archive()

	// Exactly the DONE task's narrative is archived.
	if len(arch) != 1 || arch[0].ID != "alpha" {
		t.Fatalf("archived = %+v, want exactly [alpha]", arch)
	}
	if arch[0].Header != "## alpha [DONE] <!-- attempts=0 deps= weight=32 -->" {
		t.Fatalf("archived header = %q", arch[0].Header)
	}
	nar := strings.Join(arch[0].Narrative, "\n")
	if !strings.HasPrefix(nar, "Impl (bee-alpha") || !strings.Contains(nar, "Review (approved, beehive-123)") {
		t.Fatalf("narrative missing Impl/Review prose:\n%s", nar)
	}

	// The DONE card keeps its description + Files/Doc/Accept, and drops narrative.
	alpha := p.Task("alpha")
	wantCard := []string{
		"Add Fetch, Pull, Push to git. FOUNDATION for the claim race.",
		"Files: internal/git/git.go, internal/git/git_test.go.",
		"Doc: docs/tasks/alpha.md",
		"Accept: ctx-aware wrappers with real error surfacing; unit tests.",
	}
	if strings.Join(alpha.Body, "\n") != strings.Join(wantCard, "\n") {
		t.Fatalf("alpha card body = %q, want %q", alpha.Body, wantCard)
	}

	after := p.String()
	// Narrative bytes are gone from the plan and materially smaller.
	if strings.Contains(after, "Review (approved, beehive-123)") || strings.Contains(after, "pull --ff-only") {
		t.Fatalf("narrative still present in leaned plan:\n%s", after)
	}
	if len(after) >= len(before) {
		t.Fatalf("archive did not shrink plan: before=%d after=%d", len(before), len(after))
	}

	// Re-parse: identical task set / statuses / deps / weights / claims.
	rp, err := Parse(after)
	if err != nil {
		t.Fatal(err)
	}
	assertSameTaskMeta(t, p, rp)

	// OPEN tasks are byte-for-byte untouched (their bodies, incl. gamma's Impl note).
	if got := strings.Join(rp.Task("beta").Body, "\n"); got != strings.Join(p.Task("beta").Body, "\n") {
		t.Fatalf("beta body changed: %q", got)
	}
	if got := strings.Join(rp.Task("gamma").Body, "\n"); !strings.Contains(got, "Impl: select owns the combined graph") {
		t.Fatalf("gamma (NEEDS-REVIEW) Impl note was archived; OPEN tasks must be untouched: %q", got)
	}

	// Idempotent: a second archive on the leaned plan is a no-op.
	if again := p.Archive(); again != nil {
		t.Fatalf("second Archive not a no-op: %+v", again)
	}
	if p.String() != after {
		t.Fatal("second Archive mutated the plan")
	}
}

// assertSameTaskMeta checks two plans carry the identical parsed task set and per
// task status/attempts/deps/weight/session/heartbeat (everything the selector and
// claim model read). Bodies may differ (that is what archiving changes).
func assertSameTaskMeta(t *testing.T, a, b *Plan) {
	t.Helper()
	if a.ROI != b.ROI {
		t.Fatalf("ROI drift %q vs %q", a.ROI, b.ROI)
	}
	if len(a.Tasks) != len(b.Tasks) {
		t.Fatalf("task count %d vs %d", len(a.Tasks), len(b.Tasks))
	}
	for i := range a.Tasks {
		x, y := a.Tasks[i], b.Tasks[i]
		if x.ID != y.ID || x.Status != y.Status || x.Attempts != y.Attempts ||
			x.Weight != y.Weight || x.Session != y.Session || !x.Heartbeat.Equal(y.Heartbeat) ||
			strings.Join(x.Deps, ",") != strings.Join(y.Deps, ",") {
			t.Fatalf("task %d metadata drift:\n a=%+v\n b=%+v", i, x, y)
		}
	}
}

// TestArchiveMarkerBoundaries proves the split fires only on a genuine narrative
// section head, not on description text that merely begins with a marker word.
func TestArchiveMarkerBoundaries(t *testing.T) {
	// "Reject from ..." and "Impl/Review prose ..." are description lines: they
	// must be RETAINED, and only "Reconciled (" / "Review:" starts the narrative.
	src := `## t [DONE] <!-- attempts=0 deps= -->
Guard status first: only review/arb may be rejected.
Reject from any other status must error and leave Attempts untouched.
Files: internal/claim/claim.go.
Doc: docs/tasks/t.md
Accept: reject on TODO errors; attempts unchanged. Impl/Review prose stays a card word.
Reconciled (ROI bcda44a): SHIPPED. Closed as DONE.
Review: folded in via merge; pointer bumped.
`
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	arch := p.Archive()
	if len(arch) != 1 {
		t.Fatalf("archived = %d, want 1", len(arch))
	}
	body := strings.Join(p.Task("t").Body, "\n")
	// The whole card, including the description line starting with "Reject" and
	// the Accept line mentioning "Impl/Review", is retained.
	for _, keep := range []string{
		"Reject from any other status must error",
		"Impl/Review prose stays a card word",
		"Files: internal/claim/claim.go.",
		"Accept: reject on TODO errors",
	} {
		if !strings.Contains(body, keep) {
			t.Fatalf("card dropped a retained line %q:\n%s", keep, body)
		}
	}
	// The Reconciled/Review narrative is archived and gone from the card.
	if strings.Contains(body, "Reconciled (ROI bcda44a)") || strings.Contains(body, "folded in via merge") {
		t.Fatalf("narrative not removed from card:\n%s", body)
	}
	nar := strings.Join(arch[0].Narrative, "\n")
	if !strings.HasPrefix(nar, "Reconciled (ROI bcda44a)") || !strings.Contains(nar, "folded in via merge") {
		t.Fatalf("narrative = %q", nar)
	}
}

// TestArchiveSkipsOpenAndEmpty proves non-DONE tasks are never archived (even
// with an Impl note) and a DONE card with no narrative yields no entry.
func TestArchiveSkipsOpenAndEmpty(t *testing.T) {
	src := `## rev [NEEDS-REVIEW] <!-- attempts=0 deps= -->
desc
Files: a.go
Impl (bee-x): implemented; awaiting review.
## arb [NEEDS-ARBITRATION] <!-- attempts=1 deps= -->
desc
Review (REJECTED): reviewer says redo.
## done [DONE] <!-- attempts=0 deps= -->
just a lean card
Files: b.go
Accept: it works.
`
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	before := p.String()
	if arch := p.Archive(); arch != nil {
		t.Fatalf("archived %+v; NEEDS-REVIEW/ARB untouched and clean DONE has no narrative", arch)
	}
	if p.String() != before {
		t.Fatal("Archive mutated a plan with no DONE narrative")
	}
}

// TestArchivePreservesClaimOnOpenTask guards the caveat: archiving must never
// touch OPEN tasks' claim metadata (session/heartbeat), even when a DONE sibling
// is leaned in the same pass.
func TestArchivePreservesClaimOnOpenTask(t *testing.T) {
	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	src := `## d [DONE] <!-- attempts=0 deps= -->
card
Accept: ok.
Impl (bee-d): done.
## live [TODO] <!-- attempts=0 deps= session=bee-live heartbeat=2026-06-29T10:00:00Z -->
working now
`
	p, _ := Parse(src)
	p.Archive()
	live := p.Task("live")
	if live.Session != "bee-live" || !live.Heartbeat.Equal(now) {
		t.Fatalf("OPEN task claim mutated: session=%q heartbeat=%v", live.Session, live.Heartbeat)
	}
	if !live.Active(now, time.Hour) {
		t.Fatal("OPEN task lost its active claim across archive")
	}
}
