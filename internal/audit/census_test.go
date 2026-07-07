package audit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
)

// writeStub plants a repo.SessionStub placeholder at dir/<stem>.md pointing at
// branch — an UNFINALIZED session file, the shape the census must classify as a
// stub (not a parse error). stem (the file name) is the session id; branch is the
// distinct live branch the transcript streams to.
func writeStub(t *testing.T, dir, stem, branch string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(repo.SessionStub(branch)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseDirCensusClassifies is the binding acceptance: a sessions dir mixing
// finalized transcripts, repo.SessionStub files, and one malformed file yields
// correct counts (finalized N, stubs M with sids+branches, errors 1), and the
// stubs are NOT reported as generic parse errors. It also pins ParseDir's
// unchanged (sessions, joined-malformed-error) contract over the same dir.
func TestParseDirCensusClassifies(t *testing.T) {
	dir := t.TempDir()
	// Finalized (mineable) transcripts.
	writeSynth(t, dir, "bee-alpha-100", KindWork, 3, "")
	writeSynth(t, dir, "bee-beta-200", KindReview, 2, "")
	// Unfinalized stubs: the file stem (sid) deliberately differs from the branch
	// the stub streams to — exactly as the live runner writes them
	// (sessions/bee-<task>-<epoch>-<pid>.md holding a "<sm>-<epoch>-<pid>-session"
	// branch) — proving sid=stem and branch=parsed are recorded independently.
	writeStub(t, dir, "bee-gamma-300", "beehive-300-777-session")
	writeStub(t, dir, "bee-delta-400", "beehive-400-888-session")
	// One genuinely malformed file: no header, and not a stub.
	if err := os.WriteFile(filepath.Join(dir, "bee-garbage-500.md"),
		[]byte("# session\n\nthere is no header line here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := ParseDirCensus(dir)
	if err != nil {
		t.Fatalf("ParseDirCensus: %v", err)
	}
	if c.Finalized() != 2 {
		t.Errorf("finalized=%d want 2 (%v)", c.Finalized(), ids(c.Sessions))
	}
	if c.StubCount() != 2 {
		t.Fatalf("stubs=%d want 2 (%+v)", c.StubCount(), c.Stubs)
	}
	if c.ErrorCount() != 1 {
		t.Fatalf("errors=%d want 1 (%v)", c.ErrorCount(), c.Errors)
	}
	if c.Total() != 5 {
		t.Errorf("total=%d want 5", c.Total())
	}
	// Stubs record sid (file stem) + the branch they stream to, sid-sorted.
	wantStubs := []Stub{
		{SID: "bee-delta-400", Branch: "beehive-400-888-session"},
		{SID: "bee-gamma-300", Branch: "beehive-300-777-session"},
	}
	if !reflect.DeepEqual(c.Stubs, wantStubs) {
		t.Errorf("stubs=%+v want %+v", c.Stubs, wantStubs)
	}
	// The single error names ONLY the malformed file; no stub sid leaks into it.
	joined := errors.Join(c.Errors...).Error()
	if !strings.Contains(joined, "bee-garbage-500.md") {
		t.Errorf("error %q should name the malformed file", joined)
	}
	for _, stub := range []string{"bee-gamma-300", "bee-delta-400"} {
		if strings.Contains(joined, stub) {
			t.Errorf("stub %s wrongly reported as a parse error in %q", stub, joined)
		}
	}

	// ParseDir keeps its original contract: 2 sessions and a joined error naming
	// only the malformed file — stubs are neither sessions nor errors.
	ss, derr := ParseDir(dir)
	if len(ss) != 2 {
		t.Errorf("ParseDir sessions=%d want 2 (%v)", len(ss), ids(ss))
	}
	if derr == nil || !strings.Contains(derr.Error(), "bee-garbage-500.md") {
		t.Fatalf("ParseDir err=%v want it to name the malformed file", derr)
	}
	for _, stub := range []string{"bee-gamma-300", "bee-delta-400"} {
		if strings.Contains(derr.Error(), stub) {
			t.Errorf("ParseDir wrongly surfaced stub %s as an error", stub)
		}
	}
}

// TestParseDirCensusStubFixture parses the committed stub fixture (a real
// repo.SessionStub written to disk) and asserts it is classified as one
// unfinalized stub — zero finalized, zero errors — with its sid and branch.
func TestParseDirCensusStubFixture(t *testing.T) {
	c, err := ParseDirCensus(filepath.Join("testdata", "stubs"))
	if err != nil {
		t.Fatalf("ParseDirCensus stub fixture: %v", err)
	}
	if c.Finalized() != 0 || c.ErrorCount() != 0 {
		t.Fatalf("fixture finalized=%d errors=%d want 0/0 (%v)", c.Finalized(), c.ErrorCount(), c.Errors)
	}
	want := []Stub{{
		SID:    "bee-example-task-1782800000-98765",
		Branch: "beehive-1782800000-98765-session",
	}}
	if !reflect.DeepEqual(c.Stubs, want) {
		t.Errorf("stubs=%+v want %+v", c.Stubs, want)
	}
}

// TestCorpusFinalizedNotStubbed guards the false-positive direction: the real
// finalized corpus must classify entirely as mineable sessions with zero stubs
// and zero errors (a healthy, fully-finalized corpus).
func TestCorpusFinalizedNotStubbed(t *testing.T) {
	c, err := ParseDirCensus(filepath.Join("testdata", "sessions"))
	if err != nil {
		t.Fatalf("ParseDirCensus sessions: %v", err)
	}
	if c.Finalized() != len(corpus) || c.StubCount() != 0 || c.ErrorCount() != 0 {
		t.Fatalf("finalized=%d stubs=%d errors=%d want %d/0/0",
			c.Finalized(), c.StubCount(), c.ErrorCount(), len(corpus))
	}
	if f := c.MineableFraction(); f != 1 {
		t.Errorf("fully-finalized corpus mineable fraction=%v want 1.0", f)
	}
	if c.CorpusBroken(false) {
		t.Errorf("fully-finalized corpus wrongly flagged broken")
	}
}

// TestParseDirCensusModelHeader is the binding acceptance for the
// producer/consumer schema-drift fix: a directory holding ONLY a post-248e967
// four-field "· model: <model>" transcript must classify it as one finalized
// (mineable) session — NOT a parse error — with Model captured. Before the
// fix, the old anchored three-field regex rejected this exact shape, so every
// such session piled into Census.Errors and the corpus looked broken.
func TestParseDirCensusModelHeader(t *testing.T) {
	c, err := ParseDirCensus(filepath.Join("testdata", "model-header"))
	if err != nil {
		t.Fatalf("ParseDirCensus model-header fixture: %v", err)
	}
	if c.Finalized() != 1 || c.ErrorCount() != 0 || c.StubCount() != 0 {
		t.Fatalf("finalized=%d errors=%d stubs=%d want 1/0/0 (%v)", c.Finalized(), c.ErrorCount(), c.StubCount(), c.Errors)
	}
	if got := c.Sessions[0].Model; got != "github-copilot/claude-opus-4.8" {
		t.Errorf("model=%q want github-copilot/claude-opus-4.8", got)
	}
}

// mkCensus builds a Census with the given counts (element identity is irrelevant
// to the census math, which is purely count-based).
func mkCensus(finalized, stubs, errs int) Census {
	c := Census{}
	for i := 0; i < finalized; i++ {
		c.Sessions = append(c.Sessions, Session{ID: fmt.Sprintf("f%d", i)})
	}
	for i := 0; i < stubs; i++ {
		c.Stubs = append(c.Stubs, Stub{SID: fmt.Sprintf("s%d", i)})
	}
	for i := 0; i < errs; i++ {
		c.Errors = append(c.Errors, fmt.Errorf("e%d", i))
	}
	return c
}

// mkCensusGone builds a Census like mkCensus but splits its stubs between live
// (GoneBranch == false) and gone (GoneBranch == true), so CorpusBroken's
// gone-stub exemption math (NonGoneStubCount) can be exercised directly,
// without a coordination repo or ClassifyStubs.
func mkCensusGone(finalized, live, gone, errs int) Census {
	c := Census{}
	for i := 0; i < finalized; i++ {
		c.Sessions = append(c.Sessions, Session{ID: fmt.Sprintf("f%d", i)})
	}
	for i := 0; i < live; i++ {
		c.Stubs = append(c.Stubs, Stub{SID: fmt.Sprintf("live%d", i), Branch: fmt.Sprintf("live-branch-%d", i)})
	}
	for i := 0; i < gone; i++ {
		c.Stubs = append(c.Stubs, Stub{SID: fmt.Sprintf("gone%d", i), Branch: fmt.Sprintf("gone-branch-%d", i), GoneBranch: true})
	}
	for i := 0; i < errs; i++ {
		c.Errors = append(c.Errors, fmt.Errorf("e%d", i))
	}
	return c
}

// TestCensusMineableMath pins the census arithmetic, including the empty-dir edge
// (defined as fully mineable so a genuinely empty sessions dir never warns).
func TestCensusMineableMath(t *testing.T) {
	cases := []struct {
		fin, stub, err int
		total          int
		frac           float64
	}{
		{0, 0, 0, 0, 1.0},  // empty dir: fully mineable by definition
		{3, 1, 0, 4, 0.75}, // one live stream against three finalized
		{1, 2, 0, 3, 1.0 / 3},
		{30, 729, 0, 759, 30.0 / 759}, // the ~96%-loss defect
		{2, 0, 2, 4, 0.5},             // malformed files also count against the fraction
	}
	for _, tc := range cases {
		c := mkCensus(tc.fin, tc.stub, tc.err)
		if c.Total() != tc.total {
			t.Errorf("total(%d,%d,%d)=%d want %d", tc.fin, tc.stub, tc.err, c.Total(), tc.total)
		}
		if c.Finalized() != tc.fin || c.StubCount() != tc.stub || c.ErrorCount() != tc.err {
			t.Errorf("counts(%d,%d,%d)=%d/%d/%d", tc.fin, tc.stub, tc.err,
				c.Finalized(), c.StubCount(), c.ErrorCount())
		}
		if got := c.MineableFraction(); got != tc.frac {
			t.Errorf("fraction(%d,%d,%d)=%v want %v", tc.fin, tc.stub, tc.err, got, tc.frac)
		}
	}
}

// TestCorpusBroken exercises the warning decision across the four regimes.
func TestCorpusBroken(t *testing.T) {
	cases := []struct {
		name        string
		fin, stub   int
		windowEmpty bool
		broken      bool
	}{
		// Rest: everything finalized, window empty only because all audited.
		{"rest-empty-window", 6, 0, true, false},
		// Defect: window empty WHILE stubs exist — the exact starve signature.
		{"unfinalized-empty-window", 3, 5, true, true},
		// Degradation: majority stubs, window not yet empty -> fires below thresh.
		{"low-fraction-nonempty", 1, 2, false, true},
		// Healthy: a few live streams, high fraction, non-empty window -> silent.
		{"few-stubs-high-fraction", 9, 1, false, false},
		// No stubs is never broken, even with a low fraction from malformed files.
		{"no-stubs-never-broken", 1, 0, true, false},
	}
	for _, tc := range cases {
		c := mkCensus(tc.fin, tc.stub, 0)
		if got := c.CorpusBroken(tc.windowEmpty); got != tc.broken {
			t.Errorf("%s: CorpusBroken(%v)=%v want %v (frac=%.3f)",
				tc.name, tc.windowEmpty, got, tc.broken, c.MineableFraction())
		}
	}
}

// TestCorpusBrokenMalformed pins the malformed-class fold-in: a corpus with any
// malformed files is broken regardless of window state or stub fraction — a
// parser/producer regression must self-announce even with zero stubs to blame
// it on (the stub short-circuit used to return false before ever consulting
// ErrorCount(), the exact silent-starvation regression audit-malformed-
// visibility exists to close) and even when the window is non-empty and
// finalized sessions still exist to mine.
func TestCorpusBrokenMalformed(t *testing.T) {
	cases := []struct {
		name        string
		fin, errs   int
		windowEmpty bool
	}{
		{"errors-only-empty-window", 0, 3, true},                // the exact regression case: stubs==0
		{"errors-with-finalized-nonempty-window", 20, 3, false}, // "still flags" despite work left to mine
		{"single-malformed-file", 5, 1, false},
	}
	for _, tc := range cases {
		c := mkCensus(tc.fin, 0, tc.errs)
		if !c.CorpusBroken(tc.windowEmpty) {
			t.Errorf("%s: CorpusBroken(windowEmpty=%v)=false want true (fin=%d errs=%d)",
				tc.name, tc.windowEmpty, tc.fin, tc.errs)
		}
	}
}

// TestCorpusCleanNeverBroken guards the regression direction: zero stubs AND
// zero errors must never flag broken in any window state, so a healthy rested
// swarm stays byte-for-byte silent (CorpusWarning == "").
func TestCorpusCleanNeverBroken(t *testing.T) {
	c := mkCensus(42, 0, 0)
	for _, windowEmpty := range []bool{true, false} {
		if c.CorpusBroken(windowEmpty) {
			t.Errorf("clean corpus CorpusBroken(windowEmpty=%v)=true want false", windowEmpty)
		}
		if w := c.CorpusWarning(windowEmpty); w != "" {
			t.Errorf("clean corpus CorpusWarning(windowEmpty=%v)=%q want \"\" (byte-stable)", windowEmpty, w)
		}
	}
}

// TestCorpusFractionThreshold pins the low-fraction boundary: just below
// LowMineableFraction fires, exactly at/above it is silent and byte-stable.
func TestCorpusFractionThreshold(t *testing.T) {
	// 2 finalized + 2 stubs = 0.50 == threshold -> NOT below -> silent.
	at := mkCensus(2, 2, 0)
	if at.MineableFraction() != LowMineableFraction {
		t.Fatalf("fraction=%v want exactly the threshold %v", at.MineableFraction(), LowMineableFraction)
	}
	if at.CorpusBroken(false) {
		t.Errorf("fraction exactly at threshold must not fire (non-empty window)")
	}
	if w := at.CorpusWarning(false); w != "" {
		t.Errorf("at-threshold warning=%q want empty (byte-stable)", w)
	}
	// 1 finalized + 2 stubs = 0.33 < threshold -> fires.
	below := mkCensus(1, 2, 0)
	if !below.CorpusBroken(false) {
		t.Errorf("fraction %.3f below threshold must fire", below.MineableFraction())
	}
}

// TestCorpusWarningByteStable asserts the warning is silent (zero bytes) for a
// healthy corpus and loud+informative for the defect, and that it distinguishes
// the empty-window case in its text.
func TestCorpusWarningByteStable(t *testing.T) {
	// Healthy: no stubs -> exactly zero bytes, regardless of window state.
	healthy := mkCensus(6, 0, 0)
	if w := healthy.CorpusWarning(true); w != "" {
		t.Errorf("healthy empty-window warning=%q want '' (rest, byte-stable)", w)
	}
	if w := healthy.CorpusWarning(false); w != "" {
		t.Errorf("healthy warning=%q want '' (byte-stable)", w)
	}
	// Defect with an empty window: loud banner naming the counts and the cause.
	defect := mkCensus(30, 729, 0)
	w := defect.CorpusWarning(true)
	if w == "" {
		t.Fatal("defect warning empty, want the loud corpus-broken banner")
	}
	for _, want := range []string{"CORPUS BROKEN, NOT A REST", "729", "759", "EMPTY"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning missing %q:\n%s", want, w)
		}
	}
	// Non-empty-window low-fraction defect omits the empty-window sentence but
	// still fires.
	w2 := mkCensus(1, 2, 0).CorpusWarning(false)
	if w2 == "" || strings.Contains(w2, "window is EMPTY") {
		t.Errorf("low-fraction non-empty warning=%q want fired without the empty-window line", w2)
	}
}

// TestCorpusWarningNamesMalformed is the binding acceptance for the malformed
// class: Errors>0 with zero stubs names the malformed count and steers to
// audit-parse-model-header — NOT session-transcript-finalize, the stub fix.
// This line was silently missing before this task: CorpusWarning short-
// circuited via CorpusBroken's stub-only check before ever consulting
// ErrorCount(), so this exact case (regression: session-audit-004's 10 new
// malformed files) rendered "" instead of the loud banner.
func TestCorpusWarningNamesMalformed(t *testing.T) {
	c := mkCensus(0, 0, 10)
	w := c.CorpusWarning(true)
	if w == "" {
		t.Fatal("malformed-only warning empty, want the loud corpus-broken banner (regression: was silent)")
	}
	for _, want := range []string{"CORPUS BROKEN, NOT A REST", "10 of 10", "MALFORMED", "audit-parse-model-header"} {
		if !strings.Contains(w, want) {
			t.Errorf("malformed warning missing %q:\n%s", want, w)
		}
	}
	// Must NOT credit the defect to the stub fix — the two classes have
	// different causes and different remedies.
	if strings.Contains(w, "UNFINALIZED") || strings.Contains(w, "session-transcript-finalize") {
		t.Errorf("malformed-only warning wrongly names the stub fix:\n%s", w)
	}
}

// TestCorpusWarningMixedNamesBoth pins the mixed-corpus acceptance (stubs>0 AND
// errors>0, as session-audit-004 actually observed): the banner must name BOTH
// classes, each with its own distinct fix, never collapsing one into the other.
func TestCorpusWarningMixedNamesBoth(t *testing.T) {
	c := mkCensus(805, 99, 10) // mirrors the observed pass: finalized/stubs/malformed
	w := c.CorpusWarning(false)
	if w == "" {
		t.Fatal("mixed corpus warning empty, want the loud banner")
	}
	for _, want := range []string{
		"UNFINALIZED", "session-transcript-finalize",
		"MALFORMED", "audit-parse-model-header",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("mixed warning missing %q:\n%s", want, w)
		}
	}
}

// TestCorpusBrokenGoneStubsExempt is the binding acceptance for
// audit-gone-stub-exempt (session-audit-006 Finding #2): a stub CONFIRMED
// permanently gone (its stream branch resolves nowhere) must never be able to
// trip CorpusBroken on a future genuinely-rested window purely because the
// dead file still exists — mirroring the real corpus, which held EXACTLY 99
// such legacy stubs stable across three passes while zero errors occurred. A
// single NEW live (non-gone) stub alongside the 99 gone ones must still flag
// exactly as before (no regression on the guard's original purpose), and a
// mixed gone+live corpus whose LIVE-only fraction falls below threshold must
// still flag even with a non-empty window.
func TestCorpusBrokenGoneStubsExempt(t *testing.T) {
	// All 99 stubs confirmed gone, empty window, zero errors: the exact false
	// alarm this task diagnosed (regression: today CorpusBroken(true) == true,
	// CorpusWarning prints the "corpus broken" banner over dead files).
	allGone := mkCensusGone(801, 0, 99, 0)
	if allGone.CorpusBroken(true) {
		t.Error("all-gone stubs + empty window + zero errors wrongly flagged broken (the false-alarm-on-a-rest case this task closes)")
	}
	if w := allGone.CorpusWarning(true); w != "" {
		t.Errorf("all-gone stubs + empty window warning=%q want \"\" (byte-stable, no false alarm)", w)
	}

	// One NEW live stub alongside the 99 gone ones, window still empty: the
	// guard's ORIGINAL purpose (an unfinalized live stream during a claimed
	// rest) must still fire — the gone exemption must not swallow a real
	// defect just because dead stubs are also present.
	oneLive := mkCensusGone(801, 1, 99, 0)
	if !oneLive.CorpusBroken(true) {
		t.Error("one live stub among 99 gone + empty window must still flag (no regression on the guard's original purpose)")
	}
	if w := oneLive.CorpusWarning(true); w == "" {
		t.Error("one live stub among 99 gone + empty window must still print the corpus-broken banner")
	}

	// Mixed gone+live where the LIVE-only fraction is below threshold, window
	// NOT empty: must still flag via the fraction branch (3 finalized / (3
	// live + 3 finalized) = 0.5... use asymmetric counts so it is strictly
	// below LowMineableFraction regardless of how the gone stubs are counted).
	mixed := mkCensusGone(3, 5, 50, 0) // live-only fraction 3/(3+5) = 0.375 < 0.5
	if !mixed.CorpusBroken(false) {
		t.Error("mixed gone+live with non-gone-only fraction below threshold must still flag")
	}

	// Symmetric sanity check: mixed gone+live whose LIVE-only fraction is AT
	// the threshold, non-empty window, stays silent — the gone exemption must
	// not itself manufacture a NEW false positive at the boundary.
	atThreshold := mkCensusGone(5, 5, 40, 0) // live-only fraction 5/(5+5) = 0.5 == threshold
	if atThreshold.CorpusBroken(false) {
		t.Error("mixed gone+live with non-gone-only fraction exactly at threshold must not flag")
	}
}

// TestClassifyStubs is the binding acceptance for the resolver wiring: it must
// classify GoneBranch per the SAME two-step resolution order
// internal/swarm/sweep.go's resolveRef uses — refs/heads/<branch> first, then
// refs/remotes/<remote>/<branch>, else gone — so cmd/beehive/cmd_audit.go's
// git-backed BranchResolver and the finalize sweep never disagree on what
// counts as a gone branch.
func TestClassifyStubs(t *testing.T) {
	c := Census{Stubs: []Stub{
		{SID: "s-heads", Branch: "resolves-via-heads"},
		{SID: "s-remote", Branch: "resolves-via-remote"},
		{SID: "s-gone", Branch: "resolves-nowhere"},
	}}
	resolve := func(branch string) string {
		switch branch {
		case "resolves-via-heads":
			return "refs/heads/" + branch
		case "resolves-via-remote":
			return "refs/remotes/origin/" + branch
		default:
			return ""
		}
	}
	ClassifyStubs(&c, resolve)
	want := map[string]bool{"s-heads": false, "s-remote": false, "s-gone": true}
	for _, s := range c.Stubs {
		if s.GoneBranch != want[s.SID] {
			t.Errorf("stub %s GoneBranch=%v want %v", s.SID, s.GoneBranch, want[s.SID])
		}
	}
}

// TestClassifyStubsNilResolver pins the safe no-op: without a resolver (e.g. a
// caller with no coordination-repo access), every stub keeps GoneBranch ==
// false, so CorpusBroken behaves exactly as it did before this field existed.
func TestClassifyStubsNilResolver(t *testing.T) {
	c := Census{Stubs: []Stub{{SID: "s", Branch: "whatever"}}}
	ClassifyStubs(&c, nil)
	if c.Stubs[0].GoneBranch {
		t.Error("nil resolver must not classify anything gone")
	}
}
