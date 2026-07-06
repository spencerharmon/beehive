package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// human-resolve-chat: an AI-assisted resolution surface for NEEDS-HUMAN tasks. A
// NEEDS-HUMAN task blocks the swarm until an operator clears a concrete blocker (a
// credential, a public-contract decision, a contradictory spec, a missing upstream
// API). This wires two operator actions onto each blocked task:
//
//  1. an AI chat, seeded with the task's full blocker context, that helps the
//     operator understand and clear it. The chat IS the existing chat-diff editor
//     (chatManager/chatSession, the /edit/{id}/... routes) opened over a
//     beehive-layer target file with a task-aware system prompt — so it inherits
//     the propose-then-approve diff loop unchanged (nothing is written or committed
//     without explicit approval); and
//  2. a deterministic "resolve" action that flips the task NEEDS-HUMAN -> TODO
//     (plan.Task.Resolve) and publishes PLAN.md to main, so selection picks the
//     task up again and the swarm resumes.
//
// The two are independent: the chat helps the operator DECIDE and can propose a
// file edit, but the actual unblock (the status flip) is a separate deterministic
// action the LLM never performs — a model can never free-rewrite the multi-task
// PLAN.md or silently un-escalate a blocker.

// humanManager maps each NEEDS-HUMAN task to its (reused) resolution chat session.
// The chat sessions themselves live in the shared chatManager (so the /edit routes
// serve them); this only remembers, per task, which chat session backs its
// resolution page, so re-opening the page reuses the same conversation instead of
// cutting a fresh worktree each load.
type humanManager struct {
	chat *chatManager

	mu     sync.Mutex
	byTask map[string]string // "<sub>/<id>" -> chat session ID
}

func newHumanManager(chat *chatManager) *humanManager {
	return &humanManager{chat: chat, byTask: map[string]string{}}
}

// taskKey is the per-task map key; sub and id are already validated basenames.
func taskKey(sub, id string) string { return sub + "/" + id }

// session returns the resolution chat session for a task, opening one on first
// use over targetPath under a blocker-seeded system prompt and reusing it (by id)
// on later loads. A remembered id whose session has since been reclaimed
// (chat.get miss) is transparently reopened.
func (m *humanManager) session(ctx context.Context, sub string, it PlanItem, targetPath string) (*chatSession, error) {
	key := taskKey(sub, it.ID)
	m.mu.Lock()
	id, ok := m.byTask[key]
	m.mu.Unlock()
	if ok {
		if sess, live := m.chat.get(id); live {
			return sess, nil
		}
	}
	clean, err := cleanRepoPath(targetPath)
	if err != nil {
		return nil, err
	}
	sys := humanResolveSystemPrompt(sub, it, clean)
	sess, err := m.chat.openWith(ctx, clean, sys)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.byTask[key] = sess.ID
	m.mu.Unlock()
	return sess, nil
}

// humanResolveSystemPrompt seeds the resolution chat with the concrete blocker so
// the AI can help the operator clear it, layered on top of the generic chat-diff
// editor contract for the target file (chatSystemPrompt) so a proposed change
// still flows through the human-approved diff loop.
func humanResolveSystemPrompt(sub string, it PlanItem, path string) string {
	reason := it.HumanReason
	if strings.TrimSpace(reason) == "" {
		reason = "(no explicit reason recorded on the task)"
	}
	desc := it.Desc
	if strings.TrimSpace(desc) == "" {
		desc = "(no description on the task)"
	}
	return fmt.Sprintf(`You are assisting a human operator in RESOLVING a task that an autonomous coding
swarm escalated to NEEDS-HUMAN. A NEEDS-HUMAN task is blocked and NOT worked by
the swarm until the operator clears the blocker and reopens it.

Blocked task: %[1]s   (submodule: %[2]s)
Summary: %[3]s
Why it is blocked (operator input required): %[4]s

Your job: help the operator understand EXACTLY what is needed and how to provide
it, then confirm when the blocker is cleared. The operator's available actions in
this UI are:
  - add a credential/token via the Secrets panel (secret values are encrypted and
    are NEVER pasted into this chat or into files);
  - edit a repository file (you may propose an edit to %[5]s here, applied only on
    approval);
  - click "Mark resolved" to flip this task back to TODO so the swarm resumes.

Be concrete and specific to THIS blocker. If a change to %[5]s is what resolves
it, propose it. If the resolution is a secret or an out-of-band action, explain
precisely what to provide and in what format, and do NOT propose a file edit.

%[6]s`,
		it.ID, sub, desc, reason, path, chatSystemPrompt(path))
}

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

// humanTargetPath resolves the file the resolution chat proposes edits to: an
// explicit ?path= (any repo-relative beehive-layer file) or, by default, the
// submodule's ROI.md — the human-owned intent doc, a safe editable default that
// the chat-diff editor already treats as human-owned. The path is only a seed for
// the chat; validation happens in the chat manager (cleanRepoPath).
func humanTargetPath(r *http.Request, sub string) string {
	if p := strings.TrimSpace(r.URL.Query().Get("path")); p != "" {
		return p
	}
	return "submodules/" + sub + "/" + repo.ROIFile
}

// humanResolvePage renders the per-task resolution workspace: the blocker context,
// the AI resolution chat (the shared chat-diff panel, loaded by session id), links
// to the secret/edit surfaces, and the deterministic resolve action. A task that
// is unknown or no longer NEEDS-HUMAN is a 404 (its link went stale).
func (s *Server) humanResolvePage(w http.ResponseWriter, r *http.Request) {
	sub, id := r.PathValue("sub"), r.PathValue("id")
	sm, it, ok := s.humanTask(r.Context(), sub, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	target := humanTargetPath(r, sub)
	sess, err := s.humans.session(r.Context(), sub, it, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "human_resolve.html", map[string]interface{}{
		"Sub":    sm.Name,
		"Item":   it,
		"ChatID": sess.ID,
		"Path":   sess.Path,
	})
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
		// server fault — surface it without touching PLAN.md.
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
	// Drop the remembered chat session for this task: it is resolved, its blocker
	// context is stale. Any live worktree is a clean, abandoned edit branch the
	// editor's startup GC reclaims (same as an abandoned chat-edit).
	s.humans.forget(sub, id)
	http.Redirect(w, r, "/human", http.StatusSeeOther)
}

// forget drops the remembered resolution session for a task (called after resolve
// so a later NEEDS-HUMAN re-escalation of the same id opens a fresh conversation).
func (m *humanManager) forget(sub, id string) {
	m.mu.Lock()
	delete(m.byTask, taskKey(sub, id))
	m.mu.Unlock()
}
