package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/swarm"
)

// resolve-agent: the AI resolution surface for a NEEDS-HUMAN task. Unlike the
// generic chat-diff editor (chatManager, a single-file propose-then-approve
// loop), a resolution session is a GENERAL, tool-using agent working in a private
// worktree of the beehive coordination repo. It has the full opencode "build"
// toolset (read, grep, bash, edit, write) auto-approved, so it can investigate
// the repo, run read-only commands to understand the blocker, and make
// MULTI-FILE beehive-layer changes (ROI.md, INFRASTRUCTURE.md, ARTIFACTS.md,
// docs/, notes) to help clear it. Nothing it does is live until the operator
// clicks Publish (merge the worktree branch to the hive's main); Discard drops
// the worktree.
//
// The agent can NEVER un-escalate the task itself: flipping NEEDS-HUMAN -> TODO
// is a separate deterministic operator action (humanResolveApply), so a model can
// never silently reopen a blocker or free-rewrite the multi-task PLAN.md. The
// resolution page therefore carries three orthogonal levers, matching the ways a
// blocker actually clears: the Secrets panel (a credential), this agent + Publish
// (a coordination-layer change), and Mark resolved (the deterministic reopen).

var (
	errResolveBusy    = errors.New("resolve-agent: a turn is already in progress")
	errNothingToPub   = errors.New("resolve-agent: no changes to publish")
	errResolveMissing = errors.New("resolve-agent: session not found")
)

// resolveManager owns the per-task resolution agent sessions and the worktrees
// backing them. Sessions are keyed by task (one general agent per blocked task,
// reused across page reloads) and live in-memory only.
type resolveManager struct {
	root        string
	git         *git.Repo // root repo (cuts the per-task worktrees)
	client      swarm.Client
	now         func() time.Time
	turnCeiling time.Duration // absolute per-turn wall-clock ceiling (0 = none)

	mu     sync.Mutex
	byTask map[string]*resolveSession // taskKey(sub,id) -> session
}

// newResolveManager builds a resolveManager over the beehive repo at root driving
// the given opencode client. turnCeiling bounds a single agent turn so a wedged
// tool call (e.g. an opencode permission elicitation that never resolves) can
// never leave a session "working…" forever; the client's own IdleTimeout progress
// watchdog is the finer-grained cut, this is the absolute backstop.
func newResolveManager(root string, client swarm.Client, turnCeiling time.Duration) *resolveManager {
	return &resolveManager{
		root:        root,
		git:         git.New(root),
		client:      client,
		now:         time.Now,
		turnCeiling: turnCeiling,
		byTask:      map[string]*resolveSession{},
	}
}

// resolveSession is one blocked task's resolution agent: a tool-using opencode
// session over a throwaway worktree branch off main. The opencode session is
// opened lazily on the first turn; each turn's file changes are committed to the
// branch so the diff/publish reflect exactly what the agent produced.
type resolveSession struct {
	ID     string
	Branch string
	Sub    string
	TaskID string
	wtPath string
	sys    string
	mgr    *resolveManager
	wt     *git.Repo

	mu        sync.Mutex
	oc        swarm.Session      // lazy: opened on first turn
	cancel    context.CancelFunc // cancels the in-flight turn's context (teardown)
	log       []chatTurn
	busy      bool
	errMsg    string
	published bool
	first     bool // first turn seeds an orientation preamble

	// wtMu serializes ALL git operations against this session's worktree (the
	// post-turn commit, diff/hasChanges reads, publish, teardown). s.mu guards the
	// in-memory session state; wtMu guards the on-disk worktree/index so a panel
	// poll or Publish can never race the background turn's `git add -A` commit on
	// index.lock. Lock order when both are held is wtMu THEN s.mu.
	wtMu sync.Mutex
}

// taskKey is the per-task session key; sub and id are validated basenames.
func taskKey(sub, id string) string { return sub + "/" + id }

