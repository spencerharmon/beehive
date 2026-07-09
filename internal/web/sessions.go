package web

import (
	"context"
	"encoding/json"
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
	Display  string // session-list-links-labels: ID shortened for display (sessionDisplayName)
	Kind     string // stats-tag-model's kind tag (sessionTags), "" if undetermined
	Model    string // stats-tag-model's model tag (sessionTags), "" if undetermined
	sessionLink
}

// sessionLink is delivery-traceability's/branch-graph-sectioned's doc+commit
// resolution (DeliveryLink, delivery.go) plus a link to the task's own
// PLAN.md row, reused for ONE session (session-list-links-labels): "link
// every session to the change(s) it moved" (as worker, reviewer, or
// arbitrator) — one click from the session to the task, the change doc, and
// the hive commit that delivered it. Keyed off the task id the session's file
// stem names (taskIDForSession); every field is "" when unresolved (id has no
// task-shaped stem, no plan row, no stamped doc, no DONE flip yet) — never a
// dead link, matching DeliveryLink/resolveDocHref's own contract. Shared by
// the session-list row (sessionInfo, embedded above) and the session page
// (sessionView).
type sessionLink struct {
	TaskID   string
	TaskHref string // link to the task's PLAN.md row (plan-row-deep-anchors), "" if not in the current plan
	DocPath  string // change-doc path the task's implementing commit stamped, "" if none
	DocHref  string // link to view that doc (branch-graph-sectioned), "" if unresolved
	FlipSHA  string // short hive commit sha that flipped the task DONE, "" if not (yet) applicable
	FlipHref string // link to view that hive commit (delivery-traceability), "" if unresolved
}

// taskIDForSession derives the task id a session's file stem names, reusing
// stats.go's bee-<task>-<epoch>-<pid> convention (sessionNameRE) — the SAME
// split stats' per-task honeybee tally already relies on, so a shortened
// display name and a resolved task/doc/commit link never disagree about what
// task a row names. "" when id does not match: a legacy pre-pid stem (session
// naming added the trailing -<pid> after some history had already
// accumulated — see swarm.SessionID), or a non-task session kind
// (bee-bootstrap-*/bee-reconcile-*, which name no PLAN task at all) — left
// unresolved rather than guessed.
func taskIDForSession(id string) string {
	if m := sessionNameRE.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return ""
}

// sessionDisplayName shortens a session id for display
// (session-list-links-labels): the "bee-" prefix and the "-<epoch>-<pid>"
// suffix stripped — exactly taskIDForSession's capture, reused here so the
// label a row shows and the task it links to always name the same thing.
// Falls back to the id unchanged when it doesn't match that shape: never a
// mangled/ambiguous guess (a legacy pre-pid id can itself end in digits, e.g.
// "ui-audit-002", which would be unsafe to split without a trailing pid to
// anchor where the task name ends).
func sessionDisplayName(id string) string {
	if t := taskIDForSession(id); t != "" {
		return t
	}
	return id
}

