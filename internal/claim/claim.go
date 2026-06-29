// Package claim implements the unified commit-race claim protocol. A honeybee
// stamps a PLAN.md task with its unique session id + a heartbeat timestamp,
// commits, and publishes to beehive main. Publishing is the race: a competing
// claim on the same task lands first and the loser's publish conflicts, so the
// loser learns it lost (ErrLost) and reselects instead of wasting a session.
//
// The same mechanism covers EVERY task status (TODO, NEEDS-REVIEW,
// NEEDS-ARBITRATION): "in progress" is not a status but a derived property
// (session set + heartbeat fresh within TTL). Heartbeat re-stamps each turn and
// re-verifies ownership; stale claims are GC-reclaimable by overwrite.
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

// ErrLost means another session won the task: abandon and reselect.
var ErrLost = errors.New("claim: lost commit race")

// ErrResolved means the task left the status this run was working: it reached a
// terminal/handoff state (e.g. a worked TODO became NEEDS-REVIEW, or a review
// became DONE) during the previous turn. NOT a failure — the runner re-checks
// completion and exits cleanly.
var ErrResolved = errors.New("claim: task resolved out of its working status")

// Claimer races to own a task in a submodule PLAN.md using this process's
// Session token. Publish merges the worktree branch to main (nil = local-only,
// no cross-process race); a publish conflict is read as a lost race.
type Claimer struct {
	Repo    *repo.Repo
	Sub     repo.Submodule
	Git     *git.Repo // beehive worktree root
	TTL     time.Duration
	Session string
	Now     func() time.Time
	Publish func(ctx context.Context) error
	Remote  string
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

// syncMain pulls beehive main into the worktree so we observe competing claims
// and resolutions. A merge conflict means a competing edit to our task line:
// surfaced as ErrLost by callers.
func (c *Claimer) syncMain(ctx context.Context) error {
	ref := "main"
	if c.Remote != "" {
		if err := c.Git.Fetch(ctx, c.Remote, "main"); err != nil {
			return err
		}
		ref = c.Remote + "/main"
	}
	return c.Git.Merge(ctx, ref)
}

// Claim stamps taskID with our session + ts (no status change), commits, and
// publishes. A publish/merge conflict, or another live session owning the task,
// is a lost race (ErrLost). On success our session owns the task on main.
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
	if t.Active(c.now(), c.TTL) && t.Session != c.Session {
		return ErrLost // a live bee already holds it
	}
	t.Claim(c.Session, ts)
	if err := c.save(p); err != nil {
		return err
	}
	if err := c.Git.Commit(ctx, stampMsg(taskID, "claim")); err != nil && err != git.ErrNothing {
		return err
	}
	if c.Publish != nil {
		if err := c.Publish(ctx); err != nil {
			if errors.Is(err, git.ErrConflict) {
				return ErrLost
			}
			return err
		}
	}
	return c.verify(ctx, taskID)
}

// verify pulls main and asserts our session still owns the task.
func (c *Claimer) verify(ctx context.Context, taskID string) error {
	if err := c.syncMain(ctx); err != nil {
		if errors.Is(err, git.ErrConflict) {
			return ErrLost
		}
		return err
	}
	p, err := c.load()
	if err != nil {
		return err
	}
	t := p.Find(taskID)
	if t == nil || t.Session != c.Session {
		return ErrLost
	}
	return nil
}

// Heartbeat re-stamps taskID for this turn. It first pulls main (to observe a
// competitor or a resolution), then: if the task left `from` status it is
// ErrResolved; if another live session owns it it is ErrLost; otherwise we
// re-stamp, commit, and publish (a publish conflict is ErrLost).
func (c *Claimer) Heartbeat(ctx context.Context, taskID string, from plan.Status, ts time.Time) error {
	ts = ts.UTC().Truncate(time.Second)
	if err := c.syncMain(ctx); err != nil {
		if errors.Is(err, git.ErrConflict) {
			return ErrLost
		}
		return err
	}
	p, err := c.load()
	if err != nil {
		return err
	}
	t := p.Find(taskID)
	if t == nil {
		return fmt.Errorf("heartbeat: task %q absent", taskID)
	}
	if t.Status != from {
		return fmt.Errorf("%w: %q is %s", ErrResolved, taskID, t.Status)
	}
	if t.Session != "" && t.Session != c.Session && t.Active(c.now(), c.TTL) {
		return ErrLost // another live bee took it while we were away
	}
	t.Claim(c.Session, ts)
	if err := c.save(p); err != nil {
		return err
	}
	if err := c.Git.Commit(ctx, stampMsg(taskID, "heartbeat")); err != nil && err != git.ErrNothing {
		return err
	}
	if c.Publish != nil {
		if err := c.Publish(ctx); err != nil {
			if errors.Is(err, git.ErrConflict) {
				return ErrLost
			}
			return err
		}
	}
	return nil
}

// Release clears the active claim (session + heartbeat) on taskID without
// changing its status, then commits and publishes. Called on completion so a
// task the agent moved to its next phase (e.g. NEEDS-REVIEW) shows as unclaimed
// and a peer can pick it up immediately instead of waiting out the TTL. A publish
// conflict here is benign (a peer already advanced main) and is swallowed.
func (c *Claimer) Release(ctx context.Context, taskID string) error {
	p, err := c.load()
	if err != nil {
		return err
	}
	t := p.Find(taskID)
	if t == nil || (t.Session == "" && t.Heartbeat.IsZero()) {
		return nil
	}
	t.Release()
	if err := c.save(p); err != nil {
		return err
	}
	if err := c.Git.Commit(ctx, stampMsg(taskID, "release")); err != nil && err != git.ErrNothing {
		return err
	}
	if c.Publish != nil {
		if err := c.Publish(ctx); err != nil && !errors.Is(err, git.ErrConflict) {
			return err
		}
	}
	return nil
}

// Stale reports whether taskID holds an expired claim (a GC candidate).
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
	if err := t.Reject(limit, c.now()); err != nil {
		return "", err
	}
	if err := c.save(p); err != nil {
		return "", err
	}
	if err := c.Git.Commit(ctx, stampMsg(taskID, "reject")); err != nil && err != git.ErrNothing {
		return "", err
	}
	return t.Status, nil
}
