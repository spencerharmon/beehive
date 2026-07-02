package plan

import (
	"strings"
	"testing"
	"time"
)

// fatSample is a PLAN.md exercising the archive: DONE tasks with heavy
// Impl/Review/Reconciled narrative, an already-lean DONE task, and OPEN tasks
// (one actively claimed, one with a Reconciled section) that must stay untouched.
const fatSample = `<!-- Beehive-ROI: abc123 -->
# Plan

## done-fat [DONE] <!-- attempts=0 deps= weight=32 -->
Add Fetch, Pull, Push to git.go. FOUNDATION for the claim race and pointer bump.
Files: internal/git/git.go, internal/git/git_test.go.
Doc: docs/tasks/done-fat.md
Accept: ctx-aware wrappers with real error surfacing; unit tests vs a temp remote.
Impl (bee-done-fat, commit c21a4f0, pushed origin; worktree gitlink bumped to c21a4f0):
Fetch/Push pre-existed; two spec gaps closed. Added --prune to Fetch (explicit refspec
scopes prune to that branch's tracking ref; origin/<branch> opportunistic update and the
HEAD-stays-put invariant preserved) and a new Pull running pull --ff-only so divergence
errors surface via Run's stderr as a lost race and never merge. Push/HardReset already
matched spec, untouched. Tests in git_test.go against a temp bare origin plus two clones:
Push updates origin, Fetch advances origin/main without moving HEAD, Pull ff's HEAD and
content, HardReset discards committed and uncommitted edits. CGO_ENABLED=0 go test ./...
green across all packages, go vet clean, static cmd builds OK. Caveat: host cgo linker is
broken so the default cgo test-link fails environmentally; verified under CGO_ENABLED=0.
Review (approved, beehive-1782791070-321923): verified branch bee-done-fat vs task + ROI.
Accept met field-by-field: ctx-aware Fetch/Pull/Push/HardReset all via r.Run with stderr
surfaced (no swallowed errors); --prune scoped to the explicit refspec; Pull --ff-only
errors on divergence as the lost-race signal. Diff touches only internal/git/git.go and
git_test.go; commit carries the Beehive stamp. Independently re-ran under CGO_ENABLED=0:
go test ./... green, go vet clean, static build OK. MERGED fast-forward; pointer bumped
to the tracked-main tip. Implementer commit preserved. No dependents to unlock.

## done-reconciled [DONE] <!-- attempts=0 deps=done-fat weight=16 session=bee-old heartbeat=2026-06-29T09:00:00Z -->
Make the claim lock real: re-pull main and verify our stamp before proceeding.
Files: internal/claim/claim.go, internal/claim/claim_test.go.
Doc: docs/tasks/done-reconciled.md
Accept: two-claimer race yields exactly one winner; loser gets ErrLost.
Reconciled (ROI bcda44a): SHIPPED. ROI "Shipped since this ROI" now records the real
claim race: Claim and Heartbeat pull main, verify the session survived, and a publish
conflict yields ErrLost so the loser reselects instead of double-working. Two-claimer
race test yields exactly one winner; the loser gets ErrLost and abandons. Independently
re-verified under CGO_ENABLED=0: go test ./... green, vet clean, static build OK. Closed
as DONE; no further work.

## done-lean [DONE] <!-- attempts=1 deps= weight=8 -->
Already-lean closed card with no narrative to move.
Files: internal/x/x.go
Doc: docs/tasks/done-lean.md
Accept: it works.

## open-claimed [TODO] <!-- attempts=0 deps=done-fat weight=128 session=bee-live heartbeat=2026-06-29T10:00:00Z -->
An in-flight task under an active claim. Its body and claim MUST NOT be touched.
Files: internal/swarm/swarm.go
Doc: docs/tasks/open-claimed.md
Accept: something measurable.
Reconciled (ROI abc123): re-tiered to P1. Only the tier moved; body preserved.

## open-review [NEEDS-REVIEW] <!-- attempts=2 deps= -->
ready for review
Impl (bee-open-review, commit deadbee): this narrative is on an OPEN task and must stay.
`

// snapshot captures every field Parse yields for a task except Body, so a test
// can assert the archived plan preserves the task set/statuses/deps/weights/claims.
type snapshot struct {
	Status    Status
	Attempts  int
	Deps      string
	Weight    int
	Session   string
	Heartbeat string
}

func snap(t *Task) snapshot {
	hb := ""
	if !t.Heartbeat.IsZero() {
		hb = t.Heartbeat.UTC().Format(time.RFC3339)
	}
	return snapshot{t.Status, t.Attempts, strings.Join(t.Deps, ","), t.Weight, t.Session, hb}
}

