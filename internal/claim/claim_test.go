package claim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
