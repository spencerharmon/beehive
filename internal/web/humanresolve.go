package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// human-resolve: the operator surface for a NEEDS-HUMAN task. A NEEDS-HUMAN task
// blocks the swarm until an operator clears a concrete blocker (a credential, a
// public-contract decision, a contradictory spec, a missing upstream API). The
// page wires three orthogonal levers, matching how a blocker actually clears:
//
//  1. the AI resolution agent \u2014 a general, tool-using agent (resolveManager /
//     resolveSession) working in a private worktree that investigates the blocker
//     and makes multi-file beehive-layer changes to help clear it. The operator
//     reviews the accumulated diff and Publishes (or Discards). See resolveagent.go.
//  2. the Secrets panel \u2014 for a credential the blocker needs (values never touch
//     this chat or a file); and
//  3. the deterministic "Mark resolved" action that flips NEEDS-HUMAN -> TODO
//     (plan.Task.Resolve) and publishes PLAN.md to main, so selection re-picks the
//     task and the swarm resumes.
//
// The agent can propose/apply coordination-layer changes and run tools, but the
// actual unblock (the status flip) is a separate deterministic action the LLM
// never performs \u2014 a model can never free-rewrite the multi-task PLAN.md or
// silently un-escalate a blocker.

// ---- HTTP handlers ----

// humanTask locates a submodule's NEEDS-HUMAN task and returns it projected as a
// PlanItem (carrying the blocker reason, description and deps). ok is false when
// the submodule or task is unknown OR the task is no longer NEEDS-HUMAN (e.g. it
// was resolved in another tab), so the caller renders a 404 rather than acting on
// a stale link.
func (s *Server) humanTask(ctx context.Context, sub, id string) (repo.Submodule, PlanItem, bool) {
	sm, err := s.submodule(sub)
	if err != nil {
		return repo.Submodule{}, PlanItem{}, false
	}
	p, err := s.planView(s.headSHA(ctx), sm.PlanPath(), time.Now(), s.ttl())
	if err != nil {
		return repo.Submodule{}, PlanItem{}, false
	}
	for _, it := range p.Items {
		if it.ID == id {
			if it.Status != StatusHuman {
				return sm, it, false
			}
			return sm, it, true
		}
	}
	return sm, PlanItem{}, false
}

// humanResolvePage renders the per-task resolution workspace: the blocker
// context, the secret/resolve levers, and the AI resolution agent (its chat +
// live multi-file diff + Publish/Discard). A task that is unknown or no longer
// NEEDS-HUMAN is a 404 (its link went stale).
func (s *Server) humanResolvePage(w http.ResponseWriter, r *http.Request) {
	sub, id := r.PathValue("sub"), r.PathValue("id")
	sm, it, ok := s.humanTask(r.Context(), sub, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sess, err := s.humans.session(r.Context(), sub, it)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "human_resolve.html", map[string]interface{}{
		"Sub":    sm.Name,
		"Item":   it,
		"SessID": sess.ID,
		"Title":  pageTitle("resolve", it.ID, sm.Name),
	})
}

