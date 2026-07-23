package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/spencerharmon/beehive/internal/editor"
)

// resolve-agent: the AI resolution surface for a NEEDS-HUMAN task. A resolution
// session is a GENERAL, tool-using agent working in a private worktree of the
// beehive coordination repo: the full opencode "build" toolset (read, grep,
// bash, edit, write) auto-approved, so it can investigate the repo, run
// read-only commands to understand the blocker, and make MULTI-FILE beehive-layer
// changes (ROI.md, INFRASTRUCTURE.md, ARTIFACTS.md, docs/, notes) to help clear
// it. Nothing it does is live until the operator clicks Publish (merge the
// worktree branch to the hive's main); Discard drops the worktree.
//
// edit-session-consolidation: this is NO LONGER a bespoke, in-memory-only session
// type. A resolution session is an editor.Session of Kind resolve (whole-tree +
// unrestricted + a caller-supplied blocker System prompt + no agent self-merge),
// so it rides the SAME durable worktree/opencode/transcript/publish/reclaim
// engine as the coordination-file editor and the bootstrap agent — it gains
// persistence across a beehived restart, transcript replay on resume, and the
// editor-session-wipe protection, none of which the old resolveSession had.
// resolveManager is now just a task-keyed facade that maps a blocked task
// (sub/id) to its one editor.Session and builds the blocker-seeded Spec.
//
// The agent can NEVER un-escalate the task itself: flipping NEEDS-HUMAN -> TODO
// is a separate deterministic operator action (humanResolveApply), so a model can
// never silently reopen a blocker or free-rewrite the multi-task PLAN.md.

var (
	errNothingToPub = errors.New("resolve-agent: no changes to publish")
	errResolveBusy  = errors.New("resolve-agent: a turn is already in progress")
)

// resolveManager maps each NEEDS-HUMAN task to its one resolution session over
// the shared editor.Manager. It owns no worktrees or sessions of its own — the
// editor.Manager does — so a restart's editor.Reload transparently recovers every
// resolution session, and find() re-associates it with its task by persisted Meta.
type resolveManager struct {
	editors *editor.Manager
	mu      sync.Mutex // serializes session() so two page loads can't double-open one task
}

// newResolveManager builds the facade over the given editor.Manager. A resolution
// turn's wedge protection is the editor client's IdleTimeout watchdog (wired in
// editor.NewManager), so a turn stuck on a hung tool call is cut and can never
// leave a session "working…" forever.
func newResolveManager(editors *editor.Manager) *resolveManager {
	return &resolveManager{editors: editors}
}

// find returns the live resolution session for a task, matched by its persisted
// Meta tags so it is found again after a restart recovers it from the store.
func (m *resolveManager) find(sub, id string) (*editor.Session, bool) {
	for _, s := range m.editors.List() {
		if s.Kind() != editor.KindResolve {
			continue
		}
		md := s.Meta()
		if md["sub"] == sub && md["task"] == id {
			return s, true
		}
	}
	return nil, false
}

// session returns the resolution agent for a task, opening one on first use over
// a fresh whole-tree worktree under a blocker-seeded system prompt and reusing it
// on later loads. m.mu is held across the whole find-or-open so two concurrent
// page loads cannot both miss and both cut a worktree (the loser would leak its
// worktree and its returned id would 404 on every panel poll).
func (m *resolveManager) session(ctx context.Context, sub string, it PlanItem) (*editor.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.find(sub, it.ID); ok {
		return s, nil
	}
	return m.editors.OpenSession(ctx, editor.Spec{
		WholeTree:    true,
		Unrestricted: true,
		Kind:         editor.KindResolve,
		System:       resolveSystemPrompt(sub, it),
		Preamble:     resolveFirstTurnPreamble,
		Slug:         "resolve-" + slugPath(sub+"-"+it.ID),
		Meta:         map[string]string{"sub": sub, "task": it.ID},
	})
}

// get returns a live resolution session by id (rejecting a non-resolve session id).
func (m *resolveManager) get(id string) (*editor.Session, bool) {
	s, ok := m.editors.Get(id)
	if !ok || s.Kind() != editor.KindResolve {
		return nil, false
	}
	return s, true
}

// forget drops and tears down a task's resolution session (called after resolve,
// so a later re-escalation of the same id opens a fresh agent with no stale
// blocker context). The worktree/branch is removed via editor.Close; a Publish
// already landed its changes on main, so nothing worth keeping is lost.
func (m *resolveManager) forget(ctx context.Context, sub, id string) {
	if s, ok := m.find(sub, id); ok {
		_ = m.editors.Close(ctx, s.ID)
	}
}

// resolvePublish lands a resolution session's committed changes on the hive main
// via the editor's shared publish path (own-remote-or-local, resolved at open).
// It refuses when a turn is still writing the worktree or when there is nothing
// to publish, then notes the publish in the log. The caller MUST serialize this
// against other primary-checkout writers (Server.gitMu).
func resolvePublish(ctx context.Context, s *editor.Session) error {
	if s.Busy() {
		return errResolveBusy
	}
	if s.State(ctx) != "dirty" {
		return errNothingToPub
	}
	if err := s.Merge(ctx); err != nil {
		return err
	}
	s.Note("Published the change to main.")
	return nil
}