// session returns the resolution agent for a task, opening one on first use over
// a fresh worktree under a blocker-seeded system prompt and reusing it on later
// loads. A session whose worktree was reclaimed out from under it is transparently
// reopened.
func (m *resolveManager) session(ctx context.Context, sub string, it PlanItem) (*resolveSession, error) {
	key := taskKey(sub, it.ID)
	// Hold m.mu across the whole check-create-insert so two concurrent page loads
	// cannot both miss and both cut a worktree (the loser would leak its worktree
	// and its returned SessID would 404 on every panel poll). WorktreeAdd runs
	// under the lock; session creation is infrequent, so the serialization is
	// cheap relative to the orphan-worktree bug it prevents.
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.byTask[key]; ok {
		return s, nil
	}
	branch := "edit-resolve-" + slugPath(sub+"-"+it.ID) + "-" + fmt.Sprint(m.now().UnixNano())
	wtPath := m.root + "/.worktrees/" + branch
	if err := m.git.WorktreeAdd(ctx, wtPath, branch, "main"); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}
	s := &resolveSession{
		ID:     branch,
		Branch: branch,
		Sub:    sub,
		TaskID: it.ID,
		wtPath: wtPath,
		sys:    resolveSystemPrompt(sub, it),
		mgr:    m,
		wt:     git.New(wtPath),
		first:  true,
	}
	m.byTask[key] = s
	return s, nil
}

// get returns a live session by id.
func (m *resolveManager) get(id string) (*resolveSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.byTask {
		if s.ID == id {
			return s, true
		}
	}
	return nil, false
}

// forget drops and tears down a task's resolution session (called after resolve,
// so a later re-escalation of the same id opens a fresh agent with no stale
// blocker context). The worktree/branch is removed; a Publish already landed its
// changes on main, so nothing worth keeping is lost.
func (m *resolveManager) forget(ctx context.Context, sub, id string) {
	key := taskKey(sub, id)
	m.mu.Lock()
	s, ok := m.byTask[key]
	delete(m.byTask, key)
	m.mu.Unlock()
	if ok {
		s.teardown(ctx)
	}
}

// startChat records the user message and runs the agent turn in the background,
// so the HTTP handler returns immediately and the panel poll renders progress and
// the resulting multi-file diff. One turn at a time per session.
func (s *resolveSession) startChat(bg context.Context, msg string) error {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return errResolveBusy
	}
	s.busy = true
	s.errMsg = ""
	s.log = append(s.log, chatTurn{Role: "user", Text: msg, At: s.mgr.now()})
	first := s.first
	ctx, cancel := context.WithCancel(bg)
	if s.mgr.turnCeiling > 0 {
		ctx, cancel = context.WithTimeout(bg, s.mgr.turnCeiling)
	}
	s.cancel = cancel
	s.mu.Unlock()

	go s.runTurn(ctx, msg, first)
	return nil
}

func (s *resolveSession) runTurn(ctx context.Context, msg string, first bool) {
	reply, err := s.prompt(ctx, msg, first)
	s.mu.Lock()
	if err != nil {
		s.errMsg = err.Error()
		s.busy = false
		s.cancel = nil
		s.mu.Unlock()
		return
	}
	// Turn succeeded: consume the one-shot orientation preamble now (not at
	// dispatch), so a turn that failed to reach the model still re-seeds it.
	s.first = false
	display := strings.TrimSpace(reply)
	if display == "" {
		display = "(no reply)"
	}
	s.log = append(s.log, chatTurn{Role: "agent", Text: display, At: s.mgr.now()})
	s.mu.Unlock()

	// Capture whatever the agent wrote this turn onto the branch so the diff and a
	// later Publish reflect it exactly (new files included). Nothing-to-commit is
	// normal (the agent only investigated or answered). Serialized on wtMu so a
	// concurrent panel diff/Publish never races the index.
	s.wtMu.Lock()
	cerr := s.wt.Commit(ctx, "resolve "+s.TaskID+": agent turn")
	s.wtMu.Unlock()
	s.mu.Lock()
	if cerr != nil && !errors.Is(cerr, git.ErrNothing) {
		s.errMsg = "commit agent changes: " + cerr.Error()
	} else if cerr == nil {
		// New content landed on the branch after a prior Publish: it is no longer
		// fully published, so the panel must offer Publish again.
		s.published = false
	}
	s.busy = false
	s.cancel = nil
	s.mu.Unlock()
}

