package plan

import (
	"strings"
	"testing"
)

// TestRepairCorruptStamps_EmptyHeartbeat locks the canonical OOM-mid-write
// signature: an empty `session=` + `heartbeat=` pair that makes the whole file
// unparseable is dropped, the repaired file parses, and every other token on the
// line survives verbatim.
func TestRepairCorruptStamps_EmptyHeartbeat(t *testing.T) {
	src := "# Plan\n\n## fix-me [DONE] <!-- attempts=2 deps=a,b weight=4 session= heartbeat= -->\nbody line\n"
	if _, err := Parse(src); err == nil {
		t.Fatal("precondition: corrupt src should NOT parse")
	}
	repaired, changed, unfixable := RepairCorruptStamps(src)
	if len(unfixable) != 0 {
		t.Fatalf("expected no unfixable residual, got %v", unfixable)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed line, got %d: %+v", len(changed), changed)
	}
	if changed[0].ID != "fix-me" || changed[0].Line != 3 {
		t.Fatalf("wrong change locus: %+v", changed[0])
	}
	if got := strings.Join(changed[0].Dropped, ","); got != "session,heartbeat" {
		t.Fatalf("dropped = %q, want session,heartbeat", got)
	}
	p, err := Parse(repaired)
	if err != nil {
		t.Fatalf("repaired src must parse, got %v\n%s", err, repaired)
	}
	tk := p.Task("fix-me")
	if tk == nil {
		t.Fatal("task lost in repair")
	}
	if tk.Attempts != 2 || tk.Weight != 4 || strings.Join(tk.Deps, ",") != "a,b" {
		t.Fatalf("surviving tokens corrupted: attempts=%d weight=%d deps=%v", tk.Attempts, tk.Weight, tk.Deps)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatalf("claim not cleared: session=%q heartbeat=%v", tk.Session, tk.Heartbeat)
	}
	if tk.Status != StatusDone {
		t.Fatalf("status changed: %s", tk.Status)
	}
	// Body preserved.
	if len(tk.Body) != 1 || tk.Body[0] != "body line" {
		t.Fatalf("body corrupted: %v", tk.Body)
	}
}

// TestRepairCorruptStamps_EmptyNotBefore covers an empty not_before gate token.
func TestRepairCorruptStamps_EmptyNotBefore(t *testing.T) {
	src := "# Plan\n\n## t1 [TODO] <!-- attempts=0 deps= not_before= -->\n"
	if _, err := Parse(src); err == nil {
		t.Fatal("precondition: corrupt src should NOT parse")
	}
	repaired, changed, unfixable := RepairCorruptStamps(src)
	if len(unfixable) != 0 || len(changed) != 1 {
		t.Fatalf("changed=%+v unfixable=%v", changed, unfixable)
	}
	p, err := Parse(repaired)
	if err != nil {
		t.Fatalf("repaired must parse: %v", err)
	}
	if !p.Task("t1").NotBefore.IsZero() {
		t.Fatal("not_before not cleared")
	}
}

// TestRepairCorruptStamps_KeepsValidClaim ensures a live task with a VALID
// session+heartbeat is never touched (dropped only empties).
func TestRepairCorruptStamps_KeepsValidClaim(t *testing.T) {
	src := "# Plan\n\n## live [TODO] <!-- attempts=1 deps= session=abc heartbeat=2026-07-10T13:13:09Z -->\n"
	repaired, changed, unfixable := RepairCorruptStamps(src)
	if len(changed) != 0 || len(unfixable) != 0 {
		t.Fatalf("valid line must be untouched: changed=%+v unfixable=%v", changed, unfixable)
	}
	if repaired != src {
		t.Fatalf("valid src mutated:\n%q\n->\n%q", src, repaired)
	}
}

// TestRepairCorruptStamps_RefusesNonEmptyMalformed ensures a non-empty but
// malformed heartbeat (real-but-corrupt data) is NOT discarded — it is reported
// as unfixable and left byte-identical, so the repair never loses real data.
func TestRepairCorruptStamps_RefusesNonEmptyMalformed(t *testing.T) {
	src := "# Plan\n\n## t [TODO] <!-- attempts=0 deps= heartbeat=2026-07-10T13:13 -->\n"
	repaired, changed, unfixable := RepairCorruptStamps(src)
	if len(changed) != 0 {
		t.Fatalf("must not rewrite a non-empty malformed value: %+v", changed)
	}
	if len(unfixable) != 1 {
		t.Fatalf("expected 1 unfixable residual, got %v", unfixable)
	}
	if repaired != src {
		t.Fatal("src mutated despite refusal")
	}
	// And it genuinely still does not parse — the repair honestly did not fix it.
	if _, err := Parse(repaired); err == nil {
		t.Fatal("expected residual parse failure to persist")
	}
}

// TestRepairCorruptStamps_MixedEmptyAndValid: one corrupt task alongside a
// healthy one; only the corrupt header is rewritten, the healthy one is verbatim.
func TestRepairCorruptStamps_MixedEmptyAndValid(t *testing.T) {
	src := "# Plan\n\n" +
		"## good [TODO] <!-- attempts=0 deps= weight=8 -->\ngood body\n\n" +
		"## bad [DONE] <!-- attempts=1 deps= session= heartbeat= -->\nbad body\n"
	repaired, changed, unfixable := RepairCorruptStamps(src)
	if len(unfixable) != 0 || len(changed) != 1 || changed[0].ID != "bad" {
		t.Fatalf("changed=%+v unfixable=%v", changed, unfixable)
	}
	if !strings.Contains(repaired, "## good [TODO] <!-- attempts=0 deps= weight=8 -->") {
		t.Fatal("healthy header mutated")
	}
	if _, err := Parse(repaired); err != nil {
		t.Fatalf("repaired must parse: %v", err)
	}
}

// TestRepairCorruptStamps_Clean is a no-op on a healthy document.
func TestRepairCorruptStamps_Clean(t *testing.T) {
	src := "# Plan\n\n## t [TODO] <!-- attempts=0 deps= -->\nbody\n"
	repaired, changed, unfixable := RepairCorruptStamps(src)
	if len(changed) != 0 || len(unfixable) != 0 || repaired != src {
		t.Fatalf("clean doc changed: changed=%+v unfixable=%v", changed, unfixable)
	}
}