// sessionLinksFor resolves sessionLink for every id in ids
// (session-list-links-labels), reusing delivery-traceability's/branch-graph-
// sectioned's existing doc+commit machinery verbatim rather than re-deriving
// it:
//
//   - the change doc (DocPath/DocHref) comes from changeDocsByTask/
//     resolveDocHref (branches.go) for ANY task status, not only DONE — a
//     worker session's own task is usually still NEEDS-REVIEW (not yet DONE)
//     when its doc first exists. changeDocsByTask takes no `want` filter (it
//     is always the FULL task-id -> doc-path map), so sharing buildDeliveries'
//     own "delivery-docs:"+sm.Name cache entry here is always correct: no
//     caller can ever see a narrower result than another's.
//   - the hive flip commit (FlipSHA/FlipHref) comes from buildDeliveries
//     (delivery.go), called with the EXACT SAME argument computeStats already
//     uses (doneTaskIDs(sm)) rather than this call's own session-derived ids.
//     DONE is a terminal status (internal/plan's state machine has no
//     transition out of it), so a task that is not currently DONE never has a
//     flip commit no matter what `want` asks for — meaning doneTaskIDs(sm) is
//     ALWAYS the complete, correct `want` set for this half, and passing the
//     same deterministic argument every caller uses keeps this call on the
//     SAME "delivery-flips:"+sm.Name cache entry with no risk of one caller's
//     `want` shadowing another's (cachedView's key carries no memory of what
//     `want` a load populated it with — see cache.go).
//
// TaskHref links to the task's own PLAN.md row (plan-row-deep-anchors) only
// when it is still present in sm's CURRENT plan p; a task retired from the
// plan still resolves its doc/flip (both are git-history reads, independent
// of the live PLAN.md) but gets no TaskHref. Returns a map keyed by SESSION id
// (not task id, so multiple sessions sharing one task each get their own,
// identical entry); an id with no derivable task id still gets a zero-value
// entry (every field ""), never omitted, so a caller can range over ids
// without a second existence check.
func (s *Server) sessionLinksFor(ctx context.Context, head string, sm repo.Submodule, p Plan, ids []string) map[string]sessionLink {
	out := make(map[string]sessionLink, len(ids))
	taskOf := make(map[string]string, len(ids))
	wantTasks := make(map[string]bool)
	for _, id := range ids {
		t := taskIDForSession(id)
		taskOf[id] = t
		if t != "" {
			wantTasks[t] = true
		}
	}
	if len(wantTasks) == 0 {
		for _, id := range ids {
			out[id] = sessionLink{}
		}
		return out
	}
	planIDs := make(map[string]bool, len(p.Items))
	for _, it := range p.Items {
		planIDs[it.ID] = true
	}
	docs, _ := cachedView(head, s.cache, "delivery-docs:"+sm.Name, func() (map[string]string, error) {
		return changeDocsByTask(ctx, sm.RepoDir()), nil
	})
	flips := indexDeliveries(s.buildDeliveries(ctx, head, sm, doneTaskIDs(sm)))
	for _, id := range ids {
		t := taskOf[id]
		if t == "" {
			out[id] = sessionLink{}
			continue
		}
		link := sessionLink{TaskID: t, DocPath: docs[t], DocHref: resolveDocHref(sm, docs[t])}
		if planIDs[t] {
			link.TaskHref = "/submodule/" + sm.Name + "/plan#task-" + t
		}
		if f, ok := flips[t]; ok {
			link.FlipSHA = f.FlipSHA
			link.FlipHref = f.FlipHref
		}
		out[id] = link
	}
	return out
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
	s.render(w, "session_list.html", map[string]interface{}{"Name": sm.Name, "Title": pageTitle("sessions", sm.Name), "Crumbs": sessionsCrumbs(sm.Name)})
}

