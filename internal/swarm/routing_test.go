package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// modelClient is a Client that ALSO implements modelSelector, recording every
// model the runner routes to (in order) so a test can assert per-kind dispatch.
// Its session is a plain mockSession the test can drive to terminal.
type modelClient struct {
	sess Session
	set  []string // models SetModel was called with, in dispatch order
}

func (c *modelClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	if c.sess == nil {
		c.sess = &mockSession{}
	}
	return c.sess, nil
}
func (c *modelClient) SetModel(model string) { c.set = append(c.set, model) }

// workFixture builds a minimal beehive repo with one submodule holding an
// IN-PROGRESS Work task (mirrors TestRunCompletes' setup) and returns the opened
// repo, its beehive git, the submodule dir, the PLAN.md path, and a Work
// selection — the shared scaffold for the routing and stall tests.
func workFixture(t *testing.T) (*repo.Repo, *git.Repo, string, string, *selectt.Selection) {
	t.Helper()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")
	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
	return rp, g, sm, planPath, sel
}

// completeWork returns an onTurn that drives a Work task terminal: it writes the
// required change doc and flips PLAN.md to DONE, so the runner's completion check
// passes and the run ends cleanly.
func completeWork(sm, planPath string) func(int) {
	return func(int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}
}

// TestRunRoutesModelPerKind proves per-kind routing: a code Work dispatch selects
// the STRONG model and a trivial kind (bootstrap) selects the CHEAP one, via the
// optional modelSelector capability, before the session opens.
func TestRunRoutesModelPerKind(t *testing.T) {
	models := map[string]string{"work": "strong/model", "bootstrap": "cheap/model"}
	modelFor := func(kind string) string { return models[kind] }

	// --- Work -> strong ---
	rp, g, sm, planPath, sel := workFixture(t)
	work := &modelClient{sess: &mockSession{onTurn: completeWork(sm, planPath)}}
	rw := &Runner{Repo: rp, Git: g, Client: work, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, ModelFor: modelFor}
	if _, err := rw.Run(context.Background(), sel, "sys", "first"); err != nil {
		t.Fatalf("work run: %v", err)
	}
	if len(work.set) == 0 || work.set[0] != "strong/model" {
		t.Fatalf("work dispatch routed to %v, want strong/model", work.set)
	}

	// --- Bootstrap -> cheap ---
	root := t.TempDir()
	gb := gitInit(t, root)
	repo.Init(root)
	smb := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(smb, "docs"), 0o755)
	os.WriteFile(filepath.Join(smb, "ROI.md"), []byte("# ROI\nbuild it\n"), 0o644)
	gb.Commit(context.Background(), "seed roi")
	rpb, _ := repo.Open(root)
	subsb, _ := rpb.Submodules()
	selb := &selectt.Selection{Kind: selectt.Bootstrap, Submodule: subsb[0]}
	boot := &modelClient{sess: &mockSession{onTurn: func(int) {
		os.WriteFile(subsb[0].PlanPath(), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	rb := &Runner{Repo: rpb, Git: gb, Client: boot, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, ModelFor: modelFor}
	if _, err := rb.Run(context.Background(), selb, "sys", "first"); err != nil {
		t.Fatalf("bootstrap run: %v", err)
	}
	if len(boot.set) == 0 || boot.set[0] != "cheap/model" {
		t.Fatalf("bootstrap dispatch routed to %v, want cheap/model", boot.set)
	}
}

// TestRunModelRoutingInertByDefault proves the inert default: with no ModelFor
// (single-model host) the runner never calls SetModel, and a ModelFor that
// returns "" for the kind is equally inert — the client keeps its own model.
func TestRunModelRoutingInertByDefault(t *testing.T) {
	// No ModelFor at all.
	rp, g, sm, planPath, sel := workFixture(t)
	cl := &modelClient{sess: &mockSession{onTurn: completeWork(sm, planPath)}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	if _, err := r.Run(context.Background(), sel, "sys", "first"); err != nil {
		t.Fatalf("run (nil ModelFor): %v", err)
	}
	if len(cl.set) != 0 {
		t.Fatalf("SetModel called %v with nil ModelFor; must be inert", cl.set)
	}

	// ModelFor present but empty for this kind (no routing configured for it).
	rp2, g2, sm2, planPath2, sel2 := workFixture(t)
	cl2 := &modelClient{sess: &mockSession{onTurn: completeWork(sm2, planPath2)}}
	r2 := &Runner{Repo: rp2, Git: g2, Client: cl2, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		ModelFor: func(string) string { return "" }}
	if _, err := r2.Run(context.Background(), sel2, "sys", "first"); err != nil {
		t.Fatalf("run (empty ModelFor): %v", err)
	}
	if len(cl2.set) != 0 {
		t.Fatalf("SetModel called %v when ModelFor returned \"\"; must be inert", cl2.set)
	}
}

// TestStallDetectorObserve unit-tests the counting: disabled (limit<=0) never
// trips; a constant fingerprint trips exactly when `limit` consecutive repeats
// accumulate; a changed fingerprint resets the streak.
func TestStallDetectorObserve(t *testing.T) {
	// Inert when disabled.
	off := &stallDetector{limit: 0}
	for i := 0; i < 5; i++ {
		if off.observe("x") {
			t.Fatalf("limit=0 tripped at i=%d; must be inert", i)
		}
	}
	// limit=2: reference (repeats 0), then two repeats -> trip on the 3rd identical.
	d := &stallDetector{limit: 2}
	if d.observe("a") {
		t.Fatal("tripped on the reference observation")
	}
	if d.observe("a") {
		t.Fatal("tripped after only one repeat (want 2)")
	}
	if !d.observe("a") {
		t.Fatal("did not trip after two repeats")
	}
	// A changed fingerprint resets the streak.
	d2 := &stallDetector{limit: 2}
	d2.observe("a")
	d2.observe("a") // repeats=1
	if d2.observe("b") {
		t.Fatal("tripped on a changed fingerprint; the streak must reset")
	}
	if d2.observe("b") { // repeats=1 again
		t.Fatal("tripped one repeat after reset")
	}
	if !d2.observe("b") { // repeats=2
		t.Fatal("did not trip two repeats after reset")
	}
}

// TestRunStallCapsIdleChurn proves the real (git-fingerprint) path: an idle Work
// agent that never changes a file leaves the code worktree fingerprint identical
// every turn, so with StallTurns=3 the runner abandons the pass for GC at turn 4
// — bounded and surfaced — far short of the MaxTurns=10 budget, never a silent
// full-budget burn.
func TestRunStallCapsIdleChurn(t *testing.T) {
	rp, g, _, _, sel := workFixture(t)
	r := &Runner{Repo: rp, Git: g, Client: &mockClient{}, MaxTurns: 10, WallCap: time.Hour, TTL: time.Hour, StallTurns: 3}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed || !res.GCMarked {
		t.Fatalf("want idle-churn GC cap, got %+v", res)
	}
	if res.Turns != 4 {
		t.Fatalf("stall cap fired at turn %d, want 4 (StallTurns=3 => 3 unchanged turns after the first)", res.Turns)
	}
	if !strings.Contains(res.Warning, "forward progress") {
		t.Fatalf("idle-churn abandonment not surfaced: %q", res.Warning)
	}
}

// TestRunStallDoesNotKillProgressingAgent proves no false kill: when each turn
// makes forward progress (a fingerprint that changes every turn) even the most
// aggressive StallTurns=1 never trips, so the run proceeds to the ordinary
// MaxTurns cap (turns=MaxTurns+1) instead of an early idle-churn abandonment. The
// progress signal is injected so the property is exercised hermetically.
func TestRunStallDoesNotKillProgressingAgent(t *testing.T) {
	rp, g, _, _, sel := workFixture(t)
	var n int
	r := &Runner{Repo: rp, Git: g, Client: &mockClient{}, MaxTurns: 4, WallCap: time.Hour, TTL: time.Hour,
		StallTurns: 1, Progress: func(context.Context) string { n++; return fmt.Sprintf("sig-%d", n) }}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed {
		t.Fatalf("idle agent should not complete: %+v", res)
	}
	if res.Turns != 5 {
		t.Fatalf("changing progress must not stall-kill; reached turn %d, want 5 (MaxTurns cap)", res.Turns)
	}
	if strings.Contains(res.Warning, "forward progress") {
		t.Fatalf("idle-churn kill fired despite forward progress: %q", res.Warning)
	}
}