// humanResolvePanel renders the resolution agent's live chat log, multi-file diff
// and Publish/Discard controls. Polled by the page while a turn is in flight.
func (s *Server) humanResolvePanel(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.humans.get(r.PathValue("sid"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "human_resolve_panel.html", s.resolvePanelData(r.Context(), sess))
}

// humanResolveMessage runs one agent turn in the background (tool-using turns can
// be long) and returns the refreshed panel; the page poll shows progress and the
// resulting diff.
func (s *Server) humanResolveMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.humans.get(r.PathValue("sid"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if msg := strings.TrimSpace(r.FormValue("message")); msg != "" {
		_ = sess.startChat(context.Background(), msg)
	}
	s.render(w, "human_resolve_panel.html", s.resolvePanelData(r.Context(), sess))
}

// humanResolvePublish lands the agent's committed changes on the hive main. It
// serializes against the follow-the-remote pull and other frontend writes
// (Server.gitMu), and resolves the publish target with the same trusted-remote
// rule as the editor (own remote, else local main).
func (s *Server) humanResolvePublish(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.humans.get(r.PathValue("sid"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Hold gitMu across BOTH the remote probe (which fetches on the primary repo)
	// and the publish, so neither races the follow-the-remote pull or another
	// frontend write on the primary checkout's index/refs.
	s.gitMu.Lock()
	remote, err := s.humans.publishRemote(r.Context())
	if err == nil {
		err = sess.publish(r.Context(), remote)
	}
	s.gitMu.Unlock()
	data := s.resolvePanelData(r.Context(), sess)
	if err != nil {
		data["Error"] = err.Error()
	}
	s.render(w, "human_resolve_panel.html", data)
}

// humanResolveDiscard tears down the agent's worktree/branch (dropping
// unpublished changes) by session id, then — if the task is still NEEDS-HUMAN —
// opens a fresh session so the operator can restart cleanly. Operating by sid
// (not by re-resolving the task) means a stale worktree can still be discarded
// after the task was resolved in another tab.
func (s *Server) humanResolveDiscard(w http.ResponseWriter, r *http.Request) {
	sub, id := r.PathValue("sub"), r.PathValue("id")
	sess, ok := s.humans.get(r.PathValue("sid"))
	if !ok {
		http.Redirect(w, r, "/human", http.StatusSeeOther)
		return
	}
	s.humans.forget(r.Context(), sess.Sub, sess.TaskID)
	// Re-open only if the task is still blocked; otherwise there is nothing to work.
	if sm, it, blocked := s.humanTask(r.Context(), sub, id); blocked {
		if _, err := s.humans.session(r.Context(), sm.Name, it); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/human/"+sub+"/"+id, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/human", http.StatusSeeOther)
}

// fileDiff is one changed file's rendered diff for the resolve panel: its path
// and the per-line DiffRows, so a multi-file resolution change previews each
// file under its own path — colorized through the shared renderer like the
// editor/chat/skill panels, never a raw patch.
type fileDiff struct {
	Path string
	Rows []editor.DiffRow
}

// renderFileDiff renders one file's before->after change as unified DiffRows,
// syntax-highlighted through the file's chroma lexer when its name matches one
// (else the plain char-diff fallback) — identical to how the chat-diff editor
// colorizes a file (chatPanelData), so every resolve-panel file reads the same.
func renderFileDiff(path, before, after string) []editor.DiffRow {
	lexer := lexerFor(path)
	return editor.RenderDiffHTML(before, after, highlightLines(before, lexer), highlightLines(after, lexer))
}

// resolvePanelData projects a resolution session into the panel template model.
func (s *Server) resolvePanelData(ctx context.Context, sess *resolveSession) map[string]interface{} {
	stat, files, err := sess.changedFiles(ctx)
	diffs := make([]fileDiff, 0, len(files))
	for _, f := range files {
		diffs = append(diffs, fileDiff{Path: f.Path, Rows: renderFileDiff(f.Path, f.Before, f.After)})
	}
	data := map[string]interface{}{
		"SessID":    sess.ID,
		"Sub":       sess.Sub,
		"TaskID":    sess.TaskID,
		"Log":       sess.logCopy(),
		"Stat":      stat,
		"Diffs":     diffs,
		"HasChange": len(diffs) > 0,
		"Busy":      sess.isBusy(),
		"Published": sess.isPublished(),
		"Error":     sess.errText(),
	}
	if err != nil {
		data["Error"] = err.Error()
	}
	return data
}

// humanResolveApply flips a NEEDS-HUMAN task back to TODO (plan.Task.Resolve) and
// publishes PLAN.md to main so the swarm re-selects it. It mirrors the CLI
// `task human` write path in reverse: read+parse the submodule PLAN.md, mutate the
// one task, serialize, write, and publish through the shared frontend write path
// (publishMain: commit -> push). Resolve rejects a non-NEEDS-HUMAN task, so a
// double-submit or a stale link can never reset an in-flight task's status/claim.
func (s *Server) humanResolveApply(w http.ResponseWriter, r *http.Request) {
	sub, id := r.PathValue("sub"), r.PathValue("id")
	sm, err := s.submodule(sub)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	planPath := sm.PlanPath()
	b, err := os.ReadFile(planPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t := p.Task(id)
	if t == nil {
		http.NotFound(w, r)
		return
	}
	if err := t.Resolve(time.Now().UTC()); err != nil {
		// Non-NEEDS-HUMAN (already resolved, or never blocked): a conflict, not a
		// server fault \u2014 surface it without touching PLAN.md.
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg := fmt.Sprintf("frontend: resolve NEEDS-HUMAN %s (%s) -> TODO\n\nBeehive: %s plan", id, sm.Name, id)
	if err := s.publishMain(r.Context(), msg); err != nil {
		http.Error(w, "resolved locally but publish to remote failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Drop the resolution agent for this task: it is resolved, its blocker context
	// is stale, and its worktree can be reclaimed.
	s.humans.forget(r.Context(), sub, id)
	http.Redirect(w, r, "/human", http.StatusSeeOther)
}
