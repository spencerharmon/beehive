package claim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
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
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("- T1 | TODO | do it\n"), 0o644)
	g.Commit(ctx, "seed")
	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	return &Claimer{Repo: rp, Sub: subs[0], Git: g, TTL: time.Hour}, ctx
}

func TestClaimVerifyHeartbeat(t *testing.T) {
	c, ctx := setup(t)
	ts := time.Now().UTC()
	if err := c.Claim(ctx, "T1", ts); err != nil {
		t.Fatalf("claim: %v", err)
	}
	stale, _ := c.Stale("T1")
	if stale {
		t.Fatal("fresh claim stale")
	}
	if err := c.Heartbeat(ctx, "T1", ts.Add(time.Minute)); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
}

func TestClaimLost(t *testing.T) {
	c, ctx := setup(t)
	c.Claim(ctx, "T1", time.Now().UTC())
	// Another bee overwrites with a different ts -> our verify fails.
	if err := c.verify("T1", time.Now().Add(time.Hour)); err != ErrLost {
		t.Fatalf("want ErrLost, got %v", err)
	}
}

func TestRejectCounter(t *testing.T) {
	c, ctx := setup(t)
	c.Claim(ctx, "T1", time.Now().UTC())
	for i := 0; i < 3; i++ {
		if s, _ := c.Reject(ctx, "T1", 3); s != "TODO" {
			t.Fatalf("attempt %d status %s", i, s)
		}
		c.Claim(ctx, "T1", time.Now().UTC())
	}
	if s, _ := c.Reject(ctx, "T1", 3); s != "NEEDS-HUMAN" {
		t.Fatalf("over limit status %s", s)
	}
}
