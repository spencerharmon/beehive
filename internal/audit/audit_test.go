package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
)

// want captures the expected, corpus-verified metrics for a fixture session.
type want struct {
	epoch     int64
	kind      string
	taskid    string
	bytes     int64
	turns     int
	userTurns int
}

// corpus is the six current sessions copied verbatim into testdata/sessions
// (NOT trimmed) so these assertions reproduce the ROI-cited 14–102 turn /
// 60–330 KB spread against real data, per "validate against the real corpus, do
// not hardcode." Values were extracted with the pinned exact-line turn rule.
var corpus = map[string]want{
	"bee-bootstrap-1782766865":               {1782766865, KindBootstrap, "bootstrap", 154791, 50, 1},
	"bee-links-graph-enforcement-1782767318": {1782767318, KindWork, "links-graph-enforcement", 329739, 102, 3},
	"bee-links-graph-enforcement-1782772942": {1782772942, KindWork, "links-graph-enforcement", 219180, 68, 3},
	"bee-links-graph-enforcement-1782781988": {1782781988, KindReview, "links-graph-enforcement", 162015, 48, 1},
	"bee-reconcile-1782772649":               {1782772649, KindReconcile, "reconcile", 64390, 14, 1},
	"bee-reconcile-1782781231":               {1782781231, KindReconcile, "reconcile", 114877, 33, 1},
}

func loadCorpus(t *testing.T) []Session {
	t.Helper()
	ss, err := ParseDir(filepath.Join("testdata", "sessions"))
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if len(ss) != len(corpus) {
		t.Fatalf("parsed %d sessions, want %d", len(ss), len(corpus))
	}
	return ss
}

func index(ss []Session) map[string]Session {
	m := make(map[string]Session, len(ss))
	for _, s := range ss {
		m[s.ID] = s
	}
	return m
}

func TestParseCorpus(t *testing.T) {
	by := index(loadCorpus(t))
	for id, w := range corpus {
		s, ok := by[id]
		if !ok {
			t.Fatalf("missing session %s", id)
		}
		if s.Epoch != w.epoch {
			t.Errorf("%s epoch=%d want %d", id, s.Epoch, w.epoch)
		}
		if s.Kind != w.kind {
			t.Errorf("%s kind=%q want %q", id, s.Kind, w.kind)
		}
		if s.Branch != "bee-"+w.taskid {
			t.Errorf("%s branch=%q want bee-%s", id, s.Branch, w.taskid)
		}
		if s.TaskID != w.taskid {
			t.Errorf("%s taskid=%q want %q", id, s.TaskID, w.taskid)
		}
		if s.Submodule != "beehive" {
			t.Errorf("%s submodule=%q want beehive", id, s.Submodule)
		}
		if s.Bytes != w.bytes {
			t.Errorf("%s bytes=%d want %d", id, s.Bytes, w.bytes)
		}
		if s.Turns != w.turns {
			t.Errorf("%s turns=%d want %d", id, s.Turns, w.turns)
		}
		if s.UserTurns != w.userTurns {
			t.Errorf("%s userTurns=%d want %d", id, s.UserTurns, w.userTurns)
		}
		// No fixture was runner-aborted, so every conservative flag is clear.
		if h := s.Heuristics; h.Aborted || h.LostRace || h.CompletionMiss {
			t.Errorf("%s unexpected abort flags %+v", id, h)
		}
	}
}

// TestSpread pins the turn-count rule against the ROI-cited spread: the corpus
// must land in 14–102 turns and 60–330 KB, with the extremes hit exactly.
func TestSpread(t *testing.T) {
	ss := loadCorpus(t)
	minT, maxT := 1<<30, 0
	for _, s := range ss {
		if s.Turns < 14 || s.Turns > 102 {
			t.Errorf("%s turns=%d outside ROI spread 14–102", s.ID, s.Turns)
		}
		if s.Bytes < 60_000 || s.Bytes > 330_000 {
			t.Errorf("%s bytes=%d outside ROI spread 60–330 KB", s.ID, s.Bytes)
		}
		if s.Turns < minT {
			minT = s.Turns
		}
		if s.Turns > maxT {
			maxT = s.Turns
		}
	}
	if minT != 14 || maxT != 102 {
		t.Fatalf("turn extremes [%d,%d], want [14,102] — turn rule drifted", minT, maxT)
	}
}

