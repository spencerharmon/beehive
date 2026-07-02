package plan

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// archiveSample mirrors the real PLAN.md shape: fat DONE tasks whose bulk is
// post-hoc Impl/Review/Reconciled narrative, an already-lean DONE task, and OPEN
// tasks (one carrying a live session+heartbeat claim, one NEEDS-REVIEW whose Impl
// note the reviewer still needs). fat-done carries stale claim metadata in its
// header (a real DONE task can, e.g. roi-pre-receive-hook) to prove archiving
// never disturbs it.
const archiveSample = `<!-- Beehive-ROI: abc123 -->
# Plan

## fat-done [DONE] <!-- attempts=1 deps=dep-a weight=32 session=bee-old heartbeat=2026-06-29T10:00:00Z -->
Add Fetch, Pull, Push to internal/git/git.go. FOUNDATION for the claim race.
Files: internal/git/git.go, internal/git/git_test.go.
Doc: docs/tasks/fat-done.md
Accept: ctx-aware wrappers with real error surfacing; unit tests against a temp
bare remote.
Impl (bee-fat-done, commit c21a4f0, pushed origin): Fetch/Push pre-existed; two spec
gaps closed. Added --prune to Fetch and a new Pull running pull --ff-only. Tests green
under CGO_ENABLED=0; go vet clean; static build OK.
Review (approved, beehive-999): verified branch bee-fat-done vs task + ROI. Accept met
field-by-field. Independently re-ran go test ./... GREEN. MERGED via fast-forward; pointer
bumped. No dependents to unlock.

## reconciled-done [DONE] <!-- attempts=0 deps= weight=16 -->
Make the claim lock real so two bees cannot both win.
Files: internal/claim/claim.go.
Doc: docs/tasks/reconciled-done.md
Accept: two-claimer race yields exactly one winner.
Reconciled (ROI bcda44a): SHIPPED. ROI now records the real claim race. Closed as DONE;
no further work.

## lean-done [DONE] <!-- attempts=0 deps= weight=8 -->
Already-lean completed task with no narrative to move.
Files: internal/x/x.go.
Doc: docs/tasks/lean-done.md
Accept: it does the thing.

## open-todo [TODO] <!-- attempts=0 deps= weight=32 session=bee-live heartbeat=2026-07-02T14:00:00Z -->
Publish-tree invariant work still in flight.
Files: internal/swarm/swarm.go.
Doc: docs/tasks/open-todo.md
Accept: a forced publish failure propagates.
Reconciled (ROI 84fb034): stays in-flight; the reviewer must confirm the guard merged.

## open-review [NEEDS-REVIEW] <!-- attempts=0 deps= weight=16 -->
Reviewer must read the implementer note below; do not archive it.
Files: internal/config/hook.go.
Doc: docs/tasks/open-review.md
Accept: a push touching ROI.md under honeybee identity is rejected.
Impl (bee-open-review, commit f38da14, pushed origin): adds the pre-receive hook. Ready
for review.
`

// snap captures every task's identity + status + claim metadata (everything the
// selector and claim model depend on) so a test can assert archiving preserves it.
func snap(p *Plan) map[string]string {
	m := map[string]string{}
	for _, t := range p.Tasks {
		m[t.ID] = fmt.Sprintf("status=%s attempts=%d deps=%s weight=%d session=%s hb=%s",
			t.Status, t.Attempts, strings.Join(t.Deps, ","), t.Weight, t.Session,
			t.Heartbeat.UTC().Format(time.RFC3339))
	}
	return m
}

