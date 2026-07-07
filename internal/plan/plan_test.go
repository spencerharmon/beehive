package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `<!-- Beehive-ROI: abc123 -->
# Plan

## t1 [TODO] <!-- attempts=0 deps= -->
do the first thing

## t2 [TODO] <!-- attempts=1 deps=t1 session=bee-9 heartbeat=2026-06-29T10:00:00Z -->
second, depends on t1

## t3 [NEEDS-REVIEW] <!-- attempts=2 deps= -->
ready for review
`

func TestParseRoundTrip(t *testing.T) {
	p, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if p.ROI != "abc123" {
		t.Fatalf("roi=%q", p.ROI)
	}
	if len(p.Tasks) != 3 {
		t.Fatalf("tasks=%d", len(p.Tasks))
	}
	t2 := p.Task("t2")
	if t2.Status != StatusTODO || t2.Attempts != 1 || len(t2.Deps) != 1 || t2.Deps[0] != "t1" {
		t.Fatalf("t2 parsed wrong: %+v", t2)
	}
	if t2.Session != "bee-9" {
		t.Fatalf("session=%q", t2.Session)
	}
	if t2.Heartbeat.UTC().Format(time.RFC3339) != "2026-06-29T10:00:00Z" {
		t.Fatalf("heartbeat=%v", t2.Heartbeat)
	}
	if got := p.String(); got != sample {
		t.Fatalf("round trip mismatch:\n%q\nvs\n%q", got, sample)
	}
}

// TestCard proves Task.Card() renders a task's H2 header + body exactly as
// Plan.String() emits that task (single source of truth for the card format the
// runner injects in the honeybee brief).
func TestCard(t *testing.T) {
	p, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	t2 := p.Task("t2")
	card := t2.Card()
	if !strings.HasPrefix(card, "## t2 [TODO] <!--") {
		t.Fatalf("card missing canonical header: %q", card)
	}
	// The card is exactly the slice Plan.String() emits for this task: its header
	// line plus its body, each newline-terminated.
	want := t2.header() + "\n" + strings.Join(t2.Body, "\n") + "\n"
	if card != want {
		t.Fatalf("card mismatch:\n%q\nvs\n%q", card, want)
	}
	// A body-less task renders just the header line.
	bare := (&Task{ID: "z", Status: StatusDone}).Card()
	if bare != "## z [DONE] <!-- attempts=0 deps= -->\n" {
		t.Fatalf("bare card: %q", bare)
	}
}

// TestLegacyInProgressNormalizes proves an old [IN-PROGRESS] header still loads,
// mapped to TODO (in-progress is now session+heartbeat, not a status).
func TestLegacyInProgressNormalizes(t *testing.T) {
	p, err := Parse("## x [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\nbody\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Task("x").Status != StatusTODO {
		t.Fatalf("legacy IN-PROGRESS not normalized: %s", p.Task("x").Status)
	}
}

func TestBadStatus(t *testing.T) {
	if _, err := Parse("## x [BOGUS] <!-- attempts=0 deps= -->\n"); err == nil {
		t.Fatal("want error on bad status")
	}
}

func TestStateMachine(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	tk := &Task{ID: "a", Status: StatusTODO}
	// A claim marks the task active without changing status.
	tk.Claim("bee-1", now)
	if !tk.Active(now, time.Hour) {
		t.Fatal("claimed task not active")
	}
	if tk.Status != StatusTODO {
		t.Fatal("claim must not change status")
	}
	// TODO cannot jump straight to DONE.
	if err := tk.Transition(StatusDone, now); err == nil {
		t.Fatal("illegal TODO->DONE allowed")
	}
	// TODO -> NEEDS-REVIEW releases the active claim.
	if err := tk.Transition(StatusReview, now); err != nil {
		t.Fatal(err)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("transition must release the claim")
	}
	if err := tk.Transition(StatusDone, now); err != nil {
		t.Fatal(err)
	}
}