// TestParseDirSorted asserts ParseDir returns epoch-ascending order.
func TestParseDirSorted(t *testing.T) {
	ss := loadCorpus(t)
	for i := 1; i < len(ss); i++ {
		if ss[i-1].Epoch > ss[i].Epoch {
			t.Fatalf("not epoch-sorted at %d: %d > %d", i, ss[i-1].Epoch, ss[i].Epoch)
		}
	}
}

func TestGrouping(t *testing.T) {
	ss := loadCorpus(t)
	delivered := map[string]bool{"links-graph-enforcement": true}
	aggs := Aggregate(ss, delivered)
	by := map[string]TaskAgg{}
	for _, a := range aggs {
		by[a.TaskID] = a
	}
	if len(aggs) != 3 {
		t.Fatalf("task groups=%d want 3 (%v)", len(aggs), by)
	}
	lge := by["links-graph-enforcement"]
	if lge.Reruns != 3 || lge.Retries != 2 {
		t.Errorf("lge reruns=%d retries=%d want 3/2", lge.Reruns, lge.Retries)
	}
	if !lge.Delivered {
		t.Errorf("lge should be delivered")
	}
	if lge.Turns != 102+68+48 || lge.Bytes != 329739+219180+162015 {
		t.Errorf("lge turns=%d bytes=%d want 218/710934", lge.Turns, lge.Bytes)
	}
	// Sessions are epoch-ordered within the group.
	wantSess := []string{
		"bee-links-graph-enforcement-1782767318",
		"bee-links-graph-enforcement-1782772942",
		"bee-links-graph-enforcement-1782781988",
	}
	if !reflect.DeepEqual(lge.Sessions, wantSess) {
		t.Errorf("lge sessions=%v want %v", lge.Sessions, wantSess)
	}
	if rec := by["reconcile"]; rec.Reruns != 2 || rec.Retries != 1 || rec.Delivered {
		t.Errorf("reconcile agg=%+v want reruns2 retries1 delivered=false", rec)
	}
	if bs := by["bootstrap"]; bs.Reruns != 1 || bs.Delivered {
		t.Errorf("bootstrap agg=%+v want reruns1 delivered=false", bs)
	}
}

// TestDeliveredTrend asserts the tracked metric counts ONLY delivered (DONE)
// tasks: bootstrap and reconcile (not plan tasks, never DONE) are excluded.
func TestDeliveredTrend(t *testing.T) {
	ss := loadCorpus(t)
	aggs := Aggregate(ss, map[string]bool{"links-graph-enforcement": true})
	tr := ComputeTrend(aggs, 1)
	if tr.DeliveredTasks != 1 {
		t.Fatalf("deliveredTasks=%d want 1 (only lge is DONE)", tr.DeliveredTasks)
	}
	if tr.Turns != 218 || tr.Bytes != 710934 || tr.Retries != 2 {
		t.Errorf("trend turns=%d bytes=%d retries=%d want 218/710934/2", tr.Turns, tr.Bytes, tr.Retries)
	}
	if tr.TurnsPerTask() != 218 || tr.RetriesPerTask() != 2 {
		t.Errorf("per-task turns=%v retries=%v want 218/2", tr.TurnsPerTask(), tr.RetriesPerTask())
	}
	// Nothing delivered -> zero, no divide-by-zero.
	empty := ComputeTrend(Aggregate(ss, nil), 1)
	if empty.DeliveredTasks != 0 || empty.TurnsPerTask() != 0 {
		t.Errorf("empty trend=%+v want zeroed", empty)
	}
}

