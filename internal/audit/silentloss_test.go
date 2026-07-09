package audit

import "testing"

// mustParseDir parses a synthetic sessions dir (each file written by writeSynth)
// through the full ParseDir path, so the corpus-level SilentLoss flag is applied.
func mustParseDir(t *testing.T, dir string) []Session {
	t.Helper()
	ss, err := ParseDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

// TestSilentLossGenuinePair: two consecutive same-kind work sessions for one
// task. The first's flip never landed (the task was re-dispatched at TODO as a
// second work session), so the FIRST is the silent loss; the SECOND, which
// actually re-did the work, is not.
func TestSilentLossGenuinePair(t *testing.T) {
	dir := t.TempDir()
	writeSynth(t, dir, "bee-widget-100", KindWork, 40, "")
	writeSynth(t, dir, "bee-widget-200", KindWork, 45, "")
	by := index(mustParseDir(t, dir))
	if !by["bee-widget-100"].Heuristics.SilentLoss {
		t.Errorf("earlier same-kind session not flagged SilentLoss: %+v", by["bee-widget-100"].Heuristics)
	}
	if by["bee-widget-200"].Heuristics.SilentLoss {
		t.Errorf("later (re-doing) session wrongly flagged SilentLoss: %+v", by["bee-widget-200"].Heuristics)
	}
}

// TestSilentLossProgressionNotFlagged: a legitimate work→review→arbitrate
// progression (a DIFFERENT kind/status each hop) is never a silent loss — this
// is the binding "must not false-flag a real progression" Accept criterion.
func TestSilentLossProgressionNotFlagged(t *testing.T) {
	dir := t.TempDir()
	writeSynth(t, dir, "bee-flow-100", KindWork, 10, "")
	writeSynth(t, dir, "bee-flow-200", KindReview, 8, "")
	writeSynth(t, dir, "bee-flow-300", KindArbitration, 6, "")
	for _, s := range mustParseDir(t, dir) {
		if s.Heuristics.SilentLoss {
			t.Errorf("progression session %s (%s) wrongly flagged SilentLoss", s.ID, s.Kind)
		}
	}
}

// TestSilentLossInterleavedReviewNotFlagged: work→review→work for one task. The
// review's existence proves the first work's flip landed, and review is a
// different kind, so nothing is flagged — the interleaved task-bearing session
// correctly breaks the same-kind adjacency.
func TestSilentLossInterleavedReviewNotFlagged(t *testing.T) {
	dir := t.TempDir()
	writeSynth(t, dir, "bee-loop-100", KindWork, 10, "")
	writeSynth(t, dir, "bee-loop-200", KindReview, 5, "")
	writeSynth(t, dir, "bee-loop-300", KindWork, 12, "")
	for _, s := range mustParseDir(t, dir) {
		if s.Heuristics.SilentLoss {
			t.Errorf("work→review→work session %s (%s) wrongly flagged SilentLoss", s.ID, s.Kind)
		}
	}
}

// TestSilentLossChain: three consecutive work sessions. The task was
// re-dispatched at TODO twice, so the first TWO are silent losses; the LAST has
// no successor to prove its flip was discarded and is never flagged.
func TestSilentLossChain(t *testing.T) {
	dir := t.TempDir()
	writeSynth(t, dir, "bee-chain-100", KindWork, 10, "")
	writeSynth(t, dir, "bee-chain-200", KindWork, 11, "")
	writeSynth(t, dir, "bee-chain-300", KindWork, 12, "")
	by := index(mustParseDir(t, dir))
	if !by["bee-chain-100"].Heuristics.SilentLoss || !by["bee-chain-200"].Heuristics.SilentLoss {
		t.Errorf("first two of a 3-work chain must be silent losses: 100=%+v 200=%+v",
			by["bee-chain-100"].Heuristics, by["bee-chain-200"].Heuristics)
	}
	if by["bee-chain-300"].Heuristics.SilentLoss {
		t.Errorf("last in chain wrongly flagged (no successor proves a discard): %+v", by["bee-chain-300"].Heuristics)
	}
}

// TestSilentLossExcludesNonTaskBearing: adjacent reconcile sessions are the
// reconcile-LOOP pattern (its own heuristic), NOT a silent loss, and bootstrap
// carries no handoff — both kinds are excluded even when two share a synthetic
// TaskID and kind.
func TestSilentLossExcludesNonTaskBearing(t *testing.T) {
	dir := t.TempDir()
	writeSynth(t, dir, "bee-reconcile-100", KindReconcile, 3, "")
	writeSynth(t, dir, "bee-reconcile-200", KindReconcile, 3, "")
	writeSynth(t, dir, "bee-bootstrap-100", KindBootstrap, 5, "")
	writeSynth(t, dir, "bee-bootstrap-200", KindBootstrap, 5, "")
	for _, s := range mustParseDir(t, dir) {
		if s.Heuristics.SilentLoss {
			t.Errorf("non-task-bearing session %s (%s) wrongly flagged SilentLoss", s.ID, s.Kind)
		}
	}
}

// TestSilentLossCrossTaskNotFlagged: two same-kind work sessions for DIFFERENT
// tasks are unrelated — neither is a silent loss (the successor is a different
// task, not a re-dispatch of this one).
func TestSilentLossCrossTaskNotFlagged(t *testing.T) {
	dir := t.TempDir()
	writeSynth(t, dir, "bee-one-100", KindWork, 10, "")
	writeSynth(t, dir, "bee-two-200", KindWork, 10, "")
	for _, s := range mustParseDir(t, dir) {
		if s.Heuristics.SilentLoss {
			t.Errorf("cross-task session %s wrongly flagged SilentLoss", s.ID)
		}
	}
}

// TestSilentLossCorpus: over the committed real corpus, the ONLY silent loss is
// the first links-graph-enforcement work session — it was dispatched as work
// twice (1782767318 then 1782772942) before a review ran, so its flip never
// landed. Every other fixture (the second work, the review, both reconciles, the
// bootstrap) must be clear. This is the read-only reproduction of the finding.
func TestSilentLossCorpus(t *testing.T) {
	by := index(loadCorpus(t))
	const lost = "bee-links-graph-enforcement-1782767318"
	if !by[lost].Heuristics.SilentLoss {
		t.Errorf("expected %s to be the corpus silent loss, flags=%+v", lost, by[lost].Heuristics)
	}
	for id, s := range by {
		if id == lost {
			continue
		}
		if s.Heuristics.SilentLoss {
			t.Errorf("session %s (%s) wrongly flagged SilentLoss", id, s.Kind)
		}
	}
}

// TestAggregateSilentLoss rolls the flagged sessions up per task, summing the
// wasted turns/bytes, sorted by TaskID, with clean tasks absent.
func TestAggregateSilentLoss(t *testing.T) {
	dir := t.TempDir()
	// task aaa: three works -> first two are silent losses.
	writeSynth(t, dir, "bee-aaa-100", KindWork, 5, "")
	writeSynth(t, dir, "bee-aaa-200", KindWork, 7, "")
	writeSynth(t, dir, "bee-aaa-300", KindWork, 9, "")
	// task bbb: two works -> one silent loss.
	writeSynth(t, dir, "bee-bbb-100", KindWork, 4, "")
	writeSynth(t, dir, "bee-bbb-200", KindWork, 6, "")
	// task ccc: clean progression -> zero, absent from the aggregate.
	writeSynth(t, dir, "bee-ccc-100", KindWork, 3, "")
	writeSynth(t, dir, "bee-ccc-200", KindReview, 2, "")

	aggs := AggregateSilentLoss(mustParseDir(t, dir))
	if len(aggs) != 2 {
		t.Fatalf("aggregate tasks=%d want 2 (aaa,bbb; ccc has none): %+v", len(aggs), aggs)
	}
	if aggs[0].TaskID != "aaa" || aggs[0].Count != 2 || aggs[0].Turns != 12 {
		t.Errorf("aaa agg=%+v want TaskID=aaa Count=2 Turns=12", aggs[0])
	}
	if got := aggs[0].Sessions; len(got) != 2 || got[0] != "bee-aaa-100" || got[1] != "bee-aaa-200" {
		t.Errorf("aaa lost sessions=%v want [bee-aaa-100 bee-aaa-200]", got)
	}
	if aggs[1].TaskID != "bbb" || aggs[1].Count != 1 || aggs[1].Turns != 4 {
		t.Errorf("bbb agg=%+v want TaskID=bbb Count=1 Turns=4", aggs[1])
	}
}