// prompt opens the opencode session on first use (seeding the blocker system
// prompt) and reuses it for later turns. The first user turn is prefixed with a
// short orientation so the agent knows to investigate before proposing.
func (s *resolveSession) prompt(ctx context.Context, msg string, first bool) (string, error) {
	s.mu.Lock()
	oc := s.oc
	s.mu.Unlock()
	if oc == nil {
		sess, err := s.mgr.client.Open(ctx, s.wtPath, s.sys)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.oc = sess
		s.mu.Unlock()
		oc = sess
	}
	if first {
		msg = resolveFirstTurnPreamble + "\n\n" + msg
	}
	return oc.Prompt(ctx, msg)
}

func (s *resolveSession) setErr(msg string) {
	s.mu.Lock()
	s.errMsg = msg
	s.mu.Unlock()
}

// resolveFile is one changed file on the session branch: its repo-relative path
// and its before/after content (base = the main...HEAD merge-base blob, proposed
// = HEAD's blob). The panel renders each through the shared colorized DiffRow
// renderer, so the resolution diff reads like every other diff surface instead
// of a raw uncolored patch.
type resolveFile struct {
	Path          string
	Before, After string
}

// changedFiles returns the session branch's committed change against main as a
// --stat summary plus one resolveFile per changed path. It reconstructs each
// side's content that the raw `git diff main...HEAD` patch is derived from: the
// base is the main...HEAD MERGE-BASE (so a main that advanced under the session
// still shows only the agent's own change, matching the three-dot patch) and the
// proposed side is HEAD. A side absent at its ref (an add has no base blob, a
// delete no HEAD blob) reads as "" — a whole-file add/delete the renderer
// tolerates. files is nil when the agent has committed no change yet. Serialized
// on wtMu against the turn commit, exactly as the raw diff was.
func (s *resolveSession) changedFiles(ctx context.Context) (stat string, files []resolveFile, err error) {
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	stat, err = s.wt.Run(ctx, "diff", "--stat", "main...HEAD")
	if err != nil {
		return "", nil, err
	}
	names, err := s.wt.Run(ctx, "diff", "--name-only", "main...HEAD")
	if err != nil {
		return "", nil, err
	}
	base, err := s.wt.Run(ctx, "merge-base", "main", "HEAD")
	if err != nil {
		return "", nil, err
	}
	base = strings.TrimSpace(base)
	for _, p := range strings.Split(strings.TrimSpace(names), "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		before, _ := s.wt.Show(ctx, base, p)  // "" when p is added (absent in base)
		after, _ := s.wt.Show(ctx, "HEAD", p) // "" when p is deleted (absent in HEAD)
		files = append(files, resolveFile{Path: p, Before: before, After: after})
	}
	return strings.TrimSpace(stat), files, nil
}

