package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Deterministic git maintenance: auto-gc storm prevention + runner-owned gc.
//
// On 2026-07-08 the shared hive object store corrupted because git's *automatic*
// gc/geometric-repack fired concurrently across the many honeybee passes + beehived
// that all share one checkout: passes killed at their turn timeout died mid-repack,
// and a prune-before-pack window dropped main's tip commit object. No beehive code
// called gc — stock git raced itself on a shared filesystem.
//
// The runner therefore owns maintenance deterministically, every pass:
//   - DisableAutoGC turns stock auto-gc/maintenance OFF on every repo it touches, so
//     git never fires an unsupervised concurrent repack again.
//   - MaybeGC runs a SINGLE `git gc` itself, interval-gated and serialized across
//     concurrent honeybees. All coordination state lives in ad-hoc, UNCOMMITTED local
//     git config (beehive.gc.*), which is shared across every worktree of a repo — so
//     passes converge on one gc with no committed lock, no external timer, no process.
const (
	// DefaultGCInterval is the minimum wall-clock time between deterministic gc runs
	// on a repo. Worktree churn from every pass leaves loose objects; gc'ing a few
	// times a day keeps .git compact without ever running two gcs at once. Override
	// per repo with `git config beehive.gc.interval <duration>` (uncommitted, local).
	DefaultGCInterval = 6 * time.Hour

	// DefaultGCLockTTL bounds a held gc lock so a honeybee that died mid-gc cannot
	// wedge maintenance forever: a lock older than this is stale and gets stolen. It
	// is generous relative to how long `git gc` takes on a hive-sized repo, and git's
	// own gc.pid lock is the hard backstop against true concurrency regardless.
	DefaultGCLockTTL = 30 * time.Minute

	cfgGCAuto       = "gc.auto"
	cfgMaintAuto    = "maintenance.auto"
	cfgWriteCommitG = "fetch.writeCommitGraph"
	cfgGCInterval   = "beehive.gc.interval"
	cfgGCLastRun    = "beehive.gc.lastrun"
	cfgGCLockedBy   = "beehive.gc.lockedby"
	cfgGCLockedAt   = "beehive.gc.lockedat"
)

// GCConfig tunes deterministic maintenance. Zero values fall back to the package
// defaults (themselves overridable per repo via `git config beehive.gc.interval`).
type GCConfig struct {
	Interval time.Duration // min wall-clock between gc runs; <=0 => DefaultGCInterval
	LockTTL  time.Duration // stale-lock TTL; <=0 => DefaultGCLockTTL
}

// configGetLocal returns the repo-local config value for key, or "" if unset.
func (r *Repo) configGetLocal(ctx context.Context, key string) string {
	out, err := r.Run(ctx, "config", "--local", "--get", key)
	if err != nil {
		return "" // unset (exit 1) or no config
	}
	return strings.TrimSpace(out)
}

func (r *Repo) configSetLocal(ctx context.Context, key, val string) error {
	_, err := r.Run(ctx, "config", "--local", key, val)
	return err
}

func (r *Repo) configUnsetLocal(ctx context.Context, key string) {
	_, _ = r.Run(ctx, "config", "--local", "--unset", key)
}

// DisableAutoGC sets the local (uncommitted) config that stops stock git from ever
// auto-gc'ing or auto-maintaining this repo, so concurrent passes can never trigger
// the racing-repack storm that corrupted the object store. Idempotent; only writes a
// key when it is not already at the wanted value, so it is cheap to call every pass.
func (r *Repo) DisableAutoGC(ctx context.Context) error {
	want := [...][2]string{
		{cfgGCAuto, "0"},
		{cfgMaintAuto, "false"},
		{cfgWriteCommitG, "false"},
	}
	for _, kv := range want {
		if r.configGetLocal(ctx, kv[0]) == kv[1] {
			continue
		}
		if err := r.configSetLocal(ctx, kv[0], kv[1]); err != nil {
			return fmt.Errorf("git: disable auto-gc (%s): %w", kv[0], err)
		}
	}
	return nil
}

