package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/repo"
)

// sessionInfo describes one recorded session for the list view.
type sessionInfo struct {
	ID       string // file stem, e.g. bee-T3-1751210912
	Modified time.Time
	Ago      string // human "3s"/"5m" since last write
	Live     bool   // stream branch still exists = honeybee still running
}

// sessionsList is the page shell; the listing body auto-refreshes via HTMX so
// new sessions appear and live ones update without a manual reload.
func (s *Server) sessionsList(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Follow off-box runs: fast-forward local main so an agent's sessions on
	// another host are visible on first paint, not only after the body poll.
	s.followMain(r.Context(), time.Now())
	s.render(w, "session_list.html", map[string]interface{}{"Name": sm.Name})
}

// sessionsListBody returns just the <ul>, read live from the sessions dir.
func (s *Server) sessionsListBody(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	sync := s.followMain(r.Context(), now)
	s.render(w, "session_list_body.html", map[string]interface{}{
		"Name": sm.Name, "Sessions": s.sessionInfos(r.Context(), sm.SessionsDir(), now), "Sync": sync,
	})
}

// sessionView is the page shell; its body auto-refreshes via HTMX polling.
func (s *Server) sessionView(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	branch := r.PathValue("branch")
	if !safeBranch(branch) {
		http.Error(w, "bad branch", http.StatusBadRequest)
		return
	}
	// Follow off-box runs before deriving liveness so a session that started on
	// another host reads its stub locally (correct running/ended badge on load).
	s.followMain(r.Context(), time.Now())
	s.render(w, "session_view.html", map[string]interface{}{
		"Name":   sm.Name,
		"Branch": branch,
		"Live":   s.sessionLive(r.Context(), sm.SessionsDir(), branch),
	})
}

// sessionLive reports whether a session page should show the running badge. Only
// STUB files whose stream branch still exists are live; final transcripts and
// orphaned stubs whose branch is gone are ended.
func (s *Server) sessionLive(ctx context.Context, dir, id string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil {
		return false
	}
	streamBranch, isStub := repo.ParseSessionStub(string(raw))
	if !isStub {
		return false
	}
	rem, _ := s.git.Remote(ctx)
	_, ok := s.branchTipTime(ctx, streamBranch, rem)
	return ok
}