// hasChanges reports whether the branch carries any committed change over main.
// Callers hold wtMu (publish) or accept a best-effort read.
func (s *resolveSession) hasChanges(ctx context.Context) (bool, error) {
	out, err := s.wt.Run(ctx, "diff", "--name-only", "main...HEAD")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// publish lands the session's committed changes on the hive's main, making the
// coordination-layer change live. It first captures any residual working-tree
// change, refuses when there is nothing to publish, then publishes to the repo's
// own remote (when it has one that shares history) or, for a local-only hive, to
// the checked-out main via updateInstead. The caller MUST serialize this against
// other primary-checkout writers (Server.gitMu).
func (s *resolveSession) publish(ctx context.Context, remote string) error {
	// Never publish while a turn is still writing the worktree: the branch tip may
	// be a half-written tree. The operator retries once the turn settles.
	if s.isBusy() {
		return errResolveBusy
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	if cerr := s.wt.Commit(ctx, "resolve "+s.TaskID+": publish"); cerr != nil && !errors.Is(cerr, git.ErrNothing) {
		return cerr
	}
	has, err := s.hasChanges(ctx)
	if err != nil {
		return err
	}
	if !has {
		return errNothingToPub
	}
	if err := s.wt.PublishToMain(ctx, remote); err != nil {
		return err
	}
	if remote != "" {
		// Sync the local projection so beehived's own views reflect it at once; a
		// dirty primary tree only downgrades this to a soft note (remote has it).
		if uerr := s.wt.UpdateLocalMain(ctx); uerr != nil {
			s.setErr("published to remote main; local tree not updated: " + uerr.Error())
		}
	}
	s.mu.Lock()
	s.published = true
	s.log = append(s.log, chatTurn{Role: "system", Text: "Published the change to main.", At: s.mgr.now()})
	s.mu.Unlock()
	return nil
}

// publishRemote returns the remote to publish to: the repo's OWN remote (one
// whose main shares history with local main) when present, else "" for a
// local-only hive that publishes to its checked-out main. Mirrors the editor's
// trusted-remote rule so a foreign origin/main is never a publish target.
func (m *resolveManager) publishRemote(ctx context.Context) (string, error) {
	remote, err := m.git.Remote(ctx)
	if err != nil || remote == "" {
		return "", err
	}
	if err := m.git.Fetch(ctx, remote, "main"); err != nil {
		return "", err
	}
	own, err := m.git.SharesHistory(ctx, "main", remote+"/main")
	if err != nil {
		return "", err
	}
	if own {
		return remote, nil
	}
	return "", nil
}

// teardown removes the session's worktree and branch. It first CANCELS any
// in-flight turn (so the background goroutine's prompt/commit unwinds) and then
// takes wtMu, so the worktree is never removed out from under a live `git add
// -A`. Best-effort: a failure to remove a clean, abandoned edit worktree is
// reclaimed by the editor's startup GC (the edit- prefix is shared), so teardown
// never blocks the caller.
func (s *resolveSession) teardown(ctx context.Context) {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.oc != nil {
		_ = s.oc.Close()
		s.oc = nil
	}
	s.mu.Unlock()
	// Serialize against any commit still draining in the cancelled turn.
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	_ = s.mgr.git.WorktreeRemove(ctx, s.wtPath)
	_, _ = s.mgr.git.Run(ctx, "branch", "-D", s.Branch)
}

func (s *resolveSession) isBusy() bool    { s.mu.Lock(); defer s.mu.Unlock(); return s.busy }
func (s *resolveSession) errText() string { s.mu.Lock(); defer s.mu.Unlock(); return s.errMsg }
func (s *resolveSession) isPublished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}

func (s *resolveSession) logCopy() []chatTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]chatTurn, len(s.log))
	copy(out, s.log)
	return out
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
// full tool authority and boundaries, and \u2014 crucially \u2014 the explicit set of ways a
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
    intent \u2014 propose careful edits), INFRASTRUCTURE.md and ARTIFACTS.md
    (operational / process docs \u2014 the right home for "document the process"), and
    change docs under submodules/%[2]s/docs/. You may create new files here.
  - Multiple files in one session \u2014 you are not limited to a single file.

WHAT YOU MUST NOT DO
  - Do NOT edit the target's application SOURCE CODE (anything under
    submodules/%[2]s/repo/). That is a separate git checkout; code changes go
    through a normal swarm WORK task, not this dialog. If the blocker needs code,
    describe precisely what a work task must implement and capture that intent in
    ROI.md so the swarm builds it after you unblock \u2014 do not tell the operator you
    "can't help".
  - Do NOT run destructive or irreversible commands, and do NOT touch external
    systems, clusters, or networks. Read-only inspection only.
  - Do NOT ask for, print, or write secret values. Credentials are provided
    out-of-band via the Secrets panel and never pass through this chat or a file.
  - Do NOT commit, merge, push, or flip the task status yourself \u2014 the operator
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
