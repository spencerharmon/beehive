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

// humanManager maps each NEEDS-HUMAN task's resolution surface to its (reused)
// chat session. The chat sessions themselves live in the shared chatManager (so
// the /edit routes serve them); this only remembers which chat session backs a
// given (task, target-file) pair, so re-opening the same page — or retargeting
// the AI chat at a different beehive-layer file — reuses the matching
// conversation instead of cutting a fresh worktree each load. Keying on the
// target path (not the task alone) is what makes ?path= retargeting real: each
// file the operator points the chat at gets its own remembered session.
type humanManager struct {
	chat *chatManager

	mu        sync.Mutex
	bySession map[string]string // sessionKey(sub,id,path) -> chat session ID
}

func newHumanManager(chat *chatManager) *humanManager {
	return &humanManager{chat: chat, bySession: map[string]string{}}
}

// taskPrefix is the shared prefix of every session key for a task; forget uses
// it to drop all of a task's per-file sessions at once. sub and id are already
// validated basenames, and the NUL separator cannot appear in a path.
func taskPrefix(sub, id string) string { return sub + "/" + id + "\x00" }

// sessionKey identifies a resolution chat by task AND target file, so pointing
// the chat at a different beehive-layer file opens (and remembers) a distinct
// conversation rather than silently reusing the first file's session.
func sessionKey(sub, id, path string) string { return taskPrefix(sub, id) + path }

// session returns the resolution chat session for a (task, target-file) pair,
// opening one on first use over targetPath under a blocker-seeded system prompt
// and reusing it (by id) on later loads. A remembered id whose session has since
// been reclaimed (chat.get miss) is transparently reopened.
func (m *humanManager) session(ctx context.Context, sub string, it PlanItem, targetPath string) (*chatSession, error) {
	clean, err := cleanRepoPath(targetPath)
	if err != nil {
		return nil, err
	}
	key := sessionKey(sub, it.ID, clean)
	m.mu.Lock()
	id, ok := m.bySession[key]
	m.mu.Unlock()
	if ok {
		if sess, live := m.chat.get(id); live {
			return sess, nil
		}
	}
	sys := humanResolveSystemPrompt(sub, it, clean)
	sess, err := m.chat.openWith(ctx, clean, sys)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.bySession[key] = sess.ID
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
it, then confirm when the blocker is cleared. Be concrete and specific to THIS
blocker.

WHAT YOU CAN DO IN THIS UI (and its limits — state them accurately, never claim
you are locked to one file or that you have no way to help):
  - Propose an edit to the beehive-layer file currently targeted by this chat:
    %[5]s. The operator applies it only after approving the diff.
  - The operator can RETARGET this chat at any other beehive-layer file (via the
    "AI chat target" selector on the resolution page) — e.g. this submodule's
    INFRASTRUCTURE.md or ARTIFACTS.md to DOCUMENT a process or operational rule,
    or ROI.md (human-owned intent) to clarify what the target should do. If the
    resolution needs a doc written, tell the operator which beehive-layer file to
    retarget the chat at, then propose the documentation there.
  - The operator can add a credential/token via the Secrets panel. Secret values
    are encrypted and are NEVER pasted into this chat, into a transcript, or into
    a file. If the blocker is a secret, explain precisely what to store and in
    what format — do NOT propose a file edit and do NOT ask for the value here.
  - The operator can click "Mark resolved" to flip this task back to TODO so the
    swarm resumes.

WHAT YOU CANNOT DO HERE (do NOT pretend otherwise, and route these correctly):
  - You have NO tools: you cannot run commands, scripts, git, kubectl, or touch
    any external system or cluster. Never claim to have run something.
  - You cannot edit the submodule's own SOURCE CODE (files under
    submodules/%[2]s/repo/). Application code — including new scripts that belong
    in the target's repository — is written by a normal swarm WORK task, not this
    dialog. If the blocker really needs code, say so, and describe what the work
    task should implement so the operator can capture it in ROI.md / let the swarm
    build it after you unblock. Do not tell the operator you simply "can't help."
  - Actions that must run out-of-band on the operator's own host (e.g.
    materializing a live secret into a cluster) are the operator's to run; give
    exact, copy-pasteable steps and confirm the expected result, but never route
    a raw secret through this chat.

When a change to %[5]s (or a retargeted beehive-layer file) is what resolves the
blocker, propose it in full. Otherwise explain precisely what the operator must
provide, in what format, and how to confirm it worked.

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
		"Sub":     sm.Name,
		"Item":    it,
		"ChatID":  sess.ID,
		"Path":    sess.Path,
		"Targets": humanTargetOptions(sm.Name, sess.Path),
	})
}

// humanTargetOption is one entry in the resolution page's "AI chat target"
// selector: a beehive-layer file the operator can point the AI chat at, its
// human label, and whether it is the file currently targeted.
type humanTargetOption struct {
	Path    string
	Label   string
	Current bool
}

// humanTargetOptions lists the beehive-layer files the resolution chat can be
// retargeted at for a submodule, marking the one currently loaded. These are the
// operator-editable coordination files (never the submodule's repo/ code): ROI.md
// (human-owned intent), INFRASTRUCTURE.md and ARTIFACTS.md (per-target operational
// docs — the right home for "document the process" resolutions).
func humanTargetOptions(sub, current string) []humanTargetOption {
	base := "submodules/" + sub + "/"
	defs := []struct{ file, label string }{
		{repo.ROIFile, "Intent (ROI.md)"},
		{repo.InfraFile, "Infrastructure notes (INFRASTRUCTURE.md)"},
		{repo.Artifacts, "Artifacts / process docs (ARTIFACTS.md)"},
	}
	opts := make([]humanTargetOption, 0, len(defs))
	for _, d := range defs {
		p := base + d.file
		opts = append(opts, humanTargetOption{Path: p, Label: d.label, Current: p == current})
	}
	return opts
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

// forget drops every remembered resolution session for a task — across all target
// files the operator retargeted the chat at (called after resolve so a later
// NEEDS-HUMAN re-escalation of the same id opens fresh conversations, none
// carrying the stale blocker context).
func (m *humanManager) forget(sub, id string) {
	prefix := taskPrefix(sub, id)
	m.mu.Lock()
	for k := range m.bySession {
		if strings.HasPrefix(k, prefix) {
			delete(m.bySession, k)
		}
	}
	m.mu.Unlock()
}
