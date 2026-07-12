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
	// Valid from either reviewable status (NEEDS-REVIEW or NEEDS-ARBITRATION):
	// an interrupted review's merge can be finalized from both.
	for _, st := range []Status{StatusReview, StatusArb} {
		if err := (&Task{ID: "a", Status: st}).FinalizeAlreadyMerged("note", time.Now()); err != nil {
			t.Fatalf("finalize-already-merged rejected on %s: %v", st, err)
		}
	}
	for _, st := range []Status{StatusTODO, StatusDone, StatusHuman} {
		if err := (&Task{ID: "a", Status: st}).FinalizeAlreadyMerged("note", time.Now()); err == nil {
			t.Fatalf("finalize-already-merged allowed on %s", st)
		}
	}
	if err := (&Task{ID: "a", Status: StatusReview}).FinalizeAlreadyMerged("", time.Now()); err == nil {
		t.Fatal("finalize-already-merged allowed with an empty note")
	}
}

// TestRecoverLostWorkAttempts mirrors TestRejectAttempts/TestStrandAttempts:
// repeated recoveries recycle NEEDS-REVIEW or NEEDS-ARBITRATION back to TODO
// (claim released, attempts bumped) up to limit, then overflow to NEEDS-HUMAN
// carrying the caller's concrete reason verbatim.
func TestRecoverLostWorkAttempts(t *testing.T) {
	now := time.Now()
	for _, start := range []Status{StatusReview, StatusArb} {
		tk := &Task{ID: "a", Status: start, Attempts: 0}
		for i := 0; i < 3; i++ {
			tk.Status = start
			tk.Session, tk.Heartbeat = "bee-1", now
			if err := tk.RecoverLostWork("implementer commit unrecoverable: no branch, no doc", 3, now); err != nil {
				t.Fatal(err)
			}
			if tk.Status != StatusTODO {
				t.Fatalf("from %s attempt %d status %s", start, i, tk.Status)
			}
			if tk.Session != "" || !tk.Heartbeat.IsZero() {
				t.Fatalf("from %s attempt %d: recover-lost-work must release the claim", start, i)
			}
		}
		tk.Status = start
		if err := tk.RecoverLostWork("implementer commit unrecoverable: no branch, no doc", 3, now); err != nil {
			t.Fatal(err) // 4th > 3
		}
		if tk.Status != StatusHuman {
			t.Fatalf("from %s: want NEEDS-HUMAN, got %s", start, tk.Status)
		}
		if got := tk.HumanReason(); got != "implementer commit unrecoverable: no branch, no doc" {
			t.Fatalf("human reason = %q, want the caller's concrete reason verbatim", got)
		}
	}
}