func snapAll(p *Plan) map[string]snapshot {
	m := map[string]snapshot{}
	for _, t := range p.Tasks {
		m[t.ID] = snap(t)
	}
	return m
}

// TestArchivePreservesTaskSetAndShrinks is the core acceptance: archiving fat
// DONE entries preserves the parsed task set/statuses/deps/weights/claims and
// materially shrinks bytes, and the leaned plan still round-trips through Parse.
func TestArchivePreservesTaskSetAndShrinks(t *testing.T) {
	p, err := Parse(fatSample)
	if err != nil {
		t.Fatal(err)
	}
	before := snapAll(p)
	beforeBytes := len(p.String())

	docs := p.Archive()
	if len(docs) != 2 { // done-fat + done-reconciled; done-lean has no narrative
		t.Fatalf("archived %d docs, want 2 (%v)", len(docs), docNames(docs))
	}

	leaned := p.String()
	// Round-trips: the leaned text re-parses to the identical task set/metadata.
	rp, err := Parse(leaned)
	if err != nil {
		t.Fatalf("leaned plan does not parse: %v", err)
	}
	after := snapAll(rp)
	if len(after) != len(before) {
		t.Fatalf("task count changed: %d -> %d", len(before), len(after))
	}
	for id, b := range before {
		a, ok := after[id]
		if !ok {
			t.Fatalf("task %s vanished after archive", id)
		}
		if a != b {
			t.Fatalf("task %s metadata changed:\n before %+v\n after  %+v", id, b, a)
		}
	}
	if len(leaned) >= beforeBytes {
		t.Fatalf("archive did not shrink: %d -> %d", beforeBytes, len(leaned))
	}
	// The narrative dominated the fixture, so the shrink must be substantial.
	if len(leaned) > beforeBytes*6/10 {
		t.Fatalf("archive shrink not material: %d -> %d (want <= 60%%)", beforeBytes, len(leaned))
	}
}

// TestArchiveOffloadsDoneNarrativeOnly proves the Impl/Review prose leaves
// PLAN.md and lands in the archive doc, that the lean card keeps its
// description + Files/Doc/Accept + a pointer, and that OPEN tasks (including
// their claim metadata and any Reconciled/Impl body) are byte-for-byte untouched.
func TestArchiveOffloadsDoneNarrativeOnly(t *testing.T) {
	p, _ := Parse(fatSample)
	openClaimedBefore := strings.Join(p.Task("open-claimed").Body, "\n")
	openReviewBefore := strings.Join(p.Task("open-review").Body, "\n")
	doneLeanBefore := strings.Join(p.Task("done-lean").Body, "\n")

	docs := p.Archive()
	byID := map[string]ArchiveDoc{}
	for _, d := range docs {
		byID[d.ID] = d
	}

	// done-fat: narrative offloaded, card retained + pointer added.
	fat := p.Task("done-fat")
	fatBody := strings.Join(fat.Body, "\n")
	for _, keep := range []string{
		"Add Fetch, Pull, Push to git.go.",
		"Files: internal/git/git.go, internal/git/git_test.go.",
		"Doc: docs/tasks/done-fat.md",
		"Accept: ctx-aware wrappers",
	} {
		if !strings.Contains(fatBody, keep) {
			t.Fatalf("lean card lost card line %q:\n%s", keep, fatBody)
		}
	}
	for _, gone := range []string{"Impl (bee-done-fat", "Review (approved", "MERGED fast-forward"} {
		if strings.Contains(fatBody, gone) {
			t.Fatalf("narrative %q still inline after archive:\n%s", gone, fatBody)
		}
	}
	d, ok := byID["done-fat"]
	if !ok {
		t.Fatal("no archive doc for done-fat")
	}
	if d.Path != "docs/plan-archive/done-fat.md" {
		t.Fatalf("archive path = %q", d.Path)
	}
	if !strings.Contains(fatBody, archivePointerPrefix+" "+d.Path) {
		t.Fatalf("lean card missing pointer to %s:\n%s", d.Path, fatBody)
	}
	for _, want := range []string{"Impl (bee-done-fat", "Review (approved", "MERGED fast-forward"} {
		if !strings.Contains(d.Content, want) {
			t.Fatalf("archive doc missing narrative %q:\n%s", want, d.Content)
		}
	}
	// The archive doc must not swallow the card lines (they stay in PLAN.md only).
	if strings.Contains(d.Content, "Files: internal/git/git.go") {
		t.Fatalf("archive doc wrongly captured the card fields:\n%s", d.Content)
	}

	// done-reconciled: a Reconciled-only DONE task is archived; its claim metadata
	// (on the header) is preserved by Archive (it only edits the body).
	if _, ok := byID["done-reconciled"]; !ok {
		t.Fatal("Reconciled-only DONE task was not archived")
	}
	if dr := p.Task("done-reconciled"); dr.Session != "bee-old" || dr.Heartbeat.IsZero() {
		t.Fatalf("archive clobbered done-reconciled claim metadata: %+v", dr)
	}

	// done-lean: no narrative -> not archived, body identical.
	if _, ok := byID["done-lean"]; ok {
		t.Fatal("already-lean DONE task should not produce an archive doc")
	}
	if got := strings.Join(p.Task("done-lean").Body, "\n"); got != doneLeanBefore {
		t.Fatalf("already-lean DONE body changed:\n%s", got)
	}

	// OPEN tasks: untouched, narrative-looking body and claim preserved.
	if _, ok := byID["open-claimed"]; ok {
		t.Fatal("OPEN task archived")
	}
	if got := strings.Join(p.Task("open-claimed").Body, "\n"); got != openClaimedBefore {
		t.Fatalf("OPEN claimed body changed:\n%s", got)
	}
	if got := strings.Join(p.Task("open-review").Body, "\n"); got != openReviewBefore {
		t.Fatalf("OPEN review body (with an Impl line) changed:\n%s", got)
	}
}