func TestStaleHeartbeat(t *testing.T) {
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	ttl := time.Hour
	// Active claim: session + fresh heartbeat.
	tk := &Task{ID: "a", Status: StatusTODO, Session: "bee-1", Heartbeat: base}
	if tk.Stale(base.Add(30*time.Minute), ttl) {
		t.Fatal("fresh treated stale")
	}
	if !tk.Active(base.Add(30*time.Minute), ttl) {
		t.Fatal("fresh claim not active")
	}
	if !tk.Stale(base.Add(2*time.Hour), ttl) {
		t.Fatal("old not stale")
	}
	if tk.Active(base.Add(2*time.Hour), ttl) {
		t.Fatal("expired claim still active")
	}
	// No session => never active or stale, regardless of a leftover heartbeat.
	unclaimed := &Task{ID: "b", Status: StatusReview, Heartbeat: base}
	if unclaimed.Stale(base.Add(99*time.Hour), ttl) || unclaimed.Active(base, ttl) {
		t.Fatal("sessionless task must be neither active nor stale")
	}
}

func TestRejectAttempts(t *testing.T) {
	now := time.Now()
	tk := &Task{ID: "a", Status: StatusReview, Attempts: 0}
	for i := 0; i < 3; i++ {
		tk.Status = StatusReview
		if err := tk.Reject(3, now); err != nil {
			t.Fatal(err)
		}
		if tk.Status != StatusTODO {
			t.Fatalf("attempt %d status %s", i, tk.Status)
		}
	}
	tk.Status = StatusReview
	tk.Reject(3, now) // 4th > 3
	if tk.Status != StatusHuman {
		t.Fatalf("want NEEDS-HUMAN, got %s", tk.Status)
	}
	if got := tk.HumanReason(); got == "" {
		t.Fatal("reject overflow should record a human reason")
	}
}

// TestStrandAttempts mirrors TestRejectAttempts: repeated strands recycle to
// TODO (a fresh honeybee redoes the work and retries the push) up to limit,
// then overflow to NEEDS-HUMAN carrying the caller's concrete reason verbatim
// — never the generic reject-overflow message.
func TestStrandAttempts(t *testing.T) {
	now := time.Now()
	tk := &Task{ID: "a", Status: StatusReview, Attempts: 0}
	for i := 0; i < 3; i++ {
		tk.Status = StatusReview
		tk.Session, tk.Heartbeat = "bee-1", now
		if err := tk.Strand("origin unreachable: dial tcp: connection refused", 3, now); err != nil {
			t.Fatal(err)
		}
		if tk.Status != StatusTODO {
			t.Fatalf("attempt %d status %s", i, tk.Status)
		}
		if tk.Session != "" || !tk.Heartbeat.IsZero() {
			t.Fatalf("attempt %d: strand must release the claim", i)
		}
	}
	tk.Status = StatusReview
	if err := tk.Strand("origin unreachable: dial tcp: connection refused", 3, now); err != nil {
		t.Fatal(err) // 4th > 3
	}
	if tk.Status != StatusHuman {
		t.Fatalf("want NEEDS-HUMAN, got %s", tk.Status)
	}
	if got := tk.HumanReason(); got != "origin unreachable: dial tcp: connection refused" {
		t.Fatalf("human reason = %q, want the caller's concrete reason verbatim", got)
	}
}

// TestStrandGuardedAndRequiresReason: Strand only applies to NEEDS-REVIEW (never
// NEEDS-ARBITRATION — that transition is Reject's, back after arbitration; and
// never TODO/DONE), and always requires a concrete reason.
func TestStrandGuardedAndRequiresReason(t *testing.T) {
	now := time.Now()
	for _, st := range []Status{StatusTODO, StatusArb, StatusDone, StatusHuman} {
		if err := (&Task{ID: "a", Status: st}).Strand("reason", 3, now); err == nil {
			t.Fatalf("strand allowed on %s", st)
		}
	}
	if err := (&Task{ID: "a", Status: StatusReview}).Strand("  \t \n ", 3, now); err == nil {
		t.Fatal("strand allowed with an empty/blank reason")
	}
}