// TestRecoverLostWorkGuardedAndRequiresReason: only legal from NEEDS-REVIEW or
// NEEDS-ARBITRATION (never TODO/DONE), and always requires a concrete reason.
func TestRecoverLostWorkGuardedAndRequiresReason(t *testing.T) {
	now := time.Now()
	for _, st := range []Status{StatusTODO, StatusDone, StatusHuman} {
		if err := (&Task{ID: "a", Status: st}).RecoverLostWork("reason", 3, now); err == nil {
			t.Fatalf("recover-lost-work allowed on %s", st)
		}
	}
	if err := (&Task{ID: "a", Status: StatusReview}).RecoverLostWork("  \t \n ", 3, now); err == nil {
		t.Fatal("recover-lost-work allowed with an empty/blank reason")
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

// TestHumanReasonStructuredSpan is the storage-side core of
// plan-view-detail-polish: a "Human-needed:" field may carry a one-line
// summary plus bullets immediately below it — the structure HONEYBEE.md's
// escalation guidance asks agents to write — and HumanReason/clearHumanReason
// must treat the WHOLE span (the prefix line plus every immediately-following
// non-blank line, stopping at the next blank line) as one field, not just its
// first line, so a view can render the complete reason as markdown.
func TestHumanReasonStructuredSpan(t *testing.T) {
	tk := &Task{ID: "a", Status: StatusHuman, Body: []string{
		"stuck",
		"",
		"Human-needed: Missing credentials for the deploy API.",
		"- Blocker: cannot authenticate to the release service",
		"- Needed: a fresh API token for that service",
		"",
		"Files: a.go",
	}}
	want := "Missing credentials for the deploy API.\n" +
		"- Blocker: cannot authenticate to the release service\n" +
		"- Needed: a fresh API token for that service"
	if got := tk.HumanReason(); got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
	// Resolving must drop the ENTIRE span, including the bullets, and leave
	// unrelated trailing body content (Files:) untouched.
	if err := tk.Resolve(time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := tk.HumanReason(); got != "" {
		t.Fatalf("reason after resolve = %q, want cleared", got)
	}
	s := (&Plan{Tasks: []*Task{tk}}).String()
	if strings.Contains(s, "Human-needed:") || strings.Contains(s, "Blocker:") {
		t.Fatalf("resolve left structured reason lines behind:\n%s", s)
	}
	if !strings.Contains(s, "Files: a.go") {
		t.Fatalf("resolve dropped unrelated trailing body content:\n%s", s)
	}
}

// TestSetHumanReasonReplacesStructuredSpan proves setHumanReason (as
// RequestHuman uses it to overwrite a prior escalation) replaces a PRIOR
// structured reason's whole span — summary plus bullets — rather than only
// overwriting its first line and leaving stale bullets orphaned in the body.
func TestSetHumanReasonReplacesStructuredSpan(t *testing.T) {
	tk := &Task{ID: "a", Status: StatusHuman, Body: []string{
		"Human-needed: old summary.",
		"- old bullet one",
		"- old bullet two",
	}}
	tk.setHumanReason("new summary, one line")
	if got := tk.HumanReason(); got != "new summary, one line" {
		t.Fatalf("reason = %q", got)
	}
	if strings.Contains(strings.Join(tk.Body, "\n"), "old bullet") {
		t.Fatalf("stale bullets survived a reason replacement: %v", tk.Body)
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

// TestNotBeforeParseRoundTrip proves an optional not_before stamp parses to a
// time and round-trips through String() byte-for-byte.
func TestReviewCommitParseRoundTrip(t *testing.T) {
	sha := "f11aef766662b31b4f492bef1eb4cbbb1729e1eb"
	src := "<!-- Beehive-ROI: abc -->\n\n## x [NEEDS-REVIEW] <!-- attempts=1 deps= review=" + sha + " -->\nbody\n"
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	x := p.Task("x")
	if x.ReviewCommit != sha {
		t.Fatalf("review commit parsed wrong: %q", x.ReviewCommit)
	}
	if got := p.String(); got != src {
		t.Fatalf("round trip mismatch:\n%q\nvs\n%q", got, src)
	}
	// SetReviewCommit ignores a blank sha (never erases a good record) and
	// overwrites otherwise.
	x.SetReviewCommit("")
	if x.ReviewCommit != sha {
		t.Fatalf("blank SetReviewCommit erased the record: %q", x.ReviewCommit)
	}
	x.SetReviewCommit("deadbeef")
	if x.ReviewCommit != "deadbeef" {
		t.Fatalf("SetReviewCommit did not overwrite: %q", x.ReviewCommit)
	}
}

func TestNotBeforeParseRoundTrip(t *testing.T) {
	src := "<!-- Beehive-ROI: abc -->\n\n## x [TODO] <!-- attempts=0 deps= not_before=2026-07-01T12:00:00Z -->\nbody\n"
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	x := p.Task("x")
	if x.NotBefore.UTC().Format(time.RFC3339) != "2026-07-01T12:00:00Z" {
		t.Fatalf("not_before parsed wrong: %v", x.NotBefore)
	}
	if got := p.String(); got != src {
		t.Fatalf("round trip mismatch:\n%q\nvs\n%q", got, src)
	}
}

// TestNotBeforeMalformedSurfaces proves a malformed not_before timestamp is a
// parse error, not silently swallowed.
func TestNotBeforeMalformedSurfaces(t *testing.T) {
	if _, err := Parse("## x [TODO] <!-- attempts=0 deps= not_before=nonsense -->\n"); err == nil {
		t.Fatal("expected error for malformed not_before, got nil")
	}
}

// TestNotBeforeReached proves the wall-clock gate: no gate or a past gate is
// ready; a future gate is not.
func TestNotBeforeReached(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	var none Task
	if !none.NotBeforeReached(now) {
		t.Fatal("no gate must be ready")
	}
	past := Task{NotBefore: now.Add(-time.Hour)}
	if !past.NotBeforeReached(now) {
		t.Fatal("past gate must be ready")
	}
	exact := Task{NotBefore: now}
	if !exact.NotBeforeReached(now) {
		t.Fatal("gate at exactly now must be ready")
	}
	future := Task{NotBefore: now.Add(time.Hour)}
	if future.NotBeforeReached(now) {
		t.Fatal("future gate must not be ready")
	}
}

// TestCandidatesNotBeforeGate proves the selector's candidate set excludes a
// TODO task with a future not_before and includes it once wall-clock passes,
// while deps gate independently.
func TestCandidatesNotBeforeGate(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	ttl := time.Hour
	src := "## a [TODO] <!-- attempts=0 deps= not_before=2026-07-01T13:00:00Z -->\n" +
		"## b [TODO] <!-- attempts=0 deps= -->\n"
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	// a is gated in the future; only b is a candidate now.
	cands := p.Candidates(now, ttl)
	if len(cands) != 1 || cands[0].ID != "b" {
		t.Fatalf("expected only b ready, got %+v", cands)
	}
	// Once wall-clock passes a's gate, both are candidates.
	later := now.Add(2 * time.Hour)
	cands = p.Candidates(later, ttl)
	if len(cands) != 2 {
		t.Fatalf("expected a and b ready after gate, got %+v", cands)
	}
	// A future not_before must not affect an already-unmet dep path: deps gate
	// independently. Give a a future gate AND an unmet dep; it stays out even
	// after the gate passes until the dep is DONE.
	p2, _ := Parse("## a [TODO] <!-- attempts=0 deps=b not_before=2026-07-01T11:00:00Z -->\n" +
		"## b [TODO] <!-- attempts=0 deps= -->\n")
	cands = p2.Candidates(now, ttl) // gate is past, but dep b not done
	for _, c := range cands {
		if c.ID == "a" {
			t.Fatal("a must stay gated by unmet dep b even with past not_before")
		}
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