// TestArchiveIdempotent proves a second pass is a strict no-op: no docs, no byte
// change, no duplicated pointer.
func TestArchiveIdempotent(t *testing.T) {
	p, _ := Parse(fatSample)
	p.Archive()
	once := p.String()

	docs2 := p.Archive()
	if len(docs2) != 0 {
		t.Fatalf("second archive produced %d docs, want 0", len(docs2))
	}
	if twice := p.String(); twice != once {
		t.Fatalf("second archive changed bytes:\n---once---\n%s\n---twice---\n%s", once, twice)
	}
	// Re-parsing and archiving again is still a no-op (persisted-then-reloaded).
	rp, _ := Parse(once)
	if docs3 := rp.Archive(); len(docs3) != 0 {
		t.Fatalf("archive of reloaded lean plan produced %d docs, want 0", len(docs3))
	}
	// Exactly one pointer per archived card.
	if n := strings.Count(once, archivePointerPrefix); n != 2 {
		t.Fatalf("expected 2 archive pointers, found %d", n)
	}
}

// TestArchiveMultilineParenMarker guards the marker regex against a real PLAN.md
// shape: an Impl section whose opening parenthetical spans multiple lines. The
// boundary is " (" or ":" right after the keyword, not a full paren+colon, so a
// wrapped header is still detected as the narrative start.
func TestArchiveMultilineParenMarker(t *testing.T) {
	src := "## m [DONE] <!-- attempts=0 deps= -->\n" +
		"desc line\n" +
		"Files: a.go\n" +
		"Doc: docs/tasks/m.md\n" +
		"Accept: works.\n" +
		"Impl (bee-m, submodule commit pushed origin; beehive pointer bumped to it; change\n" +
		"doc path here): the rest of the wrapped implementer note.\n"
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	docs := p.Archive()
	if len(docs) != 1 {
		t.Fatalf("multi-line paren marker not detected: %d docs", len(docs))
	}
	body := strings.Join(p.Task("m").Body, "\n")
	if strings.Contains(body, "Impl (bee-m") {
		t.Fatalf("wrapped Impl header not offloaded:\n%s", body)
	}
	if !strings.Contains(body, "Accept: works.") {
		t.Fatalf("card fields lost:\n%s", body)
	}
}

// TestArchiveDoesNotSplitProse guards against over-matching: a DONE body whose
// lines merely start with a keyword ("Impl/Review prose", "Impl note",
// "Implementation") but are NOT section headers must not trigger a split, so the
// card (which here has no real narrative) is left whole and nothing is archived.
func TestArchiveDoesNotSplitProse(t *testing.T) {
	src := "## p [DONE] <!-- attempts=0 deps= -->\n" +
		"Implementation of the widget, described here.\n" +
		"Impl/Review prose belongs in the archive per this task.\n" +
		"Impl note: this reads like a header but is prose (no space-paren).\n" +
		"Files: a.go\n" +
		"Doc: docs/tasks/p.md\n" +
		"Accept: works.\n"
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	// Only " (" or ":" IMMEDIATELY after the keyword marks a section: "Impl note:"
	// has " note:" after the word (colon not adjacent) so it stays prose, as do
	// "Impl/Review" and "Implementation". Nothing here is a header -> nothing moves.
	if docs := p.Archive(); len(docs) != 0 {
		t.Fatalf("prose lines wrongly treated as narrative: archived %d (%v)", len(docs), docNames(docs))
	}
}

func docNames(docs []ArchiveDoc) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out
}