// TestBounceUnreachable locks the review-dispatch-side deterministic bounce:
// NEEDS-REVIEW -> NEEDS-ARBITRATION with the reason recorded as a review note,
// claim released, never touching Attempts (this is not a rejection verdict —
// there was no review at all).
func TestBounceUnreachable(t *testing.T) {
	tk := &Task{ID: "a", Status: StatusReview, Session: "bee-1", Heartbeat: time.Now(), Attempts: 1}
	if err := tk.BounceUnreachable("reviewable commit unreachable: implementer branch bee-a resolves nowhere"); err != nil {
		t.Fatal(err)
	}
	if tk.Status != StatusArb {
		t.Fatalf("status = %s, want NEEDS-ARBITRATION", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("bounce-unreachable must release the claim")
	}
	if tk.Attempts != 1 {
		t.Fatalf("attempts = %d, want unchanged (1): bouncing is not a review rejection", tk.Attempts)
	}
	joined := strings.Join(tk.Body, "\n")
	if !strings.Contains(joined, "Review (bounced, runner): reviewable commit unreachable") {
		t.Fatalf("body missing bounce note:\n%s", joined)
	}
}

// TestBounceUnreachableGuardedAndRequiresReason: only legal from NEEDS-REVIEW,
// and always requires a concrete reason (an empty bounce reason would leave the
// next Arbitration pass with nothing to go on).
func TestBounceUnreachableGuardedAndRequiresReason(t *testing.T) {
	for _, st := range []Status{StatusTODO, StatusArb, StatusDone, StatusHuman} {
		if err := (&Task{ID: "a", Status: st}).BounceUnreachable("reason"); err == nil {
			t.Fatalf("bounce-unreachable allowed on %s", st)
		}
	}
	if err := (&Task{ID: "a", Status: StatusReview}).BounceUnreachable(""); err == nil {
		t.Fatal("bounce-unreachable allowed with an empty reason")
	}
}

// TestFinalizeAlreadyMerged locks the review-dispatch-side deterministic
// finalize (session-audit-005 F-1, the symmetric counterpart to
// BounceUnreachable): NEEDS-REVIEW -> DONE with the note recorded as a review
// note, claim released, never touching Attempts (this completes an interrupted
// review's bookkeeping — it is not a verdict on a fresh review).
func TestFinalizeAlreadyMerged(t *testing.T) {
	tk := &Task{ID: "a", Status: StatusReview, Session: "bee-1", Heartbeat: time.Now(), Attempts: 1}
	if err := tk.FinalizeAlreadyMerged("already merged into tracked main (a02a886) by a prior interrupted review; runner-finalized DONE (no re-review)", time.Now()); err != nil {
		t.Fatal(err)
	}
	if tk.Status != StatusDone {
		t.Fatalf("status = %s, want DONE", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("finalize-already-merged must release the claim")
	}
	if tk.Attempts != 1 {
		t.Fatalf("attempts = %d, want unchanged (1): this is not a review rejection", tk.Attempts)
	}
	joined := strings.Join(tk.Body, "\n")
	if !strings.Contains(joined, "Review (runner-finalized): already merged into tracked main (a02a886)") {
		t.Fatalf("body missing finalize note:\n%s", joined)
	}
}

// TestFinalizeAlreadyMergedGuardedAndRequiresNote: only legal from
// NEEDS-REVIEW, and always requires a concrete note (an empty note would leave
// no record of why the task jumped straight to DONE without a review verdict).
func TestFinalizeAlreadyMergedGuardedAndRequiresNote(t *testing.T) {
	for _, st := range []Status{StatusTODO, StatusArb, StatusDone, StatusHuman} {
		if err := (&Task{ID: "a", Status: st}).FinalizeAlreadyMerged("note", time.Now()); err == nil {
			t.Fatalf("finalize-already-merged allowed on %s", st)
		}
	}
	if err := (&Task{ID: "a", Status: StatusReview}).FinalizeAlreadyMerged("", time.Now()); err == nil {
		t.Fatal("finalize-already-merged allowed with an empty note")
	}
}

func TestRequestHuman(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	tk := &Task{ID: "a", Status: StatusTODO, Session: "bee-1", Heartbeat: now, Body: []string{"do thing"}}
	if err := tk.RequestHuman("Need operator to choose public wire format.\nChanging later is breaking.", now); err != nil {
		t.Fatal(err)
	}
	if tk.Status != StatusHuman {
		t.Fatalf("status = %s, want NEEDS-HUMAN", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("human request must release claim")
	}
	if got := tk.HumanReason(); got != "Need operator to choose public wire format. Changing later is breaking." {
		t.Fatalf("reason = %q", got)
	}
	if got := (&Plan{Tasks: []*Task{tk}}).String(); !strings.Contains(got, "Human-needed: Need operator") {
		t.Fatalf("serialized plan missing human reason:\n%s", got)
	}
}

func TestRequestHumanRejectsEmptyAndDone(t *testing.T) {
	if err := (&Task{ID: "a", Status: StatusTODO}).RequestHuman(" \n\t ", time.Now()); err == nil {
		t.Fatal("empty reason allowed")
	}
	if err := (&Task{ID: "a", Status: StatusDone}).RequestHuman("need operator", time.Now()); err == nil {
		t.Fatal("DONE human request allowed")
	}
}

func TestResolveReopensHumanTask(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	tk := &Task{ID: "a", Status: StatusTODO, Session: "bee-1", Heartbeat: now, Body: []string{"do thing"}}
	if err := tk.RequestHuman("Need the Cloudflare API token.", now); err != nil {
		t.Fatal(err)
	}
	// A stale claim may have been re-stamped onto the escalated task; Resolve must
	// still clear it so the reopened TODO is unclaimed and freshly selectable.
	tk.Claim("bee-2", now)
	if err := tk.Resolve(now); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tk.Status != StatusTODO {
		t.Fatalf("status = %s, want TODO", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("resolve must release the claim")
	}
	if got := tk.HumanReason(); got != "" {
		t.Fatalf("reason = %q, want cleared", got)
	}
	// The reopened task is now auto-selectable (no deps) and its serialized form
	// carries neither the NEEDS-HUMAN status nor the Human-needed line.
	p := &Plan{Tasks: []*Task{tk}}
	if !p.Selectable(tk) {
		t.Fatal("resolved task should be selectable")
	}
	if s := p.String(); strings.Contains(s, "NEEDS-HUMAN") || strings.Contains(s, "Human-needed:") {
		t.Fatalf("serialized resolved plan still shows blocker:\n%s", s)
	}
}

func TestResolveRejectsNonHuman(t *testing.T) {
	now := time.Now()
	for _, st := range []Status{StatusTODO, StatusReview, StatusArb, StatusDone} {
		if err := (&Task{ID: "a", Status: st}).Resolve(now); err == nil {
			t.Fatalf("resolve allowed on %s", st)
		}
	}
}

func TestSelectable(t *testing.T) {
	p, _ := Parse(sample)
	if p.Selectable(p.Task("t2")) {
		t.Fatal("t2 selectable but dep t1 not done")
	}
	p.Task("t1").Status = StatusDone
	if !p.Selectable(p.Task("t2")) {
		t.Fatal("t2 should be selectable after dep done")
	}
}

func TestSelectableDefersCrossSubmoduleDeps(t *testing.T) {
	// A "<submodule>:<taskid>" dep is cross-submodule: the plan layer stays
	// links-free and leaves resolution to the selector, so it must not block
	// local selectability even though no local task by that id exists.
	p, err := Parse("## t1 [TODO] <!-- attempts=0 deps=other:dep -->\nbody\n")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Selectable(p.Task("t1")) {
		t.Fatal("cross-submodule dep must be deferred to the selector, not block locally")
	}
	// A local unmet dep alongside the cross-submodule one still blocks.
	p2, _ := Parse("## a [TODO] <!-- attempts=0 deps=b,other:dep -->\n## b [TODO] <!-- attempts=0 deps= -->\n")
	if p2.Selectable(p2.Task("a")) {
		t.Fatal("local unmet dep b must still block")
	}
	p2.Task("b").Status = StatusDone
	if !p2.Selectable(p2.Task("a")) {
		t.Fatal("local dep done -> selectable (cross-submodule dep deferred)")
	}
}

func TestGolden(t *testing.T) {
	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	p, _ := Parse(sample)
	p.Task("t1").Claim("bee-7", now)
	p.Task("t3").Transition(StatusDone, now)
	p.Stamp("def456")
	got := p.String()
	gp := filepath.Join("testdata", "transition.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		os.WriteFile(gp, []byte(got), 0o644)
	}
	want, err := os.ReadFile(gp)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch:\n%s", got)
	}
}