func TestArchiveDonePreservesAndShrinks(t *testing.T) {
	p, err := Parse(archiveSample)
	if err != nil {
		t.Fatal(err)
	}
	before := snap(p)
	openTodoBody := strings.Join(p.Task("open-todo").Body, "\n")
	openReviewBody := strings.Join(p.Task("open-review").Body, "\n")

	nar := p.ArchiveDone()

	// Only fat DONE tasks are archived; a lean DONE and every OPEN task are left.
	if len(nar) != 2 {
		t.Fatalf("archived %d tasks, want 2 (fat-done + reconciled-done): %v", len(nar), keys(nar))
	}
	for _, want := range []string{"fat-done", "reconciled-done"} {
		if _, ok := nar[want]; !ok {
			t.Fatalf("%s not archived", want)
		}
	}
	for _, none := range []string{"lean-done", "open-todo", "open-review"} {
		if _, ok := nar[none]; ok {
			t.Fatalf("%s must not be archived", none)
		}
	}

	// The narrative moved OUT of the card and INTO the returned text.
	fj := strings.Join(nar["fat-done"], "\n")
	if !strings.Contains(fj, "Impl (bee-fat-done") || !strings.Contains(fj, "Review (approved") {
		t.Fatalf("fat-done narrative missing Impl/Review:\n%s", fj)
	}
	leanFat := strings.Join(p.Task("fat-done").Body, "\n")
	if strings.Contains(leanFat, "Impl (") || strings.Contains(leanFat, "Review (") {
		t.Fatalf("lean card still holds narrative:\n%s", leanFat)
	}
	// The card keeps the description + Files/Doc/Accept (incl. the multi-line Accept
	// continuation) and gains a pointer to the archive file.
	for _, keep := range []string{
		"Add Fetch, Pull, Push", "Files: internal/git/git.go", "Doc: docs/tasks/fat-done.md",
		"Accept: ctx-aware wrappers", "bare remote.",
		archivedPrefix + " " + ArchivePath("fat-done"),
	} {
		if !strings.Contains(leanFat, keep) {
			t.Fatalf("lean card lost %q:\n%s", keep, leanFat)
		}
	}

	// OPEN task bodies are byte-identical (never touched).
	if got := strings.Join(p.Task("open-todo").Body, "\n"); got != openTodoBody {
		t.Fatalf("OPEN TODO body changed:\n%s", got)
	}
	if got := strings.Join(p.Task("open-review").Body, "\n"); got != openReviewBody {
		t.Fatalf("OPEN NEEDS-REVIEW body changed (its Impl note was stripped):\n%s", got)
	}

	// Round-trip: re-parse the leaned plan; identical task set/statuses/deps/
	// weights/claims (the header metadata the selector + claim model depend on).
	leaned := p.String()
	p2, err := Parse(leaned)
	if err != nil {
		t.Fatal(err)
	}
	if after := snap(p2); !reflect.DeepEqual(before, after) {
		t.Fatalf("archiving changed task metadata:\nbefore=%v\nafter=%v", before, after)
	}

	// The whole point: materially fewer bytes.
	if len(leaned) >= len(archiveSample) {
		t.Fatalf("archiving did not shrink bytes: %d -> %d", len(archiveSample), len(leaned))
	}
}

func TestArchiveDoneIdempotent(t *testing.T) {
	p, err := Parse(archiveSample)
	if err != nil {
		t.Fatal(err)
	}
	p.ArchiveDone()
	once := p.String()
	if again := p.ArchiveDone(); len(again) != 0 {
		t.Fatalf("second archive moved %d tasks, want 0 (no-op): %v", len(again), keys(again))
	}
	if got := p.String(); got != once {
		t.Fatalf("second archive changed the plan:\n%s", got)
	}
	// A fresh parse of the already-leaned text also archives nothing.
	p2, err := Parse(once)
	if err != nil {
		t.Fatal(err)
	}
	if n := p2.ArchiveDone(); len(n) != 0 {
		t.Fatalf("re-parsed leaned plan archived %d, want 0", len(n))
	}
}

func TestSplitCardBoundary(t *testing.T) {
	// A description that mentions "Reconcile"/"Review" BEFORE any card field must
	// not be mistaken for the narrative boundary; the split falls at the first
	// Impl/Review section AFTER Files/Doc/Accept.
	body := []string{
		"Reconcile completion never fires: Review the diff base sentinel.",
		"Files: internal/select/select.go.",
		"Doc: docs/tasks/x.md",
		"Accept: the range is valid.",
		"Impl (bee-x, commit abc): did the thing.",
		"Review (approved): looks good.",
	}
	card, nar := splitCard(body)
	if len(card) != 4 {
		t.Fatalf("card = %d lines, want 4 (desc+Files+Doc+Accept):\n%v", len(card), card)
	}
	if card[0] != body[0] {
		t.Fatalf("description misclassified as narrative: %q", card[0])
	}
	if len(nar) != 2 || !strings.HasPrefix(nar[0], "Impl (") {
		t.Fatalf("narrative = %v, want the trailing Impl/Review lines", nar)
	}
	// No card field at all -> boundary cannot be located safely -> archive nothing.
	only := []string{"Review the situation.", "Do the work."}
	if _, n := splitCard(only); n != nil {
		t.Fatalf("splitCard without card fields must not archive, got %v", n)
	}
}

func TestArchivePath(t *testing.T) {
	if got := ArchivePath("git-remote-ops"); got != "docs/plan-archive/git-remote-ops.md" {
		t.Fatalf("ArchivePath = %q", got)
	}
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
