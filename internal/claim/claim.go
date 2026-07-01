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
	"path/filepath"
	"strconv"
	"strings"
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
	if err := c.Git.CommitPaths(ctx, stampMsg(taskID, "claim"), c.Sub.PlanRel()); err != nil && err != git.ErrNothing {
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
	if err := c.Git.CommitPaths(ctx, stampMsg(taskID, "heartbeat"), c.Sub.PlanRel()); err != nil && err != git.ErrNothing {
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
	if err := c.Git.CommitPaths(ctx, stampMsg(taskID, "release"), c.Sub.PlanRel()); err != nil && err != git.ErrNothing {
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
	if err := c.Git.CommitPaths(ctx, stampMsg(taskID, "reject"), c.Sub.PlanRel()); err != nil && err != git.ErrNothing {
		return "", err
	}
	return t.Status, nil
}

// --- Singleton locks (bootstrap / reconcile) -------------------------------
//
// Bootstrap and ROI-reconcile operate on PLAN.md as a whole and carry no task
// to claim, so without a lock every honeybee that sees the same drift runs the
// SAME reconcile in parallel and they race to merge — wasted sessions and merge
// thrash. A singleton lock makes exactly one honeybee perform the operation:
// it's the same commit-race as a task claim, but on a dedicated lock file
// (submodules/<sm>/.bee-lock-<name>) instead of a PLAN task line. The lock
// carries a heartbeat ts and is TTL-bounded, so a crashed holder's lock is
// reclaimable by a later honeybee exactly like a stale task claim.

func (c *Claimer) lockRel(name string) string {
	return filepath.Join("submodules", c.Sub.Name, ".bee-lock-"+name)
}

func (c *Claimer) lockPath(name string) string {
	return filepath.Join(c.Sub.Path, ".bee-lock-"+name)
}

// readLock parses a lock file's "session\nunix-ts" body. ok is false when the
// file is absent or malformed (treated as unlocked).
func readLock(path string) (session string, ts int64, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, false
	}
	lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	if len(lines) < 2 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(lines[0]), n, true
}

func lockActive(ts int64, now time.Time, ttl time.Duration) bool {
	return ts != 0 && now.Sub(time.Unix(ts, 0)) < ttl
}

// ClaimLock acquires the named singleton lock for this session. A live foreign
// holder (lock fresh within TTL, different session) or a publish/merge conflict
// is ErrLost — the caller reselects. On success this session owns the operation
// on main and ReleaseLock must be called when done.
func (c *Claimer) ClaimLock(ctx context.Context, name string) error {
	rel, path := c.lockRel(name), c.lockPath(name)
	if sess, ts, ok := readLock(path); ok && sess != c.Session && lockActive(ts, c.now(), c.TTL) {
		return ErrLost
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("%s\n%d\n", c.Session, c.now().Unix())
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	if err := c.Git.CommitPaths(ctx, stampMsg(name, "lock"), rel); err != nil && err != git.ErrNothing {
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
	// Verify: pull main and assert our session still holds the lock (a peer that
	// raced may have landed first; their lock content conflicts with ours).
	if err := c.syncMain(ctx); err != nil {
		if errors.Is(err, git.ErrConflict) {
			return ErrLost
		}
		return err
	}
	if sess, _, ok := readLock(path); !ok || sess != c.Session {
		return ErrLost
	}
	return nil
}

// ReleaseLock clears the named lock if this session holds it, then commits and
// publishes the removal so a peer can take over immediately. A publish conflict
// is benign (a peer already advanced main) and is swallowed.
func (c *Claimer) ReleaseLock(ctx context.Context, name string) error {
	rel, path := c.lockRel(name), c.lockPath(name)
	if sess, _, ok := readLock(path); !ok || sess != c.Session {
		return nil // not ours / already gone
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := c.Git.CommitPaths(ctx, stampMsg(name, "unlock"), rel); err != nil && err != git.ErrNothing {
		return err
	}
	if c.Publish != nil {
		if err := c.Publish(ctx); err != nil && !errors.Is(err, git.ErrConflict) {
			return err
		}
	}
	return nil
}