// sessionsListBody returns just the <ul>, read live from the sessions dir.
// Polled every 2s (session_list.html); renderConditional (web.go) ETags the
// rendered bytes so a repeat poll with nothing new (the common case once a
// submodule's sessions are all idle) 304s instead of re-sending the list.
func (s *Server) sessionsListBody(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	sync := s.followMain(r.Context(), now)
	s.renderConditional(w, r, "session_list_body.html", map[string]interface{}{
		"Name": sm.Name, "Sessions": s.sessionInfos(r.Context(), sm, now, s.ttl()), "Sync": sync,
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
	ctx := r.Context()
	now, ttl := time.Now(), s.ttl()
	head := s.headSHA(ctx)
	// session-list-links-labels: the SAME task/doc/commit resolution the
	// session list uses (sessionLinksFor, one-id slice), plus the shortened
	// display name shown as the page's primary heading — the full session id
	// (still "Branch" here for the existing route/crumb plumbing) moves to
	// smaller, secondary text below it.
	p, _ := s.planView(head, sm.PlanPath(), now, ttl)
	link := s.sessionLinksFor(ctx, head, sm, p, []string{branch})[branch]
	s.render(w, "session_view.html", map[string]interface{}{
		"Name":     sm.Name,
		"Branch":   branch,
		"Display":  sessionDisplayName(branch),
		"Live":     s.sessionLive(ctx, sm, branch, now, ttl),
		"Title":    pageTitle(branch, sm.Name),
		"Crumbs":   sessionCrumbs(sm.Name, branch),
		"TaskHref": link.TaskHref,
		"DocPath":  link.DocPath,
		"DocHref":  link.DocHref,
		"FlipSHA":  link.FlipSHA,
		"FlipHref": link.FlipHref,
	})
}

// sessionLive reports whether a session page should show the running badge:
// id is active under the SAME canonical rule activeHoneybees/sessionInfos use
// (active-honeybee-count-unify), never a rule of its own. It first checks
// whether id currently claims a fresh PLAN task (claimedSessions); only when
// unclaimed (id has no PLAN task at all — a Bootstrap/Reconcile pass, or a
// stale/finished claim) does it fall back to the stub's own liveness: a STUB
// file whose stream branch still exists is live, a final transcript or an
// orphaned stub whose branch is gone is ended. sm/now/ttl (rather than a bare
// dir) let it resolve sm's PLAN.md for the claim half.
func (s *Server) sessionLive(ctx context.Context, sm repo.Submodule, id string, now time.Time, ttl time.Duration) bool {
	p, _ := s.planView(s.headSHA(ctx), sm.PlanPath(), now, ttl)
	if claimedSessions(p)[id] {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(sm.SessionsDir(), id+".md"))
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

// sessionBody returns the transcript pane (the htmx-poll fragment): the flat
// transcript rendered as structured, sanitized turns with a table-of-contents
// overlay (session-transcript-toc-relanding), never a flat text dump. It shares
// sessionTranscript with the SSE stream, and both render the SAME data through
// the SAME "transcript_pane" template (transcript.go's Server.renderString), so
// the poll and the live stream can never disagree on the turn/TOC HTML for a
// given transcript. Polled every 2s (session_view.html); renderConditional
// (web.go) ETags the rendered bytes so a repeat poll of an idle/ended session
// (the common steady state — most of a transcript's polled lifetime is spent
// unchanged) 304s instead of re-sending the whole transcript.
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
	body, _, sync, err := s.sessionTranscript(r.Context(), sm, branch)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.renderConditional(w, r, "session_body.html", map[string]interface{}{
		"Transcript": parseTranscript(body),
		"Sync":       sync,
	})
}

// sessionStream pushes the session transcript to the browser over server-sent
// events, re-reading it on a short cadence and sending it whenever it changes,
// until the session ends or the client disconnects. It re-derives the SAME
// file-derived transcript sessionBody renders (git/disk, never opencode) and
// renders it through the IDENTICAL "transcript_pane" template (Server.
// renderString, transcript.go) sessionBody executes, so the htmx poll and this
// stream can never disagree on the turn/TOC markup for a given transcript
// (session-transcript-toc-relanding) — the poll is an interchangeable fallback:
// the page cancels it while this stream is live and resumes it on the "end"
// event (which also fetches the authoritative final transcript). Because the
// live page cancels the poll, this loop OWNS following off-box (remote-host)
// sessions: sessionTranscript fast-forwards local main every tick and the
// staleness banner is pushed as a `sync` event so it stays fresh under the
// EventSource. A ResponseWriter that cannot flush (no streaming) is reported so
// the client keeps polling.
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
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	ctx := r.Context()
	t := time.NewTicker(s.streamIvl())
	defer t.Stop()
	last := ""
	lastSync := ""
	first := true
	for {
		// sessionTranscript follows off-box main each call (coalesced by pullIvl),
		// so the SSE path owns off-box follow while the htmx poll is cancelled.
		body, live, sync, err := s.sessionTranscript(ctx, sm, branch)
		if err != nil {
			// Headers are already committed (200), so we cannot change status: end
			// the stream and let the htmx poll fallback surface the real error.
			writeSSEEvent(w, "end", "")
			fl.Flush()
			return
		}
		// Push the staleness banner as its own event so it stays fresh while the
		// poll (which normally refreshes it) is cancelled. Only when following a
		// remote (sync != nil) and only on change, to avoid spamming the client.
		if sync != nil {
			if enc, mErr := json.Marshal(sync); mErr == nil && string(enc) != lastSync {
				writeSSEEvent(w, "sync", string(enc))
				fl.Flush()
				lastSync = string(enc)
			}
		}
		if first || body != last {
			// Render through the SAME "transcript_pane" template sessionBody executes
			// (session-transcript-toc-relanding), so this frame and the htmx poll can
			// never disagree on the turn/TOC HTML for this transcript.
			html, rerr := s.renderString("transcript_pane", parseTranscript(body))
			if rerr != nil {
				// Headers are already committed (200): end the stream and let the htmx
				// poll fallback surface the real error, same as a transcript read error.
				writeSSEEvent(w, "end", "")
				fl.Flush()
				return
			}
			writeSSEData(w, html)
			fl.Flush()
			last = body
			first = false
		}
		if !live {
			// Session ended: tell the client to stop and do one authoritative poll.
			writeSSEEvent(w, "end", "")
			fl.Flush()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sessionTranscript resolves the current transcript text for a session branch and
// whether the session is still live (streaming to an isolated branch that exists).
// It is the shared read used by both the poll (sessionBody) and the SSE stream
// (sessionStream). It FIRST fast-forwards local main so a stub an agent published
// on ANOTHER host is present before we resolve/render it, returning the staleness
// banner (nil on a single-host repo) so both paths surface it. Then a STUB path
// resolves to its live branch copy; a non-stub path is the durable final
// transcript; a stub whose branch is gone is an ended session with no published
// final. A read error (other than a not-yet-created file) is returned so the poll
// path can surface it as a 500.
func (s *Server) sessionTranscript(ctx context.Context, sm repo.Submodule, branch string) (body string, live bool, sync *syncStatus, err error) {
	// Follow off-box runs BEFORE resolving the transcript. Coalesced (pullIvl) so
	// the SSE loop calling this each tick makes at most one network pull per
	// interval; nil on a single-host repo (no remote, nothing to follow).
	sync = s.followMain(ctx, time.Now())
	sessRel := "submodules/" + sm.Name + "/sessions/" + branch + ".md"
	b, err := os.ReadFile(filepath.Join(sm.SessionsDir(), branch+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			// Not created yet: a session just starting. Keep waiting (still live).
			return "(waiting for session output…)", true, sync, nil
		}
		return "", false, sync, err
	}
	body = string(b)
	streamBranch, isStub := repo.ParseSessionStub(body)
	if !isStub {
		return body, false, sync, nil // durable final transcript: ended
	}
	if text, ok := s.readSessionBranch(ctx, streamBranch, sessRel); ok {
		return text, true, sync, nil
	}
	rem, _ := s.git.Remote(ctx)
	if _, exists := s.branchTipTime(ctx, streamBranch, rem); exists {
		// Branch exists but the transcript file isn't on it yet: a session that
		// just started and hasn't written its first commit. Genuinely waiting.
		return "(waiting for session output…)", true, sync, nil
	}
	// Stub on main but the stream branch is gone: the session ended without its
	// finalize replacing the stub (crash, or an orphaned publish). Say so rather
	// than implying it is still spinning up.
	return "(session ended — live stream branch is gone; no final transcript was published)", false, sync, nil
}

// writeSSEData emits a (possibly multi-line) payload as one SSE message event:
// each line becomes its own data: field, which the browser rejoins with "\n".
func writeSSEData(w io.Writer, payload string) {
	for _, line := range strings.Split(payload, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}

// writeSSEEvent emits a named SSE event (e.g. "end", "sync") with an optional
// payload (a multi-line payload is split into one data: field per line).
func writeSSEEvent(w io.Writer, event, payload string) {
	fmt.Fprintf(w, "event: %s\n", event)
	if payload == "" {
		fmt.Fprint(w, "data: \n\n")
		return
	}
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

// sessionInfos lists recorded sessions from the committed sessions dir. Live
// unions the SAME two canonical signals activeHoneybees does (active-honeybee-
// count-unify): a fresh PLAN-task claim on this session id (claimed, from sm's
// own plan view), OR — the only signal available for a claimless Bootstrap/
// Reconcile pass — a STUB file whose streaming branch still exists. It does not
// call activeHoneybees directly: this is itself a sessions/ directory walk (run
// on a 2s poll via sessionsListBody), so re-scanning the same directory a
// second time would double the I/O for no benefit; claimedSessions (a cheap,
// no-I/O map over the already-cached plan view) supplies the claim half
// in-line instead. An entry whose file is a STUB is a running session: its
// freshness/liveness come from the streaming branch's tip commit time, not the
// stub's (fixed) mtime. A non-stub entry is a finished session, dated by its
// file mtime.
//
// Display/Kind/Model/sessionLink are session-list-links-labels' additions: a
// shortened display name (sessionDisplayName), the kind+model tags stats-tag-
// model's sessionTags already derives from the SAME transcript (a live/stub
// session has no header yet, so both are leniently "" until it finalizes),
// and the task/doc/commit resolution (sessionLinksFor) — "link every session
// to the change(s) it moved".
func (s *Server) sessionInfos(ctx context.Context, sm repo.Submodule, now time.Time, ttl time.Duration) []sessionInfo {
	dir := sm.SessionsDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	head := s.headSHA(ctx)
	p, _ := s.planView(head, sm.PlanPath(), now, ttl)
	claimed := claimedSessions(p)
	rem, _ := s.git.Remote(ctx)
	var out []sessionInfo
	var ids []string
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
		live := claimed[id]
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
				}
			}
		}
		tags := s.sessionTags(sessionRef{submodule: sm.Name, path: filepath.Join(dir, e.Name())})
		ids = append(ids, id)
		out = append(out, sessionInfo{
			ID:       id,
			Modified: mod,
			Ago:      humanAgo(now.Sub(mod)),
			Live:     live,
			Display:  sessionDisplayName(id),
			Kind:     tags["kind"],
			Model:    tags["model"],
		})
	}
	links := s.sessionLinksFor(ctx, head, sm, p, ids)
	for i := range out {
		out[i].sessionLink = links[out[i].ID]
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