// sessionBody returns just the transcript text. While a session runs, main holds
// a STUB at the path naming the isolated branch the transcript streams to; we
// resolve that branch (fetching from the remote when the honeybee is on another
// host) and render the branch's copy, so the UI shows the session in near real
// time without sharing a filesystem with the honeybee. A finished session's
// path holds the durable final transcript, rendered directly. opencode is never
// contacted here, so viewers add no load to the agent server.
func (s *Server) sessionBody(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	branch := r.PathValue("branch")
	if !safeBranch(branch) {
		http.Error(w, "bad branch", http.StatusBadRequest)
		return
	}
	// Follow off-box runs: fast-forward local main so a stub an agent published on
	// another host is present before we resolve/render it. sync backs the pane's
	// staleness banner (nil on a single-host repo).
	sync := s.followMain(r.Context(), time.Now())
	sessRel := "submodules/" + sm.Name + "/sessions/" + branch + ".md"
	b, err := os.ReadFile(filepath.Join(sm.SessionsDir(), branch+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			s.render(w, "session_body.html", map[string]interface{}{"Body": "(waiting for session output…)", "Sync": sync})
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	body := string(b)
	if streamBranch, isStub := repo.ParseSessionStub(body); isStub {
		if live, ok := s.readSessionBranch(r.Context(), streamBranch, sessRel); ok {
			body = live
		} else {
			rem, _ := s.git.Remote(r.Context())
			if _, exists := s.branchTipTime(r.Context(), streamBranch, rem); exists {
				// Branch exists but the transcript file isn't on it yet: a session that
				// just started and hasn't written its first commit. Genuinely waiting.
				body = "(waiting for session output…)"
			} else {
				// Stub on main but the stream branch is gone: the session ended without
				// its finalize replacing the stub (crash, or an orphaned publish). Say so
				// rather than implying it is still spinning up.
				body = "(session ended — live stream branch is gone; no final transcript was published)"
			}
		}
	}
	s.render(w, "session_body.html", map[string]interface{}{"Body": body, "Sync": sync})
}

// readSessionBranch returns the transcript file as it stands on the isolated
// session branch. For a distributed honeybee it fetches the branch from the
// remote first; for a local-only hive it reads the local branch ref directly.
func (s *Server) readSessionBranch(ctx context.Context, branch, rel string) (string, bool) {
	if !safeBranch(branch) {
		return "", false
	}
	if rem, _ := s.git.Remote(ctx); rem != "" {
		_ = s.git.Fetch(ctx, rem, branch) // best-effort: stale ref is better than nothing
		if out, err := s.git.Show(ctx, rem+"/"+branch, rel); err == nil {
			return out, true
		}
	}
	if out, err := s.git.Show(ctx, branch, rel); err == nil {
		return out, true
	}
	return "", false
}

// sessionInfos lists recorded sessions from the committed sessions dir. An entry
// whose file is a STUB is a running session: its freshness/liveness come from the
// streaming branch's tip commit time, not the stub's (fixed) mtime. A non-stub
// entry is a finished session, dated by its file mtime.
func (s *Server) sessionInfos(ctx context.Context, dir string, now time.Time) []sessionInfo {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	rem, _ := s.git.Remote(ctx)
	var out []sessionInfo
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		mod := fi.ModTime()
		live := false
		if raw, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			if branch, isStub := repo.ParseSessionStub(string(raw)); isStub {
				// A stub streams to an isolated branch that the honeybee deletes on exit
				// (deferred, even on error/orphaned publish). So branch existence tracks
				// the live process directly: live iff the branch still exists. Do NOT gate
				// on tip recency — a running session can go many seconds (a long quiet
				// turn) without writing transcript, and must not flip to idle meanwhile.
				if t, ok := s.branchTipTime(ctx, branch, rem); ok {
					mod = t
					live = true
				} else {
					live = false
				}
			}
		}
		out = append(out, sessionInfo{
			ID:       id,
			Modified: mod,
			Ago:      humanAgo(now.Sub(mod)),
			Live:     live,
		})
	}
	// Newest activity first so running sessions sit at the top.
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// branchTipTime returns the last-commit time of a session branch (preferring the
// remote-tracking ref when distributed). ok is false when the branch is gone
// (e.g. a finished session whose branch was deleted).
func (s *Server) branchTipTime(ctx context.Context, branch, rem string) (time.Time, bool) {
	if !safeBranch(branch) {
		return time.Time{}, false
	}
	refs := []string{branch}
	if rem != "" {
		refs = []string{rem + "/" + branch, branch}
	}
	for _, ref := range refs {
		out, err := s.git.Run(ctx, "log", "-1", "--format=%ct", ref)
		if err != nil {
			continue
		}
		if sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			return time.Unix(sec, 0), true
		}
	}
	return time.Time{}, false
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// safeBranch guards the path component against traversal; branches are simple
// stems like "bee-bootstrap" or "bee-T3".
func safeBranch(b string) bool {
	if b == "" || len(b) > 128 {
		return false
	}
	for _, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return !strings.Contains(b, "..")
}

// syncStatus is the session panes' staleness banner: how long since the viewer
// last fast-forwarded local main to follow off-box honeybee sessions, and the
// last pull error if local main could not be advanced (e.g. a non-fast-forward
// divergence). It is nil for a single-host repo (no remote), where sessions are
// authored locally and there is nothing to follow.
type syncStatus struct {
	Ago string // human time since the last pull attempt ("never" before the first)
	Err string // last pull error, if any — surfaced so a stalled follow is visible
}

// followMain advances local main toward the remote so a beehived on THIS host
// sees the session stubs and final transcripts an off-box honeybee published to
// origin/main, and returns the staleness banner for the session panes. The pull
// is coalesced (pullIvl) so N polling panes make at most one network pull per
// interval, and a non-fast-forward divergence is non-fatal: the pane renders the
// last good state and the banner surfaces the error. Returns nil when the repo
// has no remote (single-host: nothing to follow, no staleness to show).
func (s *Server) followMain(ctx context.Context, now time.Time) *syncStatus {
	remote, err := s.git.Remote(ctx)
	if err != nil || remote == "" {
		return nil
	}
	s.pullMain(ctx, remote)
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	st := &syncStatus{Ago: "never"}
	if s.pulled {
		st.Ago = humanAgo(now.Sub(s.lastPull))
	}
	if s.pullErr != nil {
		st.Err = s.pullErr.Error()
	}
	return st
}

// pullMain runs a coalesced `git pull --ff-only main` from remote, recording the
// outcome for the staleness banner. It is serialized with the frontend's own
// commits/publish via gitMu (shared primary-checkout index). A non-fast-forward
// is recorded, never merged: main here is a projection the swarm converges by
// fast-forward, so a divergence is transient drift a later ff pull or a publish
// heals — the viewer must not author a merge commit. The interval reservation
// (set lastPull before pulling) means concurrent polls return immediately rather
// than stampeding the remote; pullIvl<=0 disables coalescing (tests).
func (s *Server) pullMain(ctx context.Context, remote string) {
	s.syncMu.Lock()
	if s.pulled && s.pullIvl > 0 && time.Since(s.lastPull) < s.pullIvl {
		s.syncMu.Unlock()
		return
	}
	s.pulled = true
	s.lastPull = time.Now()
	s.syncMu.Unlock()

	s.gitMu.Lock()
	perr := s.git.Pull(ctx, remote, "main")
	s.gitMu.Unlock()

	s.syncMu.Lock()
	s.pullErr = perr
	s.lastPull = time.Now()
	s.syncMu.Unlock()
}
