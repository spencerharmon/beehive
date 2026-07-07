package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeTranscript drops a session transcript with the runner's header format into
// alpha's sessions dir. model=="" omits the stamp (models the pre-stamp history
// the stats page must default to opus).
func writeTranscript(t *testing.T, root, stem, kind, model string) {
	t.Helper()
	dir := filepath.Join(root, "submodules", "alpha", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tag := ""
	if model != "" {
		tag = " · model: " + model
	}
	body := "# session " + stem + "\n\nsubmodule: alpha · kind: " + kind +
		" · branch: bee-x" + tag + "\n\n## turn 1\nwork\n"
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestComputeStatsPerModel checks the by-model breakdown: honeybees are tallied
// per transcript-stamped model, an unstamped transcript defaults to opus, and a
// DONE task's delivery is attributed to the model of its MOST-RECENT session.
func TestComputeStatsPerModel(t *testing.T) {
	s, root := setup(t) // alpha PLAN has t3 [DONE]; t1 TODO, t2 NEEDS-HUMAN
	const opus = "github-copilot/claude-opus-4.8"
	const sonnet = "github-copilot/claude-sonnet-5"

	// t3 delivered: an earlier opus attempt then a later sonnet attempt -> credit
	// sonnet (higher epoch). t1 (not done) ran on opus. A legacy unstamped session
	// must fall to opus.
	writeTranscript(t, root, "bee-t3-900-4", "work", opus)
	writeTranscript(t, root, "bee-t3-1000-5", "work", sonnet)
	writeTranscript(t, root, "bee-t1-800-3", "work", opus)
	writeTranscript(t, root, "bee-legacy-700-2", "review", "") // unstamped -> opus

	subs, total, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var alpha *subStat
	for i := range subs {
		if subs[i].Name == "alpha" {
			alpha = &subs[i]
		}
	}
	if alpha == nil {
		t.Fatal("no alpha submodule in stats")
	}
	if alpha.Honeybees != 4 {
		t.Fatalf("honeybees: got %d want 4", alpha.Honeybees)
	}
	if alpha.DeliveredTasks != 1 { // only t3 is DONE
		t.Fatalf("delivered tasks: got %d want 1", alpha.DeliveredTasks)
	}
	byModel := map[string]modelStat{}
	for _, m := range alpha.Models {
		byModel[m.Model] = m
	}
	// sonnet: 1 session (t3-1000), delivered t3 (its latest session).
	if got := byModel[sonnet]; got.Honeybees != 1 || got.DeliveredTasks != 1 {
		t.Fatalf("sonnet: %+v want Honeybees=1 Delivered=1", got)
	}
	// opus: 3 sessions (t3-900, t1-800, legacy default), 0 delivered (t3 credited to sonnet).
	if got := byModel[opus]; got.Honeybees != 3 || got.DeliveredTasks != 0 {
		t.Fatalf("opus: %+v want Honeybees=3 Delivered=0", got)
	}
	if got := byModel[sonnet].DeliveredPerBeePct; got != 100 {
		t.Fatalf("sonnet yield: got %.1f want 100", got)
	}
	// Models are ordered most-honeybees-first: opus (3) before sonnet (1).
	if len(alpha.Models) != 2 || alpha.Models[0].Model != opus {
		t.Fatalf("model ordering: %+v", alpha.Models)
	}
	// Total row mirrors the single submodule.
	tm := map[string]modelStat{}
	for _, m := range total.Models {
		tm[m.Model] = m
	}
	if tm[opus].Honeybees != 3 || tm[sonnet].DeliveredTasks != 1 {
		t.Fatalf("total by model wrong: %+v", total.Models)
	}
}
