package claim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
)

func TestHeartbeatCompletedCycleDoesNotCommitOrPublish(t *testing.T) {
	c, ctx := setup(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c.Now = func() time.Time { return now }
	writePlan(t, c, ctx, "## T1 [TODO] <!-- attempts=0 deps= recurrence=24h -->\nCheck: go test ./...\n")
	if err := c.Claim(ctx, "T1", now); err != nil {
		t.Fatal(err)
	}
	published := 0
	c.Publish = func(context.Context) error { published++; return nil }
	// A check still running must retain its ordinary keepalive.
	if err := c.Heartbeat(ctx, "T1", plan.TODO, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	p, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	task := p.Find("T1")
	if task.Session != c.Session || !task.Heartbeat.Equal(now.Add(time.Minute)) || published != 1 {
		t.Fatalf("active cycle lost keepalive: task=%+v publishes=%d", task, published)
	}
	if err := task.CompleteCycle(c.Session, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	task.Commits, task.CommitsSet = nil, true
	docRel := filepath.Join("submodules", c.Sub.Name, "docs", "bee-T1-T1.md")
	docPath := filepath.Join(c.Repo.Root, docRel)
	if err := os.MkdirAll(filepath.Dir(docPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const evidence = "<!-- Beehive-Commits: none -->\n\nSuccessful cycle 1\n"
	if err := os.WriteFile(docPath, []byte(evidence), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.save(p); err != nil {
		t.Fatal(err)
	}
	wantPlan := mustRead(t, c.Sub.PlanPath())
	published = 0
	for _, committed := range []bool{false, true} {
		if committed {
			if err := c.Git.CommitPaths(ctx, "record cycle", c.planRel(), docRel); err != nil {
				t.Fatal(err)
			}
		}
		head, err := c.Git.Run(ctx, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		// Include ticks after the next cycle is due: this session must never
		// reclaim its completed cycle, even if publication takes a whole day.
		for _, elapsed := range []time.Duration{3 * time.Minute, 4 * time.Minute, 25 * time.Hour} {
			now = task.LastCycle.Add(elapsed)
			if err := c.Heartbeat(ctx, "T1", plan.TODO, now); err != nil {
				t.Fatalf("committed=%v: heartbeat must defer completion validation, got %v", committed, err)
			}
			if got := mustRead(t, c.Sub.PlanPath()); got != wantPlan {
				t.Fatalf("committed=%v: heartbeat overwrote completed cycle:\n%s", committed, got)
			}
			if got := mustRead(t, docPath); got != evidence {
				t.Fatalf("heartbeat changed evidence: %q", got)
			}
			if got, err := c.Git.Run(ctx, "rev-parse", "HEAD"); err != nil || got != head {
				t.Fatalf("committed=%v: heartbeat moved HEAD: %q -> %q (err=%v)", committed, head, got, err)
			}
			if published != 0 {
				t.Fatalf("committed=%v: heartbeat published cycle evidence", committed)
			}
		}
	}
}

func TestClaimRecurringNotBefore(t *testing.T) {
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		wantErr error
	}{
		{"future", 24*time.Hour - time.Second, ErrLost},
		{"due", 24 * time.Hour, nil},
		{"past", 25 * time.Hour, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ctx := setup(t)
			last := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			now := last.Add(tc.elapsed)
			c.Now = func() time.Time { return now }
			writePlan(t, c, ctx, "## T1 [TODO] <!-- attempts=0 deps= recurrence=24h cycles=1 last_cycle="+last.Format(time.RFC3339)+" cycle_session=previous not_before="+last.Add(24*time.Hour).Format(time.RFC3339)+" -->\nCheck: go test ./...\n")
			before := mustRead(t, c.Sub.PlanPath())
			head, err := c.Git.Run(ctx, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			published := 0
			c.Publish = func(context.Context) error { published++; return nil }
			if err := c.Claim(ctx, "T1", now); !errors.Is(err, tc.wantErr) {
				t.Fatalf("claim: got %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if got := mustRead(t, c.Sub.PlanPath()); got != before {
					t.Fatalf("refused claim changed plan:\n%s", got)
				}
				if got, err := c.Git.Run(ctx, "rev-parse", "HEAD"); err != nil || got != head || published != 0 {
					t.Fatalf("refused claim committed/published: HEAD=%q, publishes=%d, err=%v", got, published, err)
				}
				return
			}
			// Previous sessions' cycle receipts must not suppress this cycle's
			// heartbeat after its scheduling gate opens.
			if err := c.Heartbeat(ctx, "T1", plan.TODO, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			p, err := c.load()
			if err != nil {
				t.Fatal(err)
			}
			task := p.Find("T1")
			if task.Session != c.Session || !task.Heartbeat.Equal(now.Add(time.Minute)) || task.CycleSession != "previous" || published != 2 {
				t.Fatalf("next cycle did not claim/heartbeat normally: task=%+v publishes=%d", task, published)
			}
		})
	}
}