// MaybeGC runs a single `git gc` on this repo iff (a) at least the configured interval
// has elapsed since the last recorded run AND (b) no other honeybee currently holds a
// fresh gc lock. All coordination lives in ad-hoc, uncommitted local git config
// (beehive.gc.*), shared across every worktree of the repo, so concurrent honeybees
// serialize with no committed lock and no external process. It returns ran=true only
// when it actually invoked gc. It is best-effort: a returned error is for logging, not
// a reason to fail the pass.
//
// sessionID attributes the lock (so a stale lock is traceable); cfg supplies the
// interval / lock TTL.
func (r *Repo) MaybeGC(ctx context.Context, sessionID string, cfg GCConfig) (ran bool, err error) {
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultGCInterval
	}
	// Per-repo override, seeded on first sight so the knob is discoverable/editable in
	// the repo's .git/config (uncommitted, local).
	if v := r.configGetLocal(ctx, cfgGCInterval); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil {
			interval = d
		}
	} else {
		_ = r.configSetLocal(ctx, cfgGCInterval, interval.String())
	}
	ttl := cfg.LockTTL
	if ttl <= 0 {
		ttl = DefaultGCLockTTL
	}

	now := time.Now()

	// (a) interval gate — lastrun is shared across all honeybees, so gc runs at most
	// once per interval swarm-wide, not once per pass.
	if last := parseUnixTime(r.configGetLocal(ctx, cfgGCLastRun)); !last.IsZero() && now.Sub(last) < interval {
		return false, nil
	}

	// (b) lock gate — another honeybee holding a fresh lock is mid-gc; defer to it.
	if by := r.configGetLocal(ctx, cfgGCLockedBy); by != "" && by != sessionID {
		if at := parseUnixTime(r.configGetLocal(ctx, cfgGCLockedAt)); !at.IsZero() && now.Sub(at) < ttl {
			return false, nil
		}
		// else: lock is stale (holder crashed) — steal it below.
	}

	// Take the lock. `git config` writes are serialized by .git/config.lock, so the
	// only residual race is two passes both reading "due" in the same instant; that is
	// harmless because git gc's own gc.pid lock refuses a second concurrent gc — worst
	// case one pass's gc no-ops, never corruption.
	if err := r.configSetLocal(ctx, cfgGCLockedBy, sessionID); err != nil {
		return false, err
	}
	if err := r.configSetLocal(ctx, cfgGCLockedAt, strconv.FormatInt(now.Unix(), 10)); err != nil {
		r.configUnsetLocal(ctx, cfgGCLockedBy)
		return false, err
	}
	defer func() {
		r.configUnsetLocal(ctx, cfgGCLockedBy)
		r.configUnsetLocal(ctx, cfgGCLockedAt)
	}()

	// Single gc, default prune expiry (2 weeks) — never removes objects a concurrent
	// pass just created. --quiet keeps the pass log clean.
	if _, gerr := r.Run(ctx, "gc", "--quiet"); gerr != nil {
		return false, fmt.Errorf("git: gc: %w", gerr)
	}
	if serr := r.configSetLocal(ctx, cfgGCLastRun, strconv.FormatInt(time.Now().Unix(), 10)); serr != nil {
		return true, serr
	}
	return true, nil
}

// MaintainRepos runs the full deterministic maintenance — DisableAutoGC then MaybeGC —
// on this repo and every declared submodule checkout under it (submodules/<name>/repo),
// each with its own uncommitted beehive.gc.* coordination state. Best-effort: it always
// attempts every repo and returns the first error encountered, for the caller to log.
func (r *Repo) MaintainRepos(ctx context.Context, sessionID string, cfg GCConfig) error {
	repos := []*Repo{r}
	if paths, _ := r.declaredSubmodulePaths(ctx); len(paths) > 0 {
		for _, p := range paths {
			repos = append(repos, &Repo{Dir: filepath.Join(r.Dir, p)})
		}
	}
	var firstErr error
	keep := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	for _, rp := range repos {
		keep(rp.DisableAutoGC(ctx))
		if _, e := rp.MaybeGC(ctx, sessionID, cfg); e != nil {
			keep(e)
		}
	}
	return firstErr
}

// parseUnixTime parses a unix-seconds string; zero Time on any parse failure/empty.
func parseUnixTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