func TestDeliveredFromPlan(t *testing.T) {
	src := "<!-- Beehive-ROI: abc -->\n# Plan\n\n" +
		"## links-graph-enforcement [DONE] <!-- attempts=0 deps= -->\nbody\n\n" +
		"## session-metrics-extract [TODO] <!-- attempts=0 deps= -->\nbody\n\n" +
		"## claim-repull-verify [DONE] <!-- attempts=0 deps= -->\nbody\n"
	p, err := plan.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	d := DeliveredFromPlan(p)
	if !d["links-graph-enforcement"] || !d["claim-repull-verify"] {
		t.Errorf("delivered missing DONE tasks: %v", d)
	}
	if d["session-metrics-extract"] {
		t.Errorf("TODO task wrongly marked delivered")
	}
	if len(d) != 2 {
		t.Errorf("delivered=%v want exactly the 2 DONE tasks", d)
	}
}

func TestWindow(t *testing.T) {
	ss := loadCorpus(t)
	// N-2: the two most recent by epoch (1782781231, 1782781988) are excluded.
	w := Window(ss, nil)
	gotIDs := ids(w)
	wantIDs := []string{
		"bee-bootstrap-1782766865",
		"bee-links-graph-enforcement-1782767318",
		"bee-reconcile-1782772649",
		"bee-links-graph-enforcement-1782772942",
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("window=%v want %v (two newest excluded)", gotIDs, wantIDs)
	}
	// Un-audited selection skips ledger entries and advances as the ledger grows.
	audited := map[string]bool{
		"bee-bootstrap-1782766865":               true,
		"bee-links-graph-enforcement-1782767318": true,
	}
	w2 := ids(Window(ss, audited))
	want2 := []string{"bee-reconcile-1782772649", "bee-links-graph-enforcement-1782772942"}
	if !reflect.DeepEqual(w2, want2) {
		t.Fatalf("window after partial audit=%v want %v", w2, want2)
	}
	for _, id := range wantIDs {
		audited[id] = true
	}
	if w3 := Window(ss, audited); len(w3) != 0 {
		t.Fatalf("window fully audited=%v want empty", ids(w3))
	}
}

func TestWindowTooFew(t *testing.T) {
	two := []Session{{ID: "a", Epoch: 1}, {ID: "b", Epoch: 2}}
	if w := Window(two, nil); len(w) != 0 {
		t.Fatalf("window of 2 = %v, want empty (N-2 excludes both)", ids(w))
	}
}

// TestReconcileLoopCorpus: the real reconcile sessions have work between them, so
// neither is in a loop — the heuristic must NOT false-fire.
func TestReconcileLoopCorpus(t *testing.T) {
	for _, s := range loadCorpus(t) {
		if s.Heuristics.ReconcileLoop {
			t.Errorf("%s wrongly flagged reconcile-loop (work sits between reconciles)", s.ID)
		}
	}
}

// TestReconcileLoopAdjacent: two back-to-back reconcile sessions (no other-kind
// session between) are both flagged; flanking work sessions are not.
func TestReconcileLoopAdjacent(t *testing.T) {
	dir := t.TempDir()
	writeSynth(t, dir, "bee-other-100", KindWork, 3, "")
	writeSynth(t, dir, "bee-reconcile-101", KindReconcile, 2, "")
	writeSynth(t, dir, "bee-reconcile-102", KindReconcile, 2, "")
	writeSynth(t, dir, "bee-other2-103", KindWork, 3, "")
	ss, err := ParseDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	by := index(ss)
	if !by["bee-reconcile-101"].Heuristics.ReconcileLoop || !by["bee-reconcile-102"].Heuristics.ReconcileLoop {
		t.Errorf("adjacent reconcile sessions not flagged: %+v %+v",
			by["bee-reconcile-101"].Heuristics, by["bee-reconcile-102"].Heuristics)
	}
	if by["bee-other-100"].Heuristics.ReconcileLoop || by["bee-other2-103"].Heuristics.ReconcileLoop {
		t.Errorf("work session wrongly flagged reconcile-loop")
	}
}

