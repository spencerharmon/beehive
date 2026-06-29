package plan

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sample = `<!-- Beehive-ROI: abc123 -->
# Plan

## t1 [TODO] <!-- attempts=0 deps= -->
do the first thing

## t2 [IN-PROGRESS] <!-- attempts=1 deps=t1 heartbeat=2026-06-29T10:00:00Z -->
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
	if t2.Status != StatusInProgress || t2.Attempts != 1 || len(t2.Deps) != 1 || t2.Deps[0] != "t1" {
		t.Fatalf("t2 parsed wrong: %+v", t2)
	}
	if t2.Heartbeat.UTC().Format(time.RFC3339) != "2026-06-29T10:00:00Z" {
		t.Fatalf("heartbeat=%v", t2.Heartbeat)
	}
	if got := p.String(); got != sample {
		t.Fatalf("round trip mismatch:\n%q\nvs\n%q", got, sample)
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
	if err := tk.Transition(StatusInProgress, now); err != nil {
		t.Fatal(err)
	}
	if tk.Heartbeat.IsZero() {
		t.Fatal("no heartbeat after in-progress")
	}
	if err := tk.Transition(StatusDone, now); err == nil {
		t.Fatal("illegal in-progress->done allowed")
	}
	if err := tk.Transition(StatusReview, now); err != nil {
		t.Fatal(err)
	}
	if !tk.Heartbeat.IsZero() {
		t.Fatal("heartbeat not cleared on review")
	}
	if err := tk.Transition(StatusDone, now); err != nil {
		t.Fatal(err)
	}
}

func TestStaleHeartbeat(t *testing.T) {
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	tk := &Task{ID: "a", Status: StatusInProgress, Heartbeat: base}
	ttl := time.Hour
	if tk.Stale(base.Add(30*time.Minute), ttl) {
		t.Fatal("fresh treated stale")
	}
	if !tk.Stale(base.Add(2*time.Hour), ttl) {
		t.Fatal("old not stale")
	}
	done := &Task{ID: "b", Status: StatusDone}
	if done.Stale(base.Add(99*time.Hour), ttl) {
		t.Fatal("done never stale")
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

func TestGolden(t *testing.T) {
	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	p, _ := Parse(sample)
	p.Task("t1").Transition(StatusInProgress, now)
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
