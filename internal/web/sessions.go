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
	s.render(w, "session_list.html", map[string]interface{}{"Name": sm.Name})
}

// sessionsListBody returns just the <ul>, read live from the sessions dir.
func (s *Server) sessionsListBody(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "session_list_body.html", map[string]interface{}{
		"Name": sm.Name, "Sessions": s.sessionInfos(r.Context(), sm.SessionsDir(), time.Now()),
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
	sessRel := "submodules/" + sm.Name + "/sessions/" + branch + ".md"
	b, err := os.ReadFile(filepath.Join(sm.SessionsDir(), branch+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			s.render(w, "session_body.html", map[string]interface{}{"Body": "(waiting for session output…)", "Pull": s.sessionPull(r.Context())})
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
	s.render(w, "session_body.html", map[string]interface{}{"Body": body, "Pull": s.sessionPull(r.Context())})
}

// sessionPull is the freshness banner for the session pane: when the beehive repo
// has a remote, the viewer is fast-forwarding main to follow off-box honeybees,
// so the pane reports how long since the last successful pull (and any ff-only
// failure). On a single-host repo (no remote) there is nothing to follow and the
// banner stays hidden (Remote=false).
func (s *Server) sessionPull(ctx context.Context) pullStatus {
	rem, _ := s.git.Remote(ctx)
	return s.pullStatusAt(s.clock(), rem != "")
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
