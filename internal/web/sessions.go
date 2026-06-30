package web

import (
	"context"
	"fmt"
	"io"
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
	body, err := s.resolveSessionBody(r.Context(), sm, branch)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "session_body.html", map[string]interface{}{"Body": body})
}

// resolveSessionBody computes the transcript display text for a session branch:
// the live stream-branch copy of a running session's stub (fetched from the
// remote when distributed), the durable final transcript of a finished session,
// or a plain status line when output isn't available yet / the stream branch is
// gone. It never contacts opencode — purely file/git derived — so both the
// polled body fragment and the SSE stream share one source of truth. The error
// return is reserved for an unexpected read failure (not "absent", which is a
// normal "waiting" state).
func (s *Server) resolveSessionBody(ctx context.Context, sm repo.Submodule, branch string) (string, error) {
	sessRel := "submodules/" + sm.Name + "/sessions/" + branch + ".md"
	b, err := os.ReadFile(filepath.Join(sm.SessionsDir(), branch+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "(waiting for session output…)", nil
		}
		return "", err
	}
	body := string(b)
	if streamBranch, isStub := repo.ParseSessionStub(body); isStub {
		if live, ok := s.readSessionBranch(ctx, streamBranch, sessRel); ok {
			body = live
		} else {
			rem, _ := s.git.Remote(ctx)
			if _, exists := s.branchTipTime(ctx, streamBranch, rem); exists {
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
	return body, nil
}

// sessionStreamInterval is how often the SSE handler re-derives the transcript
// from disk/git and pushes it if it changed. 1s keeps the pane near real time
// while staying lighter than (and independent of) the 2s htmx poll it supersedes.
const sessionStreamInterval = 1 * time.Second

// sessionStream pushes the session transcript to the browser over Server-Sent
// Events, so the session pane updates live without a SPA. It reuses
// resolveSessionBody (file/git derived — opencode is never contacted), sends an
// immediate first frame, then re-checks every sessionStreamInterval and pushes
// only on change. The loop is bounded by the request context: a client
// disconnect cancels r.Context(), the select returns, and the handler exits —
// no goroutine is spawned, so nothing leaks. A server/proxy without streaming
// (no Flusher) returns 501 and the client keeps its htmx poll.
func (s *Server) sessionStream(w http.ResponseWriter, r *http.Request) {
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
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering so frames flush live

	ctx := r.Context()
	last, sent := "", false
	push := func() {
		body, err := s.resolveSessionBody(ctx, sm, branch)
		if err != nil {
			return // transient read error: skip this tick, keep the stream open
		}
		if sent && body == last {
			return // unchanged: don't re-send (the client already shows it)
		}
		last, sent = body, true
		writeSSE(w, body)
		fl.Flush()
	}
	push() // immediate first frame so the pane fills without waiting a tick
	t := time.NewTicker(sessionStreamInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			push()
		}
	}
}

// writeSSE writes payload as one SSE event, emitting each line as its own "data:"
// field so a multi-line transcript round-trips (the browser rejoins the data
// fields with "\n"). The event is terminated by the blank line SSE requires.
func writeSSE(w io.Writer, payload string) {
	for _, line := range strings.Split(payload, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
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
