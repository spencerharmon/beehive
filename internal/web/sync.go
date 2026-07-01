package web

import (
	"context"
	"sync"
	"time"
)

// Following off-box honeybee runs.
//
// A honeybee on another host streams its session transcript to an isolated git
// branch and pushes it, and plants a STUB naming that branch at the session path
// on the beehive repo's main. The per-session read path (readSessionBranch) keeps
// the OPEN transcript fresh by fetching that branch on demand, but two things only
// land on main: the STUB (so the session shows up in the list at all) and the
// finalized transcript (merged once at session end). A beehived that never pulled
// main would therefore never see an off-box session appear or finish.
//
// SyncRemote is that missing follower pull: it periodically fast-forwards local
// main to the remote so off-box stubs and finalized transcripts materialize in the
// local checkout the file-derived views read. It is a FOLLOWER — it never merges
// or resets — so on divergence (beehived's own unpushed commits, or a non-ff
// advance) it degrades to a plain fetch (advancing the remote-tracking refs the
// per-branch reads use) and surfaces the divergence as staleness rather than
// reconciling locally.
const (
	// syncInterval is how often the daemon polls the remote for main.
	syncInterval = 5 * time.Second
	// syncStaleAfter is how long since the last successful pull before the UI
	// flags the follower as stale (independent of any pull error).
	syncStaleAfter = 30 * time.Second
)

// remoteSync records the outcome of the periodic follower pull for the UI. It is
// mutated by the sync loop and read by request handlers, so every field access is
// guarded by mu.
type remoteSync struct {
	mu         sync.Mutex
	hasRemote  bool      // the repo has a push remote (single-host repos have none)
	everSynced bool      // a fast-forward pull has succeeded at least once
	lastSynced time.Time // wall-clock time of the last successful pull
	lastErr    error     // last pull error ("" once a later pull succeeds)
	fetched    bool      // the failed pull's fallback fetch still reached the remote
}

func (rs *remoteSync) recordNoRemote() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.hasRemote = false
}

func (rs *remoteSync) recordSynced(now time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.hasRemote = true
	rs.everSynced = true
	rs.lastSynced = now
	rs.lastErr = nil
	rs.fetched = false
}

// recordErr records a failed follower pull. fetched reports whether the fallback
// fetch still reached the remote (divergence: refs advanced, we just can't
// fast-forward) versus not (the remote is unreachable).
func (rs *remoteSync) recordErr(err error, fetched bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.hasRemote = true
	rs.lastErr = err
	rs.fetched = fetched
}

// syncStatus is the template projection of the follower state.
type syncStatus struct {
	Remote bool   // repo has a remote (single-host repos render nothing)
	Synced bool   // a pull has ever succeeded (Ago is meaningful)
	Ago    string // human "3s"/"5m" since the last successful pull
	Stale  bool   // the follower is behind (age past threshold, or a pull error)
	Note   string // human explanation when stale ("" when fresh)
}

// view projects the current follower state for rendering at now. staleAfter is the
// age past which a successful-but-old sync is flagged stale.
func (rs *remoteSync) view(now time.Time, staleAfter time.Duration) syncStatus {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if !rs.hasRemote {
		return syncStatus{Remote: false}
	}
	st := syncStatus{Remote: true, Synced: rs.everSynced}
	if rs.everSynced {
		st.Ago = humanAgo(now.Sub(rs.lastSynced))
	}
	switch {
	case !rs.everSynced:
		st.Stale = true
		st.Note = "not yet synced with remote"
	case rs.lastErr != nil && rs.fetched:
		// Reached the remote but local main can't fast-forward: it diverged. We do
		// not merge; per-branch reads still see the fetched refs, so the transcript
		// is current even though the list/finalized view is pinned to last sync.
		st.Stale = true
		st.Note = "local main diverged from remote; showing last fast-forwarded state"
	case rs.lastErr != nil:
		st.Stale = true
		st.Note = "remote unreachable; showing last synced state"
	case now.Sub(rs.lastSynced) > staleAfter:
		st.Stale = true
		st.Note = "sync is overdue"
	}
	return st
}

// SyncRemote fast-forwards the beehive repo's local main to the remote once, so
// off-box session stubs and finalized transcripts land in the local checkout the
// views read. A repo with no remote is single-host and needs no follow. On a
// non-fast-forward (local main diverged) or an outage it never merges or resets:
// it falls back to a plain fetch (so the remote-tracking refs the per-branch
// session reads use still advance) and records the divergence/outage as staleness.
func (s *Server) SyncRemote(ctx context.Context) {
	rem, err := s.git.Remote(ctx)
	if err != nil || rem == "" {
		s.sync.recordNoRemote()
		return
	}
	if perr := s.git.Pull(ctx, rem, "main"); perr != nil {
		// `git pull --ff-only` already fetched before refusing the non-ff merge, so
		// the tracking refs are current; a bare fetch both confirms reachability and
		// covers a pull that aborted before fetching. We stay a follower: no merge.
		ferr := s.git.Fetch(ctx, rem, "main")
		s.sync.recordErr(perr, ferr == nil)
		return
	}
	s.sync.recordSynced(time.Now())
}

// SyncLoop runs SyncRemote immediately, then every s.syncEvery until ctx is
// cancelled. The daemon starts it once at boot so the file-derived views follow
// off-box runs without any per-request remote I/O.
func (s *Server) SyncLoop(ctx context.Context) {
	s.SyncRemote(ctx)
	t := time.NewTicker(s.syncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SyncRemote(ctx)
		}
	}
}
