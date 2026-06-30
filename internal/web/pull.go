package web

import (
	"context"
	"sync"
	"time"
)

// DefaultPullInterval is how often the viewer fast-forwards the beehive repo from
// the remote when one is configured. It trails the producer's ~1s session-commit
// cadence closely enough to follow an off-box run near "live" without hammering
// the remote on every 2s pane poll.
const DefaultPullInterval = 5 * time.Second

// pullState records the most recent fast-forward pull the viewer performed: when
// it last succeeded and the error (if any) from the latest attempt. A beehived
// following an off-box honeybee renders the session pane from main in its working
// tree, so this is the freshness of that copy. mu guards the fields — the puller
// goroutine writes, pane handlers read.
type pullState struct {
	mu      sync.Mutex
	last    time.Time // last successful ff pull (or "already up to date")
	lastErr error     // error from the most recent attempt; nil when in sync
	ran     bool      // at least one attempt has completed
}

// clock returns the staleness clock (time.Now unless a test injected one).
func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// pullRemote fast-forwards the beehive repo's main to the remote so the working
// tree the session pane reads reflects commits other hosts pushed: new session
// stubs, finalized transcripts merged to main, and plan/doc updates. It is gated
// on the repo having a remote — a single-host install has no off-box runs to
// follow and its honeybees already publish to local main on the shared
// filesystem, so there is nothing to pull. A divergent local main makes the
// --ff-only pull fail; that error is recorded and surfaced while the local copy
// is kept untouched (NO merge commit), matching the swarm's fast-forward-or-lose
// convergence.
func (s *Server) pullRemote(ctx context.Context) error {
	remote, err := s.git.Remote(ctx)
	if err != nil {
		s.recordPull(err)
		return err
	}
	if remote == "" {
		return nil // single-host: nothing off-box to follow
	}
	perr := s.git.Pull(ctx, remote, "main")
	s.recordPull(perr)
	return perr
}

// recordPull stores the outcome of one pull attempt. A success advances the
// last-pulled time (the copy is confirmed in sync); a failure leaves it where it
// was (the copy is that old) but records the error so the pane can flag it.
func (s *Server) recordPull(err error) {
	s.pull.mu.Lock()
	defer s.pull.mu.Unlock()
	s.pull.ran = true
	s.pull.lastErr = err
	if err == nil {
		s.pull.last = s.clock()
	}
}

// StartPuller runs pullRemote every interval until ctx is canceled, priming once
// immediately so the first pane view is current. The goroutine runs even on a
// no-remote install (where pullRemote is a cheap no-op) so adding a remote later
// is picked up without a restart. It returns immediately; the daemon owns ctx's
// lifetime.
func (s *Server) StartPuller(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultPullInterval
	}
	go func() {
		_ = s.pullRemote(ctx) // prime so the first view isn't stale
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = s.pullRemote(ctx)
			}
		}
	}()
}

// pullStatus is the session pane's freshness snapshot.
type pullStatus struct {
	Remote bool   // the repo has a remote: the viewer is following off-box runs
	Ran    bool   // at least one pull attempt has completed
	Ago    string // human time since the last successful pull ("" if never)
	Err    string // last attempt's error (e.g. divergent ff-only), "" when clean
}

// pullStatusAt projects the current pull state for rendering at time now. remote
// says whether pulling is active for this install (computed once by the caller).
func (s *Server) pullStatusAt(now time.Time, remote bool) pullStatus {
	s.pull.mu.Lock()
	defer s.pull.mu.Unlock()
	ps := pullStatus{Remote: remote, Ran: s.pull.ran}
	if !s.pull.last.IsZero() {
		ps.Ago = humanAgo(now.Sub(s.pull.last))
	}
	if s.pull.lastErr != nil {
		ps.Err = s.pull.lastErr.Error()
	}
	return ps
}