// TestAbortHeuristics drives the warning-block path: a synthetic aborted work
// session yields Aborted+LostRace+CompletionMiss; a clean one yields nothing.
func TestAbortHeuristics(t *testing.T) {
	aborted := synth("bee-foo", KindWork, 5, "lost the race; another session holds it. STOP.")
	s, err := parseTranscript("bee-foo-7.md", []byte(aborted))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Heuristics
	if !h.Aborted || !h.LostRace || !h.CompletionMiss {
		t.Errorf("aborted work flags=%+v want all true", h)
	}
	if h.AbortReason != "lost the race; another session holds it. STOP." {
		t.Errorf("abortReason=%q", h.AbortReason)
	}
	// A clean session: no warning block, no flags — even though its body quotes
	// the protocol's "lost the race" language, which must NOT trigger LostRace.
	clean := synth("bee-foo", KindWork, 5, "") +
		"\n## assistant\n\nThe rules say: if you lost the race, ErrLost, STOP.\n"
	s2, err := parseTranscript("bee-foo-8.md", []byte(clean))
	if err != nil {
		t.Fatal(err)
	}
	if h := s2.Heuristics; h.Aborted || h.LostRace || h.CompletionMiss {
		t.Errorf("clean session flags=%+v want all false (prompt pollution must not trigger)", h)
	}
}

