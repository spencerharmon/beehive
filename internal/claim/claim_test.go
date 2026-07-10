package claim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

func setup(t *testing.T) (*Claimer, context.Context) {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if _, err := g.Run(ctx, a...); err != nil {
			t.Fatal(err)
		}
	}
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ndo it\n"), 0o644)
	g.Commit(ctx, "seed")
	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	return &Claimer{Repo: rp, Sub: subs[0], Git: g, TTL: time.Hour, Session: "bee-A"}, ctx
}

// writePlan overwrites the submodule PLAN.md and commits it.
func writePlan(t *testing.T, c *Claimer, ctx context.Context, body string) {
	t.Helper()
	if err := os.WriteFile(c.Sub.PlanPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Git.Commit(ctx, "edit plan"); err != nil && err != git.ErrNothing {
		t.Fatal(err)
	}
}

func TestClaimStampsSessionNotStatus(t *testing.T) {
	c, ctx := setup(t)
	ts := time.Now().UTC()
	if err := c.Claim(ctx, "T1", ts); err != nil {
		t.Fatalf("claim: %v", err)
	}
	p, _ := c.load()
	tk := p.Find("T1")
	if tk.Status != plan.TODO {
		t.Fatalf("claim changed status to %s; must stay TODO", tk.Status)
	}
	if tk.Session != "bee-A" || tk.Heartbeat.IsZero() {
		t.Fatalf("claim did not stamp session+heartbeat: %+v", tk)
	}
	if stale, _ := c.Stale("T1"); stale {
		t.Fatal("fresh claim reported stale")
	}
	if err := c.Heartbeat(ctx, "T1", plan.TODO, ts.Add(time.Minute)); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
}

// TestClaimContested: a live foreign session already holds the task, so Claim
// loses the race up front instead of stealing it.
func TestClaimContested(t *testing.T) {
	c, ctx := setup(t)
	now := time.Now().UTC().Truncate(time.Second)
	writePlan(t, c, ctx, "## T1 [TODO] <!-- attempts=0 deps= session=bee-B heartbeat="+now.Format(time.RFC3339)+" -->\ndo it\n")
	if err := c.Claim(ctx, "T1", now); !errors.Is(err, ErrLost) {
		t.Fatalf("want ErrLost on a live foreign claim, got %v", err)
	}
}

// TestHeartbeatResolved: once the task leaves the status this run was working
// (here TODO -> NEEDS-REVIEW), Heartbeat returns ErrResolved, not a fatal error.
func TestHeartbeatResolved(t *testing.T) {
	c, ctx := setup(t)
	if err := c.Claim(ctx, "T1", time.Now().UTC()); err != nil {
		t.Fatalf("claim: %v", err)
	}
	writePlan(t, c, ctx, "## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ndo it\n")
	err := c.Heartbeat(ctx, "T1", plan.TODO, time.Now().UTC())
	if !errors.Is(err, ErrResolved) {
		t.Fatalf("want ErrResolved, got %v", err)
	}
}

// TestHeartbeatLost: while we were away a live foreign session took the task;
// the next heartbeat detects it and returns ErrLost.
func TestHeartbeatLost(t *testing.T) {
	c, ctx := setup(t)
	if err := c.Claim(ctx, "T1", time.Now().UTC()); err != nil {
		t.Fatalf("claim: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	writePlan(t, c, ctx, "## T1 [TODO] <!-- attempts=0 deps= session=bee-B heartbeat="+now.Format(time.RFC3339)+" -->\ndo it\n")
	if err := c.Heartbeat(ctx, "T1", plan.TODO, now); !errors.Is(err, ErrLost) {
		t.Fatalf("want ErrLost, got %v", err)
	}
}

// TestReleaseClearsClaim: completion releases the claim so a peer can pick the
// task up immediately without waiting out the TTL.
func TestReleaseClearsClaim(t *testing.T) {
	c, ctx := setup(t)
	c.Claim(ctx, "T1", time.Now().UTC())
	if err := c.Release(ctx, "T1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	p, _ := c.load()
	tk := p.Find("T1")
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatalf("release did not clear the claim: %+v", tk)
	}
}

// TestRejectGuarded: Reject only applies to NEEDS-REVIEW/NEEDS-ARBITRATION; a
// TODO reject errors and leaves the task untouched.
func TestRejectGuarded(t *testing.T) {
	c, ctx := setup(t)
	if _, err := c.Reject(ctx, "T1", 3); err == nil {
		t.Fatal("reject on TODO must error")
	}
}

func TestRejectCounter(t *testing.T) {
	c, ctx := setup(t)
	setReview := func() {
		p, _ := c.load()
		p.Find("T1").Status = plan.NeedsReview
		os.WriteFile(c.Sub.PlanPath(), []byte(p.String()), 0o644)
		c.Git.Commit(ctx, "review")
	}
	for i := 0; i < 3; i++ {
		setReview()
		if s, err := c.Reject(ctx, "T1", 3); err != nil || s != plan.TODO {
			t.Fatalf("attempt %d: status %s err %v", i, s, err)
		}
	}
	setReview()
	if s, _ := c.Reject(ctx, "T1", 3); s != plan.NeedsHuman {
		t.Fatalf("over limit status %s", s)
	}
}

// TestStrandGuarded: Strand only applies to NEEDS-REVIEW; a TODO strand errors
// and leaves the task untouched.
func TestStrandGuarded(t *testing.T) {
	c, ctx := setup(t)
	if _, err := c.Strand(ctx, "T1", "origin unreachable", 3); err == nil {
		t.Fatal("strand on TODO must error")
	}
}

// TestStrandCounter mirrors TestRejectCounter: repeated strands recycle T1 to
// TODO up to limit, then overflow to NEEDS-HUMAN carrying the concrete reason —
// and, unlike Reject, does NOT publish (no Publish configured here would panic
// otherwise; Strand must never call it).
func TestStrandCounter(t *testing.T) {
	c, ctx := setup(t)
	setReview := func() {
		p, _ := c.load()
		p.Find("T1").Status = plan.NeedsReview
		os.WriteFile(c.Sub.PlanPath(), []byte(p.String()), 0o644)
		c.Git.Commit(ctx, "review")
	}
	for i := 0; i < 3; i++ {
		setReview()
		if s, err := c.Strand(ctx, "T1", "origin unreachable: dial tcp refused", 3); err != nil || s != plan.TODO {
			t.Fatalf("attempt %d: status %s err %v", i, s, err)
		}
	}
	setReview()
	s, err := c.Strand(ctx, "T1", "origin unreachable: dial tcp refused", 3)
	if err != nil || s != plan.NeedsHuman {
		t.Fatalf("over limit status %s err %v", s, err)
	}
	p, _ := c.load()
	if got := p.Find("T1").HumanReason(); got != "origin unreachable: dial tcp refused" {
		t.Fatalf("human reason = %q", got)
	}
}

// TestBounceUnreachablePublishesImmediately proves BounceUnreachable — unlike
// Strand/Reject — commits AND publishes on its own (it runs standalone before
// any turn loop, with no later finish() to piggyback on): the correction must
// reach the given Publish func synchronously so a peer sees NEEDS-ARBITRATION
// right away, never a review dispatched against it again.
func TestBounceUnreachablePublishesImmediately(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= session=bee-A heartbeat=2026-01-01T00:00:00Z -->\ndo it\n"), 0o644)
	g.Commit(ctx, "seed")

	wt := filepath.Join(root, ".worktrees", "bee-a")
	if err := g.WorktreeAdd(ctx, wt, "bee-a", "main"); err != nil {
		t.Fatal(err)
	}
	rp, _ := repo.Open(wt)
	subs, _ := rp.Submodules()
	wg := git.New(wt)
	published := false
	c := &Claimer{
		Repo: rp, Sub: subs[0], Git: wg, TTL: time.Hour, Session: "bee-a",
		Publish: func(ctx context.Context) error {
			published = true
			return wg.PublishToMain(ctx, "")
		},
	}
	if err := c.BounceUnreachable(ctx, "T1", "reviewable commit unreachable: implementer branch bee-T1 resolves nowhere"); err != nil {
		t.Fatalf("bounce-unreachable: %v", err)
	}
	if !published {
		t.Fatal("BounceUnreachable must publish immediately (no later finish() to rely on)")
	}
	// Read back from the ORIGINAL (main) checkout, proving the correction really
	// reached main and not just the worktree branch.
	mp, err := plan.Parse(mustRead(t, filepath.Join(sm, "PLAN.md")))
	if err != nil {
		t.Fatalf("parse main PLAN.md: %v", err)
	}
	tk := mp.Find("T1")
	if tk.Status != plan.NeedsArb {
		t.Fatalf("main status = %s, want NEEDS-ARBITRATION", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("bounce-unreachable must release the claim on main")
	}
	if joined := strings.Join(tk.Body, "\n"); !strings.Contains(joined, "reviewable commit unreachable") {
		t.Fatalf("main PLAN.md missing bounce reason:\n%s", joined)
	}
}

// TestBounceUnreachableGuarded: only legal from NEEDS-REVIEW.
func TestBounceUnreachableGuarded(t *testing.T) {
	c, ctx := setup(t)
	if err := c.BounceUnreachable(ctx, "T1", "reason"); err == nil {
		t.Fatal("bounce-unreachable on TODO must error")
	}
}

// TestRecoverLostWorkPublishesImmediately proves RecoverLostWork — like
// BounceUnreachable/FinalizeAlreadyMerged — commits AND publishes on its own
// (it runs standalone before any turn loop, with no later finish() to
// piggyback on): the TODO reset AND the incremented attempts must both reach
// the given Publish func synchronously so a peer sees a fresh, workable TODO
// right away, never a review/arbitration dispatched against lost work again.
func TestRecoverLostWorkPublishesImmediately(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= session=bee-A heartbeat=2026-01-01T00:00:00Z -->\ndo it\n"), 0o644)
	g.Commit(ctx, "seed")

	wt := filepath.Join(root, ".worktrees", "bee-a")
	if err := g.WorktreeAdd(ctx, wt, "bee-a", "main"); err != nil {
		t.Fatal(err)
	}
	rp, _ := repo.Open(wt)
	subs, _ := rp.Submodules()
	wg := git.New(wt)
	published := false
	c := &Claimer{
		Repo: rp, Sub: subs[0], Git: wg, TTL: time.Hour, Session: "bee-a",
		Publish: func(ctx context.Context) error {
			published = true
			return wg.PublishToMain(ctx, "")
		},
	}
	reason := "implementer work for T1 is unrecoverable: no local or remote bee-T1 ref, the submodule pointer carries no stamp for this task, and no change doc exists"
	if err := c.RecoverLostWork(ctx, "T1", reason, 3); err != nil {
		t.Fatalf("recover-lost-work: %v", err)
	}
	if !published {
		t.Fatal("RecoverLostWork must publish immediately (no later finish() to rely on)")
	}
	// Read back from the ORIGINAL (main) checkout, proving the correction
	// really reached main and not just the worktree branch.
	mp, err := plan.Parse(mustRead(t, filepath.Join(sm, "PLAN.md")))
	if err != nil {
		t.Fatalf("parse main PLAN.md: %v", err)
	}
	tk := mp.Find("T1")
	if tk.Status != plan.StatusTODO {
		t.Fatalf("main status = %s, want TODO", tk.Status)
	}
	if tk.Attempts != 1 {
		t.Fatalf("main attempts = %d, want 1", tk.Attempts)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("recover-lost-work must release the claim on main")
	}
	if joined := strings.Join(tk.Body, "\n"); !strings.Contains(joined, "unrecoverable") {
		t.Fatalf("main PLAN.md missing recovery reason:\n%s", joined)
	}
}

// TestRecoverLostWorkGuarded: only legal from NEEDS-REVIEW or
// NEEDS-ARBITRATION.
func TestRecoverLostWorkGuarded(t *testing.T) {
	c, ctx := setup(t)
	if err := c.RecoverLostWork(ctx, "T1", "reason", 3); err == nil {
		t.Fatal("recover-lost-work on TODO must error")
	}
}

// TestRecoverLostWorkPastLimitGoesHuman mirrors Strand/Reject's
// attempts/limit escalation: once attempts exceed limit, RecoverLostWork
// escalates to NEEDS-HUMAN instead of looping TODO forever.
func TestRecoverLostWorkPastLimitGoesHuman(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [NEEDS-ARBITRATION] <!-- attempts=3 deps= session=bee-A heartbeat=2026-01-01T00:00:00Z -->\ndo it\n"), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	c := &Claimer{Repo: rp, Sub: subs[0], Git: g, TTL: time.Hour, Session: "bee-a"}
	if err := c.RecoverLostWork(ctx, "T1", "reason", 3); err != nil {
		t.Fatalf("recover-lost-work: %v", err)
	}
	p, err := plan.Parse(mustRead(t, filepath.Join(sm, "PLAN.md")))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("T1")
	if tk.Status != plan.StatusHuman {
		t.Fatalf("status = %s, want NEEDS-HUMAN past the retry limit", tk.Status)
	}
}

// TestFinalizeAlreadyMergedPublishesImmediately proves FinalizeAlreadyMerged —
// like BounceUnreachable — commits AND publishes on its own (it runs standalone
// before any turn loop, with no later finish() to piggyback on): the DONE
// transition AND the bumped gitlink must both reach the given Publish func
// synchronously so a peer never re-dispatches a review against this task again.
// The gitlink assertion is the load-bearing one: it seeds the PRE-merge
// (stale/phantom) recorded pointer, then — mirroring exactly what
// swarm.finalizeIfAlreadyMerged does before calling this (sync the submodule
// checkout to the tracked tip) — makes submodules/sm/repo a REAL nested repo at
// a NEW sha, and asserts the committed gitlink lands at that new sha, proving
// CommitPaths read the synced checkout's actual HEAD rather than keeping (or
// blindly overwriting via a stale cached value) the pre-merge pointer.
func TestFinalizeAlreadyMergedPublishesImmediately(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= session=bee-A heartbeat=2026-01-01T00:00:00Z -->\ndo it\n"), 0o644)
	// Seed the PRE-merge recorded gitlink at a phantom sha, as a Work pass would
	// have left it (a commit that only ever lived on bee-T1, not yet folded into
	// tracked main).
	staleSHA := strings.Repeat("a", 40)
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+staleSHA+",submodules/sm/repo"); err != nil {
		t.Fatal(err)
	}
	g.Commit(ctx, "seed")

	wt := filepath.Join(root, ".worktrees", "bee-a")
	if err := g.WorktreeAdd(ctx, wt, "bee-a", "main"); err != nil {
		t.Fatal(err)
	}
	rp, _ := repo.Open(wt)
	subs, _ := rp.Submodules()
	wg := git.New(wt)

	// Simulate the caller's pre-sync: this worktree's OWN private gitlink
	// directory becomes a real nested repo at a NEW sha (standing in for "the
	// submodule checkout hard-reset to the now-merged tracked-main tip").
	repoDir := filepath.Join(wt, "submodules", "sm", "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sg := git.New(repoDir)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if _, err := sg.Run(ctx, a...); err != nil {
			t.Fatalf("init synced checkout %v: %v", a, err)
		}
	}
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("merged\n"), 0o644)
	if err := sg.Commit(ctx, "merged tip"); err != nil {
		t.Fatalf("commit synced checkout: %v", err)
	}
	mergedTip, err := sg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse synced checkout HEAD: %v", err)
	}

	published := false
	c := &Claimer{
		Repo: rp, Sub: subs[0], Git: wg, TTL: time.Hour, Session: "bee-a",
		Publish: func(ctx context.Context) error {
			published = true
			return wg.PublishToMain(ctx, "")
		},
	}
	note := fmt.Sprintf("already merged into tracked main (%s) by a prior interrupted review; runner-finalized DONE (no re-review)", mergedTip)
	if err := c.FinalizeAlreadyMerged(ctx, "T1", "submodules/sm/repo", note); err != nil {
		t.Fatalf("finalize-already-merged: %v", err)
	}
	if !published {
		t.Fatal("FinalizeAlreadyMerged must publish immediately (no later finish() to rely on)")
	}

	// Read back from the ORIGINAL (main) checkout, proving the correction really
	// reached main and not just the worktree branch.
	mp, err := plan.Parse(mustRead(t, filepath.Join(sm, "PLAN.md")))
	if err != nil {
		t.Fatalf("parse main PLAN.md: %v", err)
	}
	tk := mp.Find("T1")
	if tk.Status != plan.Done {
		t.Fatalf("main status = %s, want DONE", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("finalize-already-merged must release the claim on main")
	}
	if joined := strings.Join(tk.Body, "\n"); !strings.Contains(joined, "already merged into tracked main") {
		t.Fatalf("main PLAN.md missing finalize note:\n%s", joined)
	}
	out, err := g.Run(ctx, "ls-files", "-s", "submodules/sm/repo")
	if err != nil {
		t.Fatalf("ls-files main gitlink: %v", err)
	}
	if !strings.Contains(out, mergedTip) {
		t.Fatalf("main gitlink = %q, want bumped to the merged tip %s (not left at the stale %s)", out, mergedTip, staleSHA)
	}
}

// TestFinalizeAlreadyMergedGuarded: only legal from NEEDS-REVIEW.
func TestFinalizeAlreadyMergedGuarded(t *testing.T) {
	c, ctx := setup(t)
	if err := c.FinalizeAlreadyMerged(ctx, "T1", "submodules/sm/repo", "note"); err == nil {
		t.Fatal("finalize-already-merged on TODO must error")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPublishRaceOneWinner proves the publish-to-main conflict arbitrates a true
// race: two honeybees on separate worktrees both claim the same task; the first
// to publish wins, the second's claim conflicts on the PLAN line and is reported
// as a lost race so it can reselect.
func TestPublishRaceOneWinner(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ndo it\n"), 0o644)
	g.Commit(ctx, "seed")

	mk := func(name string) *Claimer {
		wt := filepath.Join(root, ".worktrees", name)
		if err := g.WorktreeAdd(ctx, wt, name, "main"); err != nil {
			t.Fatal(err)
		}
		rp, _ := repo.Open(wt)
		subs, _ := rp.Submodules()
		wg := git.New(wt)
		return &Claimer{
			Repo: rp, Sub: subs[0], Git: wg, TTL: time.Hour, Session: name,
			Publish: func(c context.Context) error { return wg.PublishToMain(c, "") },
		}
	}
	a, b := mk("bee-a"), mk("bee-b")
	ts := time.Now().UTC()
	errA := a.Claim(ctx, "T1", ts)
	errB := b.Claim(ctx, "T1", ts)
	// Exactly one wins; the other gets ErrLost.
	won := 0
	for _, e := range []error{errA, errB} {
		switch {
		case e == nil:
			won++
		case errors.Is(e, ErrLost):
		default:
			t.Fatalf("unexpected claim error: %v", e)
		}
	}
	if won != 1 {
		t.Fatalf("want exactly one winner, got %d (a=%v b=%v)", won, errA, errB)
	}
}

// TestClaimLockSingleton proves the bootstrap/reconcile singleton lock arbitrates
// like a task claim: two passes on separate worktrees race for the same lock,
// exactly one wins, and after the winner releases, a later pass can acquire it.
func TestClaimLockSingleton(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ndo it\n"), 0o644)
	g.Commit(ctx, "seed")

	mk := func(name string) *Claimer {
		wt := filepath.Join(root, ".worktrees", name)
		if err := g.WorktreeAdd(ctx, wt, name, "main"); err != nil {
			t.Fatal(err)
		}
		rp, _ := repo.Open(wt)
		subs, _ := rp.Submodules()
		wg := git.New(wt)
		return &Claimer{
			Repo: rp, Sub: subs[0], Git: wg, TTL: time.Hour, Session: name,
			Publish: func(c context.Context) error { return wg.PublishToMain(c, "") },
		}
	}
	a, b := mk("bee-a"), mk("bee-b")
	errA := a.ClaimLock(ctx, "reconcile")
	errB := b.ClaimLock(ctx, "reconcile")
	won, winner := 0, (*Claimer)(nil)
	for i, e := range []error{errA, errB} {
		switch {
		case e == nil:
			won++
			winner = []*Claimer{a, b}[i]
		case errors.Is(e, ErrLost):
		default:
			t.Fatalf("unexpected ClaimLock error: %v", e)
		}
	}
	if won != 1 {
		t.Fatalf("want exactly one lock winner, got %d (a=%v b=%v)", won, errA, errB)
	}
	// A fresh pass cannot take the live lock.
	c := mk("bee-c")
	if err := c.ClaimLock(ctx, "reconcile"); !errors.Is(err, ErrLost) {
		t.Fatalf("want ErrLost against a live lock, got %v", err)
	}
	// After the winner releases, a later pass can acquire it.
	if err := winner.ReleaseLock(ctx, "reconcile"); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	d := mk("bee-d")
	if err := d.ClaimLock(ctx, "reconcile"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}
