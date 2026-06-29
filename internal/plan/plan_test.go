package plan

import (
	"testing"
	"time"
)

const sample = `# Plan
<!-- Beehive-ROI: abc123 -->
- T0 | DONE | base
- T1 | TODO | feature | deps:T0
- T2 | TODO | blocked | deps:T1
- T3 | NEEDS-REVIEW | review me
- T4 | NEEDS-ARBITRATION | arb me | attempts:2
- T5 | NEEDS-HUMAN | stuck
- T6 | IN-PROGRESS | working | ts:2026-06-29T10:00:00Z | weight:3
`

func parse(t *testing.T) *Plan {
	t.Helper()
	p, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseRoundTrip(t *testing.T) {
	p := parse(t)
	if p.ROIStamp != "abc123" {
		t.Fatalf("stamp %q", p.ROIStamp)
	}
	if len(p.Tasks) != 7 {
		t.Fatalf("tasks %d", len(p.Tasks))
	}
	out, err := Parse(p.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tasks) != 7 || out.ROIStamp != "abc123" {
		t.Fatal("round trip lost data")
	}
	if t6 := out.Find("T6"); t6.Weight != 3 || t6.TS.IsZero() {
		t.Fatalf("T6 = %+v", t6)
	}
}

func TestCandidatesPriority(t *testing.T) {
	p := parse(t)
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC) // T6 ts+2h -> stale
	c := p.Candidates(now, time.Hour)
	if len(c) != 1 || c[0].ID != "T6" { // GC wins
		t.Fatalf("want GC T6, got %+v", c)
	}
	// fresh heartbeat: GC gone, arbitration next
	t6 := p.Find("T6")
	t6.TS = now
	c = p.Candidates(now, time.Hour)
	if len(c) != 1 || c[0].ID != "T4" {
		t.Fatalf("want arb T4, got %+v", c)
	}
	t6.Status = Done
	p.Find("T4").Status = Done
	c = p.Candidates(now, time.Hour)
	if len(c) != 1 || c[0].ID != "T3" {
		t.Fatalf("want review T3, got %+v", c)
	}
	p.Find("T3").Status = Done
	c = p.Candidates(now, time.Hour)
	if len(c) != 1 || c[0].ID != "T1" { // T2 deps unmet, T5 human
		t.Fatalf("want main T1, got %+v", c)
	}
}

func TestCycleSkip(t *testing.T) {
	p, _ := Parse(`- A | TODO | a | deps:B
- B | TODO | b | deps:A
- C | TODO | c`)
	if !p.HasCycle() {
		t.Fatal("cycle not detected")
	}
	c := p.Candidates(time.Now(), time.Hour)
	if len(c) != 1 || c[0].ID != "C" {
		t.Fatalf("cycle tasks not skipped: %+v", c)
	}
}

func TestStale(t *testing.T) {
	now := time.Now().UTC()
	tk := Task{Status: InProgress, TS: now.Add(-2 * time.Hour)}
	if !tk.Stale(now, time.Hour) {
		t.Fatal("should be stale")
	}
	if tk2 := (Task{Status: TODO}); tk2.Stale(now, time.Hour) {
		t.Fatal("todo not stale")
	}
}