// slugPath turns a repo path (or a sub-id key) into a branch-safe slug.
func slugPath(p string) string {
	r := strings.NewReplacer("/", "-", ".", "-", " ", "-")
	return strings.Trim(r.Replace(p), "-")
}

// resolveFirstTurnPreamble orients the agent on its first turn: investigate the
// real state before proposing, and drive toward exactly one of the concrete
// clear-paths the system prompt enumerates.
const resolveFirstTurnPreamble = "Before proposing anything, use your tools to inspect the real state of the " +
	"blocker using ONLY files inside your working directory: read this target's ROI.md, PLAN.md and any change " +
	"docs under its submodules/<name>/docs/, and run read-only git commands in this worktree. Then tell me which " +
	"concrete clear-path resolves THIS blocker and take the part of it you can (editing coordination-layer files " +
	"here), leaving the operator only the action that must be theirs."

// resolveSystemPrompt seeds the resolution agent with the concrete blocker, its
// full tool authority and boundaries, and — crucially — the explicit set of ways a
// NEEDS-HUMAN task actually clears, so the agent drives the operator to a real
// unblock instead of dead-ending.
func resolveSystemPrompt(sub string, it PlanItem) string {
	reason := strings.TrimSpace(it.HumanReason)
	if reason == "" {
		reason = "(no explicit reason recorded on the task)"
	}
	desc := strings.TrimSpace(it.Desc)
	if desc == "" {
		desc = "(no description on the task)"
	}
	return fmt.Sprintf(`You are a general engineering agent helping a human operator CLEAR a task that an
autonomous coding swarm escalated to NEEDS-HUMAN. A NEEDS-HUMAN task is blocked
and is NOT worked by the swarm until the operator clears the blocker and reopens
it.

Blocked task: %[1]s   (submodule: %[2]s)
Summary: %[3]s
Why it is blocked (operator input required): %[4]s

YOUR ENVIRONMENT
You are working inside a private git worktree of the beehive COORDINATION repo
(the "hive" superproject), checked out at its root. You have a full toolset
(read, grep, bash, edit, write) and every tool action is auto-approved, so act:
investigate first, then make the coordination-layer changes that help clear the
blocker. Nothing you write becomes live until the operator clicks Publish (which
merges this worktree branch to the hive's main); Discard throws it away.

STAY INSIDE YOUR WORKING DIRECTORY
Only read, write, and run commands on paths INSIDE this worktree. Do NOT read or
write absolute paths outside it (for example another checkout elsewhere on the
host): an out-of-tree file access blocks on a permission prompt that nothing can
answer and will hang your turn. Note that submodules/%[2]s/repo/ is a submodule
gitlink and is EMPTY in this worktree - the target's application source is not
available here, and that is intentional (see below).

WHAT YOU MAY CHANGE
  - Beehive-layer coordination files for this target: its ROI.md (human-owned
    intent — propose careful edits), INFRASTRUCTURE.md and ARTIFACTS.md
    (operational / process docs — the right home for "document the process"), and
    change docs under submodules/%[2]s/docs/. You may create new files here.
  - Multiple files in one session — you are not limited to a single file.

WHAT YOU MUST NOT DO
  - Do NOT edit the target's application SOURCE CODE (anything under
    submodules/%[2]s/repo/). That is a separate git checkout; code changes go
    through a normal swarm WORK task, not this dialog. If the blocker needs code,
    describe precisely what a work task must implement and capture that intent in
    ROI.md so the swarm builds it after you unblock — do not tell the operator you
    "can't help".
  - Do NOT run destructive or irreversible commands, and do NOT touch external
    systems, clusters, or networks. Read-only inspection only.
  - Do NOT ask for, print, or write secret values. Credentials are provided
    out-of-band via the Secrets panel and never pass through this chat or a file.
  - Do NOT commit, merge, push, or flip the task status yourself — the operator
    drives Publish and "Mark resolved" through the UI.

HOW THIS BLOCKER GETS CLEARED (drive the operator to exactly one; state which)
  1. CREDENTIAL/secret needed -> the operator adds it in the Secrets panel. Tell
     them the exact key name and value format expected; do not handle the value.
  2. DECISION, INTENT, or DOCUMENTATION needed -> you make the edit here
     (ROI.md / INFRASTRUCTURE.md / ARTIFACTS.md / docs), the operator reviews the
     diff and clicks Publish.
  3. CODE needed -> you describe the work task precisely and capture its intent in
     ROI.md (Publish), so the swarm implements it once the task is reopened.
  4. Then, in EVERY case, the operator clicks "Mark resolved -> TODO" to flip this
     task NEEDS-HUMAN -> TODO so the swarm re-selects it. You cannot do this; it is
     the deterministic operator action that actually unblocks the swarm.

Keep replies short. End each turn by naming which clear-path (1-4) applies to
THIS blocker, what you have done toward it, and the single next action the
operator must take.`,
		it.ID, sub, desc, reason)
}
