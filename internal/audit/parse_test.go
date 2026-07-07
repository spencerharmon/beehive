package audit

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// audit-warning-tail-guard: the binding acceptance for the scanBody fix. A
// transcript's own turn count and abort classification must never be
// corrupted by a QUOTED "## ⚠️ warning" line the session's own work happens to
// embed — e.g. while grepping/tail-dumping a PRIOR session's abort block as
// evidence, which is the session-audit series' explicit, permanent charter.
// Only the LAST exact "## ⚠️ warning" line in the file is even a candidate
// abort marker, and only if it is genuinely trailing (no real
// "## assistant"/"## user" turn follows it) does it count.

// TestScanBodyTrailingOnlyRegression is the regression guard: a transcript
// whose ONLY "## ⚠️ warning" is genuinely trailing (nothing after it) must
// classify EXACTLY as before this fix.
func TestScanBodyTrailingOnlyRegression(t *testing.T) {
	body := synth("bee-trail", KindWork, 4,
		"turn 5 exceeded the 1h0m0s per-turn timeout (stalled agent); abandoning for GC")
	s, err := parseTranscript("bee-trail-100.md", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if s.Turns != 4 || s.UserTurns != 1 {
		t.Fatalf("turns=%d userTurns=%d want 4/1", s.Turns, s.UserTurns)
	}
	h := s.Heuristics
	if !h.Aborted || !h.CompletionMiss {
		t.Fatalf("trailing-only warning must still classify Aborted+CompletionMiss: %+v", h)
	}
	if h.AbortReason != "turn 5 exceeded the 1h0m0s per-turn timeout (stalled agent); abandoning for GC" {
		t.Errorf("abortReason=%q", h.AbortReason)
	}
	if h.LostRace {
		t.Errorf("LostRace=true, want false (no lost-claim language)")
	}
}

// TestScanBodyQuotedWarningNotTrailing: a quoted "## ⚠️ warning" block (inside
// a fenced tool-output quoting another file) followed by MORE real
// "## assistant"/"## user" turns and a normal ending must NOT classify as an
// abort, and Turns/UserTurns must count the ENTIRE file (regression: before
// this fix the engine stopped counting at the quoted line and false-flagged
// every downstream field).
func TestScanBodyQuotedWarningNotTrailing(t *testing.T) {
	var b strings.Builder
	fmt.Fprintf(&b, "# session bee-quoted\n\nsubmodule: beehive \u00b7 kind: work \u00b7 branch: bee-quoted\n")
	b.WriteString("\n## user\n\nplease investigate the aborted sessions\n")
	b.WriteString("\n## assistant\n\nSampling a prior abort block:\n\n```\n" +
		"## \u26a0\ufe0f warning\n\nturn 1 exceeded the 1h0m0s per-turn timeout (stalled agent); abandoning for GC\n```\n")
	b.WriteString("\n## assistant\n\nNow implementing the fix.\n")
	b.WriteString("\n## user\n\nlooks good, ship it\n")
	b.WriteString("\n## assistant\n\nDone: committed and pushed.\n")

	s, err := parseTranscript("bee-quoted-200.md", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if s.Turns != 3 {
		t.Fatalf("turns=%d want 3 (whole file, not gated by the quoted warning line)", s.Turns)
	}
	if s.UserTurns != 2 {
		t.Fatalf("userTurns=%d want 2", s.UserTurns)
	}
	h := s.Heuristics
	if h.Aborted || h.CompletionMiss || h.LostRace {
		t.Fatalf("quoted (non-trailing) warning must not classify as an abort: %+v", h)
	}
	if h.AbortReason != "" {
		t.Errorf("abortReason=%q want empty", h.AbortReason)
	}
}

// TestScanBodyLastOccurrenceWins: two occurrences — an early quoted one and a
// genuinely trailing real one at EOF — must classify off the LAST occurrence
// only (its own AbortReason), with turns counted for the whole file up to (not
// including) that true trailing marker.
func TestScanBodyLastOccurrenceWins(t *testing.T) {
	var b strings.Builder
	fmt.Fprintf(&b, "# session bee-lastwins\n\nsubmodule: beehive \u00b7 kind: work \u00b7 branch: bee-lastwins\n")
	b.WriteString("\n## user\n\nplease investigate the aborted sessions\n")
	b.WriteString("\n## assistant\n\nSampling a prior abort block:\n\n```\n" +
		"## \u26a0\ufe0f warning\n\nturn 1 exceeded the 1h0m0s per-turn timeout (stalled agent); abandoning for GC\n```\n")
	b.WriteString("\n## assistant\n\nContinuing the analysis.\n")
	b.WriteString("\n## \u26a0\ufe0f warning\n\nturn 9 made no progress within the 15m0s idle timeout (stalled agent); abandoning for GC\n")

	s, err := parseTranscript("bee-lastwins-300.md", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if s.Turns != 2 {
		t.Fatalf("turns=%d want 2 (both real assistant turns, up to the true trailing marker)", s.Turns)
	}
	if s.UserTurns != 1 {
		t.Fatalf("userTurns=%d want 1", s.UserTurns)
	}
	h := s.Heuristics
	if !h.Aborted || !h.CompletionMiss {
		t.Fatalf("genuinely trailing real marker must classify as aborted: %+v", h)
	}
	if h.AbortReason != "turn 9 made no progress within the 15m0s idle timeout (stalled agent); abandoning for GC" {
		t.Errorf("abortReason=%q want the LAST occurrence's reason, not the quoted one", h.AbortReason)
	}
	if h.LostRace {
		t.Errorf("LostRace=true, want false")
	}
}

// TestParseQuotedWarningFixtureSessionAudit003 parses the real corpus file
// that first exposed this defect (session-audit-003, ledger pass 3): its OWN
// work greps and tail-dumps a PRIOR aborted session's transcript while
// sampling abort patterns, embedding an exact "## ⚠️ warning" line at 1468 of
// 5268 — but the file's true end (a normal "Deliverables" summary) carries
// none. Before this fix the engine stopped at the quoted line and reported 18
// turns / aborted / lost_race / completion_miss; after the fix it must report
// the file's REAL 85 turns and no abort flags at all.
func TestParseQuotedWarningFixtureSessionAudit003(t *testing.T) {
	path := filepath.Join("testdata", "quoted-warning", "bee-session-audit-003-1783408453-51640.md")
	s, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if s.Branch != "bee-session-audit-003" || s.TaskID != "session-audit-003" || s.Kind != KindWork {
		t.Errorf("branch=%q taskid=%q kind=%q want bee-session-audit-003/session-audit-003/work",
			s.Branch, s.TaskID, s.Kind)
	}
	if s.Epoch != 1783408453 {
		t.Errorf("epoch=%d want 1783408453", s.Epoch)
	}
	if s.Model != "github-copilot/claude-opus-4.8" {
		t.Errorf("model=%q want github-copilot/claude-opus-4.8", s.Model)
	}
	if s.Bytes != 369218 {
		t.Errorf("bytes=%d want 369218 (verbatim copy of the real file)", s.Bytes)
	}
	if s.Turns != 85 {
		t.Errorf("turns=%d want 85 real turns (regression: pre-fix engine stopped at 18)", s.Turns)
	}
	if s.UserTurns != 5 {
		t.Errorf("userTurns=%d want 5", s.UserTurns)
	}
	h := s.Heuristics
	if h.Aborted || h.LostRace || h.CompletionMiss {
		t.Errorf("flags=%+v want all false (the quoted warning at line 1468 is not this session's "+
			"own abort; regression: pre-fix flagged aborted/lost_race/completion_miss)", h)
	}
	if h.AbortReason != "" {
		t.Errorf("abortReason=%q want empty", h.AbortReason)
	}
}

// TestParseQuotedWarningFixtureSessionAudit005 parses the real corpus file
// that shows the identical shape (session-audit-005, ledger pass 4, audited by
// session-audit-006): its own work dumps two OTHER sessions' trailing warning
// blocks while categorizing abort reasons, embedding exact "## ⚠️ warning"
// lines at 1354 and 1372 of 3919 — but the file's true end is the pass's own
// successful "Deliverables"/"Verification" summary. Before this fix the
// engine reported 16 turns / aborted / lost_race / completion_miss; after the
// fix it must report the file's REAL 70 turns and no abort flags at all.
func TestParseQuotedWarningFixtureSessionAudit005(t *testing.T) {
	path := filepath.Join("testdata", "quoted-warning", "bee-session-audit-005-1783429207-734855.md")
	s, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if s.Branch != "bee-session-audit-005" || s.TaskID != "session-audit-005" || s.Kind != KindWork {
		t.Errorf("branch=%q taskid=%q kind=%q want bee-session-audit-005/session-audit-005/work",
			s.Branch, s.TaskID, s.Kind)
	}
	if s.Epoch != 1783429207 {
		t.Errorf("epoch=%d want 1783429207", s.Epoch)
	}
	if s.Model != "github-copilot/claude-opus-4.8" {
		t.Errorf("model=%q want github-copilot/claude-opus-4.8", s.Model)
	}
	if s.Bytes != 294115 {
		t.Errorf("bytes=%d want 294115 (verbatim copy of the real file)", s.Bytes)
	}
	if s.Turns != 70 {
		t.Errorf("turns=%d want 70 real turns (regression: pre-fix engine stopped at 16)", s.Turns)
	}
	if s.UserTurns != 4 {
		t.Errorf("userTurns=%d want 4", s.UserTurns)
	}
	h := s.Heuristics
	if h.Aborted || h.LostRace || h.CompletionMiss {
		t.Errorf("flags=%+v want all false (regression: pre-fix flagged aborted/lost_race/completion_miss)", h)
	}
	if h.AbortReason != "" {
		t.Errorf("abortReason=%q want empty", h.AbortReason)
	}
}

// TestParseDirQuotedWarningFixtures is the directory-level acceptance: both
// real fixtures parse via ParseDir (the exact path `beehive audit` runs) as
// finalized, non-aborted sessions — never as parse errors, never flagged.
func TestParseDirQuotedWarningFixtures(t *testing.T) {
	ss, err := ParseDir(filepath.Join("testdata", "quoted-warning"))
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if len(ss) != 2 {
		t.Fatalf("parsed %d sessions, want 2", len(ss))
	}
	for _, s := range ss {
		if s.Heuristics.Aborted {
			t.Errorf("%s wrongly classified as aborted (a quoted warning line must not gate)", s.ID)
		}
	}
}