// TestAbortNonWork: an aborted reconcile is Aborted but not CompletionMiss
// (CompletionMiss is work-only).
func TestAbortNonWork(t *testing.T) {
	s, err := parseTranscript("bee-reconcile-9.md",
		[]byte(synth("bee-reconcile", KindReconcile, 3, "wall cap reached")))
	if err != nil {
		t.Fatal(err)
	}
	if h := s.Heuristics; !h.Aborted || h.CompletionMiss || h.LostRace {
		t.Errorf("aborted reconcile flags=%+v want aborted-only", h)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := parseTranscript("noepoch.md", []byte("# x\n\nsubmodule: beehive \u00b7 kind: work \u00b7 branch: bee-x\n")); err == nil {
		t.Errorf("expected error for missing -<epoch> suffix")
	}
	if _, err := parseTranscript("bee-x-5.md", []byte("# x\n\nno header here\n")); err == nil {
		t.Errorf("expected error for missing header line")
	}
	// File-name branch must agree with the header branch.
	bad := "# x\n\nsubmodule: beehive \u00b7 kind: work \u00b7 branch: bee-y\n"
	if _, err := parseTranscript("bee-x-5.md", []byte(bad)); err == nil {
		t.Errorf("expected error for header/file branch mismatch")
	}
}

// TestParseHeaderModel pins the producer/consumer schema-drift fix (commit
// 248e967 appended a fourth "· model: <model>" field that the old three-field
// anchored regex rejected outright, dropping every post-stamp session). The
// header is now an ORDERED "·"-separated key:value list: submodule/kind/branch
// are still required in that exact order, but any trailing extra field is
// accepted (and a recognised one, "model", is captured) rather than rejecting
// the whole line.
func TestParseHeaderModel(t *testing.T) {
	const withModel = "# x\n\nsubmodule: beehive \u00b7 kind: work \u00b7 branch: bee-x \u00b7 model: github-copilot/claude-opus-4.8\n"
	s, err := parseTranscript("bee-x-5.md", []byte(withModel))
	if err != nil {
		t.Fatalf("model-header transcript: %v", err)
	}
	if s.Submodule != "beehive" || s.Kind != "work" || s.Branch != "bee-x" {
		t.Errorf("sub/kind/branch=%q/%q/%q want beehive/work/bee-x", s.Submodule, s.Kind, s.Branch)
	}
	if s.Model != "github-copilot/claude-opus-4.8" {
		t.Errorf("model=%q want github-copilot/claude-opus-4.8", s.Model)
	}

	// The legacy three-field header (no model tag at all, predating 248e967)
	// still parses, with Model left at its zero value.
	const legacy = "# x\n\nsubmodule: beehive \u00b7 kind: work \u00b7 branch: bee-x\n"
	s2, err := parseTranscript("bee-x-5.md", []byte(legacy))
	if err != nil {
		t.Fatalf("legacy header: %v", err)
	}
	if s2.Model != "" {
		t.Errorf("legacy header model=%q want \"\"", s2.Model)
	}

	// An unrecognised trailing field (a FUTURE header addition this parser does
	// not yet know about) must still parse rather than rebreaking the consumer
	// again the next time the producer grows the header.
	const unknownTrailing = "# x\n\nsubmodule: beehive \u00b7 kind: work \u00b7 branch: bee-x \u00b7 host: runner-3\n"
	s3, err := parseTranscript("bee-x-5.md", []byte(unknownTrailing))
	if err != nil {
		t.Fatalf("unknown trailing field: %v", err)
	}
	if s3.Submodule != "beehive" || s3.Kind != "work" || s3.Branch != "bee-x" || s3.Model != "" {
		t.Errorf("unknown-trailing-field session=%+v want sub/kind/branch=beehive/work/bee-x, model=\"\"", s3)
	}

	// A header missing the required branch field is STILL rejected, model tag or
	// not.
	const missingBranch = "# x\n\nsubmodule: beehive \u00b7 kind: work \u00b7 model: github-copilot/claude-opus-4.8\n"
	if _, err := parseTranscript("bee-x-5.md", []byte(missingBranch)); err == nil {
		t.Errorf("expected error for header missing the required branch field")
	}

	// Fields out of order are STILL rejected — the ordered submodule/kind/branch
	// requirement is unchanged, only trailing extras are new.
	const outOfOrder = "# x\n\nsubmodule: beehive \u00b7 branch: bee-x \u00b7 kind: work \u00b7 model: github-copilot/claude-opus-4.8\n"
	if _, err := parseTranscript("bee-x-5.md", []byte(outOfOrder)); err == nil {
		t.Errorf("expected error for out-of-order header fields")
	}
}

// TestParsePidSuffixName pins the live-runner naming fix: session files are now
// "<branch>-<epoch>-<pid>.md" (internal/swarm.SessionID appends a per-process
// suffix for fan-out). The epoch must be the FIRST numeric segment after the
// header-authoritative branch, with the -<pid> tail kept opaque (in the ID but
// never folded into branch/taskid). A greedy last-"-<digits>" split — the
// pre-fix bug — would have mis-attributed the suffix and rejected a valid file.
func TestParsePidSuffixName(t *testing.T) {
	s, err := parseTranscript("bee-git-remote-ops-1782789603-253372.md",
		[]byte(synth("bee-git-remote-ops", KindWork, 4, "")))
	if err != nil {
		t.Fatal(err)
	}
	if s.Epoch != 1782789603 {
		t.Errorf("epoch=%d want 1782789603 (first segment after branch, not the -pid)", s.Epoch)
	}
	if s.Branch != "bee-git-remote-ops" || s.TaskID != "git-remote-ops" {
		t.Errorf("branch=%q taskid=%q want bee-git-remote-ops/git-remote-ops (pid not folded in)", s.Branch, s.TaskID)
	}
	if s.ID != "bee-git-remote-ops-1782789603-253372" {
		t.Errorf("id=%q want the full stem including the -pid suffix", s.ID)
	}
	// A branch whose own name ends in a numeric segment still splits at the epoch,
	// because the split is anchored on the header branch, not a greedy guess.
	s2, err := parseTranscript("bee-x-2-1782789603-253372.md",
		[]byte(synth("bee-x-2", KindReview, 2, "")))
	if err != nil {
		t.Fatal(err)
	}
	if s2.Epoch != 1782789603 || s2.Branch != "bee-x-2" || s2.TaskID != "x-2" {
		t.Errorf("epoch=%d branch=%q taskid=%q want 1782789603/bee-x-2/x-2", s2.Epoch, s2.Branch, s2.TaskID)
	}
}

// TestParsePidFixture parses the real "<branch>-<epoch>-<pid>" transcript copied
// verbatim from the live corpus and asserts its true metrics, proving the fix on
// actual runner output (not just synthetic names).
func TestParsePidFixture(t *testing.T) {
	s, err := ParseFile(filepath.Join("testdata", "pid", "bee-git-remote-ops-1782789603-253372.md"))
	if err != nil {
		t.Fatalf("ParseFile pid fixture: %v", err)
	}
	if s.ID != "bee-git-remote-ops-1782789603-253372" || s.Epoch != 1782789603 {
		t.Errorf("id=%q epoch=%d want bee-git-remote-ops-1782789603-253372/1782789603", s.ID, s.Epoch)
	}
	if s.Branch != "bee-git-remote-ops" || s.TaskID != "git-remote-ops" || s.Kind != KindWork {
		t.Errorf("branch=%q taskid=%q kind=%q want bee-git-remote-ops/git-remote-ops/work", s.Branch, s.TaskID, s.Kind)
	}
	if s.Bytes != 118274 || s.Turns != 37 || s.UserTurns != 1 {
		t.Errorf("bytes=%d turns=%d userTurns=%d want 118274/37/1", s.Bytes, s.Turns, s.UserTurns)
	}
}

// TestParseModelHeaderFixture parses a committed post-248e967 transcript (the
// real four-field "· model: <model>" header the live runner now writes) and
// asserts it parses cleanly with Model captured, proving the fix against actual
// on-disk transcript bytes rather than just an inline string.
func TestParseModelHeaderFixture(t *testing.T) {
	path := filepath.Join("testdata", "model-header", "bee-audit-parse-model-header-1783408031.md")
	s, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile model-header fixture: %v", err)
	}
	if s.Submodule != "beehive" || s.Kind != KindWork {
		t.Errorf("sub=%q kind=%q want beehive/work", s.Submodule, s.Kind)
	}
	if s.Branch != "bee-audit-parse-model-header" || s.TaskID != "audit-parse-model-header" {
		t.Errorf("branch=%q taskid=%q want bee-audit-parse-model-header/audit-parse-model-header", s.Branch, s.TaskID)
	}
	if s.Model != "github-copilot/claude-opus-4.8" {
		t.Errorf("model=%q want github-copilot/claude-opus-4.8", s.Model)
	}
	if s.Epoch != 1783408031 {
		t.Errorf("epoch=%d want 1783408031", s.Epoch)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if s.Bytes != int64(len(data)) {
		t.Errorf("bytes=%d want %d (actual file size)", s.Bytes, len(data))
	}
	if s.Turns != 2 || s.UserTurns != 1 {
		t.Errorf("turns=%d userTurns=%d want 2/1", s.Turns, s.UserTurns)
	}
}

// TestParseDirMixedScheme is the binding acceptance: ParseDir (the exact path
// `beehive audit` runs) over a directory mixing "<branch>-<epoch>" and
// "<branch>-<epoch>-<pid>" names succeeds (no error, every file parsed) and
// yields a non-empty N-2 window, so the dependent session-audit can run.
func TestParseDirMixedScheme(t *testing.T) {
	dir := t.TempDir()
	copyTree(t, filepath.Join("testdata", "sessions"), dir) // six "<branch>-<epoch>" fixtures
	copyTree(t, filepath.Join("testdata", "pid"), dir)      // one real "<branch>-<epoch>-<pid>"

	ss, err := ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir mixed scheme: %v", err)
	}
	if len(ss) != len(corpus)+1 {
		t.Fatalf("parsed %d sessions, want %d (mixed scheme, none dropped)", len(ss), len(corpus)+1)
	}
	pid, ok := index(ss)["bee-git-remote-ops-1782789603-253372"]
	if !ok {
		t.Fatalf("pid-suffixed session missing from %v", ids(ss))
	}
	if pid.Epoch != 1782789603 || pid.Branch != "bee-git-remote-ops" || pid.TaskID != "git-remote-ops" {
		t.Errorf("pid session epoch=%d branch=%q taskid=%q want 1782789603/bee-git-remote-ops/git-remote-ops",
			pid.Epoch, pid.Branch, pid.TaskID)
	}
	if w := Window(ss, nil); len(w) == 0 {
		t.Fatalf("mixed-scheme window empty, want non-empty (audit pass would have nothing to do)")
	}
}

