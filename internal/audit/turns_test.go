package audit

import (
	"reflect"
	"strings"
	"testing"
)

// TestSplitTurnsMatchesScanBodyCounts locks the single-source-of-truth
// guarantee the session-transcript-rendered-toc ROI asked for: SplitTurns must
// carve out exactly as many "user"/"assistant" segments as scanBody counts for
// the SAME bytes, on both a hand-built transcript and the exact-line-vs-prefix
// edge case (an assistant turn embedding its own "## Notes" heading) that
// scanBody's PINNED rule exists to handle.
func TestSplitTurnsMatchesScanBodyCounts(t *testing.T) {
	body := synth("bee-splitcheck", KindWork, 3, "")
	turns, userTurns, _, err := scanBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	segs := SplitTurns([]byte(body))
	gotTurns, gotUser := 0, 0
	for _, sg := range segs {
		switch sg.Role {
		case "assistant":
			gotTurns++
		case "user":
			gotUser++
		}
	}
	if gotTurns != turns || gotUser != userTurns {
		t.Fatalf("SplitTurns counted turns=%d userTurns=%d, scanBody counted %d/%d", gotTurns, gotUser, turns, userTurns)
	}
}

// TestSplitTurnsExactLineNotPrefix proves SplitTurns uses the SAME exact-line
// rule as scanBody: an assistant turn that embeds its own "## Notes"/"## Goal"
// markdown heading must not be mistaken for a new turn boundary, and that
// embedded heading text must survive unmangled inside the turn's Body (so it
// renders as an ordinary heading, not silently vanish).
func TestSplitTurnsExactLineNotPrefix(t *testing.T) {
	body := "# session bee-x\n\nsubmodule: beehive \u00b7 kind: work \u00b7 branch: bee-x\n" +
		"\n## user\n\nplease write notes\n" +
		"\n## assistant\n\n## Notes\n\nhere are my notes\n\n## Goal\n\nship it\n"
	segs := SplitTurns([]byte(body))
	var assistant []Turn
	for _, sg := range segs {
		if sg.Role == "assistant" {
			assistant = append(assistant, sg)
		}
	}
	if len(assistant) != 1 {
		t.Fatalf("got %d assistant segments, want exactly 1 (embedded ## Notes/## Goal must not split)", len(assistant))
	}
	want := "## Notes\n\nhere are my notes\n\n## Goal\n\nship it"
	if assistant[0].Body != want {
		t.Fatalf("assistant body = %q, want %q", assistant[0].Body, want)
	}
}

// TestSplitTurnsPreamble locks the leading-segment contract: content before
// the first turn marker (the "# session <id>" title + metadata header line)
// comes back as its own Role=="" segment, first in the slice, so a caller can
// choose to drop or keep it without re-deriving where the header ends.
func TestSplitTurnsPreamble(t *testing.T) {
	body := synth("bee-preamble", KindWork, 1, "")
	segs := SplitTurns([]byte(body))
	if len(segs) < 3 {
		t.Fatalf("got %d segments, want at least 3 (preamble, user, assistant): %+v", len(segs), segs)
	}
	if segs[0].Role != "" {
		t.Fatalf("segs[0].Role = %q, want \"\" (preamble)", segs[0].Role)
	}
	if segs[1].Role != "user" || segs[2].Role != "assistant" {
		t.Fatalf("segs[1..2] roles = %q/%q, want user/assistant", segs[1].Role, segs[2].Role)
	}
}

// TestSplitTurnsNoMarkersYieldsOneSegment covers a transcript with NO
// "## assistant"/"## user" line at all (a legacy/malformed transcript, or any
// ad hoc text) — it must still come back as exactly one Role=="" segment
// wrapping the whole input, so a rendering caller always has something to
// render instead of silently dropping the file.
func TestSplitTurnsNoMarkersYieldsOneSegment(t *testing.T) {
	body := "# final transcript\nall done.\n"
	segs := SplitTurns([]byte(body))
	if !reflect.DeepEqual(segs, []Turn{{Role: "", Body: "# final transcript\nall done."}}) {
		t.Fatalf("segs = %+v, want a single Role==\"\" segment wrapping the whole body", segs)
	}
}

// TestSplitTurnsEmpty locks the empty-input contract: zero bytes in, nil (no
// segments) out — never a spurious empty segment.
func TestSplitTurnsEmpty(t *testing.T) {
	if segs := SplitTurns(nil); segs != nil {
		t.Fatalf("SplitTurns(nil) = %+v, want nil", segs)
	}
	if segs := SplitTurns([]byte("")); segs != nil {
		t.Fatalf(`SplitTurns("") = %+v, want nil`, segs)
	}
}

// TestSplitTurnsWarningBlockIsPlainBody proves SplitTurns does NOT
// special-case the trailing "## \u26a0\ufe0f warning" block the way scanBody
// does for abort classification: it is ordinary content folded into whichever
// segment it falls in (the last one, per the producer contract), so a
// rendering caller sees it as just another heading rather than losing it.
func TestSplitTurnsWarningBlockIsPlainBody(t *testing.T) {
	body := synth("bee-warned", KindWork, 1, "turn 5 exceeded the 1h0m0s per-turn timeout (stalled agent); abandoning for GC")
	segs := SplitTurns([]byte(body))
	last := segs[len(segs)-1]
	if last.Role != "assistant" {
		t.Fatalf("last segment role = %q, want assistant (warning folds into it, no new segment)", last.Role)
	}
	if !containsAll(last.Body, "## \u26a0\ufe0f warning", "turn 5 exceeded the 1h0m0s per-turn timeout") {
		t.Fatalf("last assistant segment must carry the trailing warning block verbatim: %q", last.Body)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
