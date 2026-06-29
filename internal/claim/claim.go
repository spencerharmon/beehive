// Package claim implements the commit-race claim protocol: mark a PLAN.md task
// IN-PROGRESS with a timestamp, commit to beehive main, then re-pull and assert
// our own timestamp won. Merge is not a lock; the re-verify is. Also: heartbeat
// re-stamp each turn, stale/TTL detection for GC, and the rejection counter.
package claim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// ErrLost is returned when re-verify finds another bee's stamp won the race.
var ErrLost = errors.New("claim: lost commit race")

// Claimer races to own a task in a submodule PLAN.md.
type Claimer struct {
	Repo *repo.Repo
	Sub  repo.Submodule
	Git  *git.Repo // beehive repo root
	TTL  time.Duration
	Now  func() time.Time // injectable clock; defaults to time.Now
}

func (c *Claimer) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Claimer) load() (*plan.Plan, error) {
	b, err := os.ReadFile(c.Sub.PlanPath())
	if err != nil {
		return nil, err
	}
	return plan.Parse(string(b))
}

func (c *Claimer) save(p *plan.Plan) error {
	return os.WriteFile(c.Sub.PlanPath(), []byte(p.String()), 0o644)
}

// stampMsg keeps the commit message linkable for the frontend.
func stampMsg(taskID, action string) string {
	return fmt.Sprintf("plan: %s %s\n\nBeehive: %s plan", action, taskID, taskID)
}

// Claim marks taskID IN-PROGRESS with ts, commits to main, then re-verifies our
// stamp won. ErrLost means abandon and reselect. ts must be unique to this bee.
func (c *Claimer) Claim(ctx context.Context, taskID string, ts time.Time) error {
	ts = ts.UTC().Truncate(time.Second)
	p, err := c.load()
	if err != nil {
		return err
	}
	t := p.Find(taskID)
	if t == nil {
		return fmt.Errorf("claim: task %q absent", taskID)
	}
	t.Status = plan.InProgress
	t.Heartbeat = ts
	if err := c.save(p); err != nil {
		return err
	}
	if err := c.Git.Commit(ctx, stampMsg(taskID, "claim")); err != nil {
		return err
	}
	return c.verify(taskID, ts)
}

// verify re-reads PLAN.md (post-pull caller) and asserts our timestamp owns it.
func (c *Claimer) verify(taskID string, ts time.Time) error {
	p, err := c.load()
	if err != nil {
		return err
	}
	t := p.Find(taskID)
	if t == nil {
		return ErrLost
	}
	if t.Status != plan.InProgress || !t.Heartbeat.Equal(ts.UTC().Truncate(time.Second)) {
		return ErrLost
	}
	return nil
}

// Heartbeat re-stamps taskID to keep it from going stale, committing the bump.
func (c *Claimer) Heartbeat(ctx context.Context, taskID string, ts time.Time) error {
	ts = ts.UTC().Truncate(time.Second)
	p, err := c.load()
	if err != nil {
		return err
	}
	t := p.Find(taskID)
	if t == nil {
		return fmt.Errorf("heartbeat: task %q absent", taskID)
	}
	if t.Status != plan.InProgress {
		return fmt.Errorf("heartbeat: task %q not in progress", taskID)
	}
	t.Heartbeat = ts
	if err := c.save(p); err != nil {
		return err
	}
	if err := c.Git.Commit(ctx, stampMsg(taskID, "heartbeat")); err != nil && err != git.ErrNothing {
		return err
	}
	return c.verify(taskID, ts)
}

// Stale reports whether taskID's heartbeat exceeded the TTL (a GC candidate).
func (c *Claimer) Stale(taskID string) (bool, error) {
	p, err := c.load()
	if err != nil {
		return false, err
	}
	t := p.Find(taskID)
	if t == nil {
		return false, fmt.Errorf("stale: task %q absent", taskID)
	}
	return t.Stale(c.now(), c.TTL), nil
}

// Reject increments the rejection counter and, past limit, sets NEEDS-HUMAN to
// break review/arbitration livelock; otherwise resets to TODO. Commits the change.
func (c *Claimer) Reject(ctx context.Context, taskID string, limit int) (plan.Status, error) {
	p, err := c.load()
	if err != nil {
		return "", err
	}
	t := p.Find(taskID)
	if t == nil {
		return "", fmt.Errorf("reject: task %q absent", taskID)
	}
	t.Attempts++
	t.Heartbeat = time.Time{}
	if t.Attempts > limit {
		t.Status = plan.NeedsHuman
	} else {
		t.Status = plan.TODO
	}
	if err := c.save(p); err != nil {
		return "", err
	}
	if err := c.Git.Commit(ctx, stampMsg(taskID, "reject")); err != nil && err != git.ErrNothing {
		return "", err
	}
	return t.Status, nil
}