// TestParseDirResilient: one genuinely malformed transcript must not zero the
// batch. The good sessions still parse and the per-file failure is surfaced in
// the joined error (never swallowed) rather than aborting the whole pass.
func TestParseDirResilient(t *testing.T) {
	dir := t.TempDir()
	writeSynth(t, dir, "bee-alpha-100", KindWork, 3, "")
	writeSynth(t, dir, "bee-beta-200", KindReview, 2, "")
	if err := os.WriteFile(filepath.Join(dir, "bee-garbage-300.md"),
		[]byte("# session\n\nthere is no header line here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ss, err := ParseDir(dir)
	if err == nil {
		t.Fatalf("expected a surfaced per-file error for the malformed transcript")
	}
	if len(ss) != 2 {
		t.Fatalf("got %d good sessions, want 2 (one malformed file must not zero the batch)", len(ss))
	}
	if !strings.Contains(err.Error(), "bee-garbage-300.md") {
		t.Errorf("error %q should name the malformed file", err)
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ss := loadCorpus(t)
	led := &Ledger{}
	if got := led.NextPass(); got != 1 {
		t.Fatalf("empty NextPass=%d want 1", got)
	}
	// Pass 1: audit the N-2 window.
	w1 := Window(ss, led.Audited())
	led.AppendPass(w1, ComputeTrend(Aggregate(w1, map[string]bool{"links-graph-enforcement": true}), led.NextPass()))
	if err := led.Save(dir); err != nil {
		t.Fatal(err)
	}
	// The ledger file lives under docs/audit (here a temp dir) and round-trips.
	re, err := LoadLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(re.Metrics, led.Metrics) {
		t.Fatalf("metrics round-trip mismatch:\n got %+v\nwant %+v", re.Metrics, led.Metrics)
	}
	if !reflect.DeepEqual(re.Trends, led.Trends) {
		t.Fatalf("trend round-trip mismatch:\n got %+v\nwant %+v", re.Trends, led.Trends)
	}
	// Audited entries are now skipped by the next window.
	for _, s := range w1 {
		if !re.Audited()[s.ID] {
			t.Errorf("session %s not marked audited", s.ID)
		}
	}
	if got := re.NextPass(); got != 2 {
		t.Fatalf("NextPass after one pass=%d want 2", got)
	}
	// Pass 2: trend rows accumulate.
	w2 := Window(ss, re.Audited())
	re.AppendPass(w2, ComputeTrend(Aggregate(w2, nil), re.NextPass()))
	if err := re.Save(dir); err != nil {
		t.Fatal(err)
	}
	re2, err := LoadLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(re2.Trends) != 2 {
		t.Fatalf("trend rows=%d want 2 (accumulating)", len(re2.Trends))
	}
	if re2.Trends[0].Pass != 1 || re2.Trends[1].Pass != 2 {
		t.Errorf("trend passes=%d,%d want 1,2", re2.Trends[0].Pass, re2.Trends[1].Pass)
	}
}

// TestLedgerMissing: a fresh dir loads as an empty ledger, not an error.
func TestLedgerMissing(t *testing.T) {
	l, err := LoadLedger(t.TempDir())
	if err != nil {
		t.Fatalf("LoadLedger empty dir: %v", err)
	}
	if len(l.Metrics) != 0 || len(l.Trends) != 0 || l.NextPass() != 1 {
		t.Errorf("empty ledger=%+v", l)
	}
}

// --- helpers ---

func ids(ss []Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

// synth builds a minimal but format-faithful transcript: the "# session" line,
// the "submodule · kind · branch" header, one user turn, n assistant turns, and
// an optional trailing "## ⚠️ warning" block.
func synth(branch, kind string, turns int, warning string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# session %s\n\nsubmodule: beehive \u00b7 kind: %s \u00b7 branch: %s\n", branch, kind, branch)
	b.WriteString("\n## user\n\nprompt\n")
	for i := 0; i < turns; i++ {
		b.WriteString("\n## assistant\n\nwork\n")
	}
	if warning != "" {
		fmt.Fprintf(&b, "\n## \u26a0\ufe0f warning\n\n%s\n", warning)
	}
	return b.String()
}

func writeSynth(t *testing.T, dir, stem, kind string, turns int, warning string) {
	t.Helper()
	// The synthetic header branch must match the file stem minus -<epoch>.
	branch := stem[:strings.LastIndexByte(stem, '-')]
	content := synth(branch, kind, turns, warning)
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// copyTree copies every regular file in srcDir into dstDir verbatim. Used to
// assemble a mixed-scheme sessions directory from the committed fixtures.
func copyTree(t *testing.T, srcDir, dstDir string) {
	t.Helper()
	ents, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dstDir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
