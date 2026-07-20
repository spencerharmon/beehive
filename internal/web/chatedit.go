package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/swarm"
)

// chat-diff-editor-core: a generic chat-diff surface over ANY repo file. Unlike
// the single-file editor (internal/editor, restricted to the beehive
// coordination-file allowlist), a chat-edit session opens an opencode session in
// a per-edit worktree of the beehive ROOT repo and drives a propose-then-apply
// loop: the agent replies with the COMPLETE proposed file contents wrapped in
// markers, beehived holds that proposal in memory and renders it as a unified
// diff against the worktree's current state, and NOTHING is written or committed
// until the human approves. Approve writes the proposal and commits it in the
// edit worktree; nothing here publishes that commit to main.
//
// ai-edit-publish-to-main retired this as a GENERAL edit-with-AI surface: every
// real edit-with-AI link (dashboard/explorer/roi_editor) carries a path and
// reaches internal/editor's publish-capable Manager instead (editEntry, in
// web.go), whose Merge lands an approved change on main — this manager's
// approve-and-stop had no such step, so a path-carrying request would silently
// dead-end on a throwaway branch main never advances to. What remains here is
// ONE consumer: the bootstrap wizard's singleton session over the fixed path
// LOCALS.md (bootstrap.go's openBootstrap, surfaced only via GET /bootstrap),
// which is deliberately a slow, conversational draft-then-approve loop an
// operator drives to completion before anything needs to land on main. Its
// chat/approve/reject panel fragments (/edit/{id}/panel|message|approve|reject)
// stay wired for that one caller; the GENERIC per-path HTTP entry (a POST /edit
// open and the /edit/{id} full-page view) is gone — see web.go's editEntry.

// proposeOpen/proposeClose bracket the full proposed file contents in the agent's
// reply. They are deliberately unlikely to appear in real file content; a reply
// without both markers carries no proposal (a plain answer or clarifying
// question), so the human is never shown a spurious diff.
const (
	proposeOpen  = "<<<BEEHIVE-PROPOSE>>>"
	proposeClose = "<<<BEEHIVE-END>>>"
)

var (
	errBusy       = errors.New("chat-edit: a turn is already in progress")
	errNoProposal = errors.New("chat-edit: no pending proposal to approve")
)

// chatTurn is one message in a session's log ("user", "agent", or "system").
// Parts is an agent turn's reasoning/tool-call breakdown (chat-editor-snappy-
// polish: "show ALL agent output... each expandable inline, with accurate live
// status") — nil for a user/system turn, and best-effort nil for an agent turn
// when the final Messages() fetch failed (the turn's Text still renders).
type chatTurn struct {
	Role  string
	Text  string
	At    time.Time
	Parts []swarm.Part
}

// BodyHTML renders the turn's message text through the shared markdown→HTML path
// (renderMarkdown, the same sanitized renderer the doc/ROI/transcript view panes
// use) so the chat pane honors the authored formatting instead of collapsing it:
// newlines, whitespace, and markdown/code structure survive as real HTML block
// structure rather than being squashed into a single run of text by HTML
// whitespace folding. Empty text renders nothing. Sanitization is inherited
// wholesale from renderMarkdown (no WithUnsafe), so a message can never inject
// live markup.
func (t chatTurn) BodyHTML() template.HTML {
	if t.Text == "" {
		return ""
	}
	return renderMarkdown(t.Text)
}

// sessionState is the coarse connection/turn lifecycle the panel surfaces so
// the human is never stranded on a bare spinner (chat-editor-snappy-polish):
// connecting (worktree ready, the opencode session is being created),
// connected (idle, ready for input), working (a turn is in flight), or error
// (the connect attempt failed). Purely derived from existing session state
// (oc/connErr/busy) — never stored redundantly, so it can never drift from it.
type sessionState string

const (
	stateConnecting sessionState = "connecting"
	stateConnected  sessionState = "connected"
	stateWorking    sessionState = "working"
	stateConnError  sessionState = "error"
)

// connLabel is the human-readable label for a sessionState, shown alongside its
// CSS state class in the chat pane.
func connLabel(st sessionState) string {
	switch st {
	case stateConnecting:
		return "Connecting…"
	case stateConnected:
		return "Connected"
	case stateWorking:
		return "Working…"
	case stateConnError:
		return "Connection error"
	default:
		return ""
	}
}

// chatManager owns the active chat-edit sessions and the per-edit worktrees
// backing them. Sessions are in-memory only (no persistence in the core task;
// editor-session-persist owns durable recovery — kept a separate seam here).
type chatManager struct {
	root    string
	absRoot string
	git     *git.Repo // root repo (cuts the per-edit worktrees)
	client  swarm.Client
	now     func() time.Time

	mu   sync.Mutex
	byID map[string]*chatSession

	// bootstrapID + bootstrapMu back the singleton bootstrap agent
	// (bootstrap-agent-autodetect): the unbootstrapped-repo dashboard lazily
	// opens ONE chat-edit session over LOCALS.md and reuses it across re-opens
	// rather than cutting a fresh worktree each render. bootstrapMu serializes the
	// check-or-open so concurrent dashboard loads converge on one session.
	bootstrapMu sync.Mutex
	bootstrapID string
}

// newChatManager builds a chatManager over the beehive repo at root, driving the
// given opencode client. filepath.Abs only fails when the working directory is
// unreadable (never in practice); falling back to root keeps New total.
func newChatManager(root string, client swarm.Client) *chatManager {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &chatManager{
		root:    root,
		absRoot: abs,
		git:     git.New(root),
		client:  client,
		now:     time.Now,
		byID:    map[string]*chatSession{},
	}
}

// chatSession is one propose-then-apply edit of a single repo file on its own
// throwaway worktree branch. The opencode session connects EAGERLY in the
// background as soon as the session is opened (chat-editor-snappy-polish: so
// the panel can show "connecting" -> "connected" before the human ever sends a
// message, instead of only discovering the connect cost inline on the first
// turn); ensureConnected is idempotent, so a chat turn that arrives before the
// background attempt finishes just waits on the SAME attempt rather than racing
// a second one. The pending proposal lives only in memory until approved.
type chatSession struct {
	ID     string
	Path   string // repo-relative, slash form (any file, e.g. submodules/x/notes.md)
	Branch string
	wtPath string // absolute worktree path
	sys    string // opencode system prompt
	mgr    *chatManager
	wt     *git.Repo // the per-edit worktree

	// connMu serializes connect attempts (the eager background one from openWith
	// and any turn that arrives before it finishes) so opencode's session-open
	// endpoint is called AT MOST ONCE per success; a failed attempt is retried by
	// the next caller (busy-free: connMu is only ever held for the duration of one
	// Open() call, never across a whole turn).
	connMu sync.Mutex

	mu       sync.Mutex
	oc       swarm.Session // set once ensureConnected succeeds
	connErr  error         // set when the last connect attempt failed
	seeded   bool          // true once the first user turn has been sent (seedPrompt)
	log      []chatTurn
	proposal *string // pending full-file proposal, nil when none
	busy     bool
	errMsg   string
}

// open validates rawPath, cuts a fresh worktree/branch off local main, and
// registers a session. The target need not exist yet (a genuine new-file
// creation is allowed); only traversal/absolute/.git paths are rejected.
func (m *chatManager) open(ctx context.Context, rawPath string) (*chatSession, error) {
	clean, err := cleanRepoPath(rawPath)
	if err != nil {
		return nil, err
	}
	return m.openWith(ctx, clean, chatSystemPrompt(clean))
}

// openWith cuts a fresh worktree/branch off local main and registers a session
// for clean (an ALREADY-validated repo-relative path) under the caller-supplied
// system prompt. It is the shared core of open (the generic per-file editor, with
// chatSystemPrompt) and openBootstrap (the setup agent, with a bootstrap-seeded
// prompt); callers that take untrusted input must run cleanRepoPath first.
func (m *chatManager) openWith(ctx context.Context, clean, sys string) (*chatSession, error) {
	branch := "edit-" + slugPath(clean) + "-" + fmt.Sprint(m.now().Unix())
	wtPath := filepath.Join(m.absRoot, ".worktrees", branch)
	if err := m.git.WorktreeAdd(ctx, wtPath, branch, "main"); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}
	s := &chatSession{
		ID:     branch,
		Path:   clean,
		Branch: branch,
		wtPath: wtPath,
		sys:    sys,
		mgr:    m,
		wt:     git.New(wtPath),
	}
	m.mu.Lock()
	m.byID[s.ID] = s
	m.mu.Unlock()
	// Eager background connect (chat-editor-snappy-polish): open the opencode
	// session now, detached from this request, so a human who has not typed
	// anything yet already sees "connecting" give way to "connected" instead of
	// only paying that cost inline on the first message. Best-effort: a failure
	// here just leaves connErr set (state -> error) and self-heals the same way
	// a lazy failure always has — the next chat turn retries via ensureConnected.
	go func() { _ = s.ensureConnected(context.Background()) }()
	return s, nil
}

// get returns a registered session.
func (m *chatManager) get(id string) (*chatSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	return s, ok
}

// chat runs one turn synchronously: it records the user message, awaits the
// assistant turn (opencode-turn-poll, via swarm.Session.Prompt), parses any
// proposal out of the reply, and records the agent message. Holding the turn
// synchronous means the panel that renders after this call already carries the
// proposed diff — the human never sees a half-finished turn. One turn at a time.
// The blocking counterpart of startChat; both share runTurn.
func (s *chatSession) chat(ctx context.Context, msg string) error {
	if !s.beginTurn(msg) {
		return errBusy
	}
	return s.runTurn(ctx, msg)
}

// startChat is chat's ASYNC counterpart for the HTTP path (chat-editor-snappy-
// polish: never strand the user on a bare spinner). It records the user's
// message and returns immediately — so it renders before the agent replies —
// and runs the turn in the background; the panel poll shows Busy/liveSteps
// progress and the final reply once runTurn finishes. bg should be a
// request-independent context (the turn outlives the HTTP request that started
// it), mirroring resolveSession.startChat / editor.Session.StartChat.
func (s *chatSession) startChat(bg context.Context, msg string) error {
	if !s.beginTurn(msg) {
		return errBusy
	}
	go func() { _ = s.runTurn(bg, msg) }()
	return nil
}

// beginTurn claims the busy flag and records the user's message; false means a
// turn is already in progress (the caller must not start another). Shared by
// chat and startChat so the user's message is visible in the log the instant
// either returns/dispatches, before the agent has replied.
func (s *chatSession) beginTurn(msg string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	s.busy = true
	s.errMsg = ""
	s.log = append(s.log, chatTurn{Role: "user", Text: msg, At: s.mgr.now()})
	return true
}

// runTurn drives one agent turn (the caller already recorded the user message
// via beginTurn) and records the outcome: an error turn sets errMsg; a
// successful one parses any proposal, and best-effort attaches the turn's
// reasoning/tool-call breakdown (lastAssistantSteps) so the log entry stays
// expandable/inspectable after the fact, not just while live.
func (s *chatSession) runTurn(ctx context.Context, msg string) error {
	reply, err := s.prompt(ctx, msg)
	var steps []swarm.Part
	if err == nil {
		steps = s.lastAssistantSteps(ctx) // best-effort: nil on any failure
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = false
	if err != nil {
		s.errMsg = err.Error()
		return err
	}
	display, proposed, ok := extractProposal(reply)
	if ok {
		if strings.TrimSpace(display) == "" {
			display = "Proposed a change."
		}
		p := proposed
		s.proposal = &p
	}
	s.log = append(s.log, chatTurn{Role: "agent", Text: display, At: s.mgr.now(), Parts: steps})
	return nil
}

// ensureConnected opens the underlying opencode session if it is not already
// open, retrying a prior failed attempt (the exact self-healing retry-on-next-
// call semantics the original lazy-open had). connMu serializes it so the
// eager background attempt (openWith) and a real turn's call never race a
// double Open(): whichever arrives first performs the ONE attempt; the other
// blocks on connMu and then observes its outcome (oc set, or connErr set).
func (s *chatSession) ensureConnected(ctx context.Context) error {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.mu.Lock()
	oc := s.oc
	s.mu.Unlock()
	if oc != nil {
		return nil
	}
	sess, err := s.mgr.client.Open(ctx, s.wtPath, s.sys)
	s.mu.Lock()
	if err != nil {
		s.connErr = err
	} else {
		s.oc = sess
		s.connErr = nil
	}
	s.mu.Unlock()
	return err
}

// connState derives the panel's coarse lifecycle badge from existing session
// state: never stored redundantly, so it can never drift from the fields it
// reads. error takes priority (a stale connErr from a prior failed attempt
// still needs the human's attention even if, oddly, busy got set some other
// way), then working, then connecting-vs-connected on whether oc is set.
func (s *chatSession) connState() sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.connErr != nil:
		return stateConnError
	case s.busy:
		return stateWorking
	case s.oc == nil:
		return stateConnecting
	default:
		return stateConnected
	}
}

// prompt ensures the opencode session is connected (seeding the system prompt)
// and, on the SESSION's first user turn only, prefixes the current file
// contents so the agent can return a full proposed file; later turns send msg
// as-is. "first" is tracked explicitly (not inferred from oc's presence, which
// eager-connect can now set before any message is ever sent) via s.seeded.
// Never called under s.mu.
func (s *chatSession) prompt(ctx context.Context, msg string) (string, error) {
	if err := s.ensureConnected(ctx); err != nil {
		return "", err
	}
	s.mu.Lock()
	oc := s.oc
	first := !s.seeded
	s.seeded = true
	s.mu.Unlock()
	if !first {
		return oc.Prompt(ctx, msg)
	}
	base, _ := s.base(ctx)
	return oc.Prompt(ctx, seedPrompt(s.Path, base, msg))
}

// agentSteps filters a message's parts to the human-visible reasoning/tool-call
// breakdown (chat-editor-snappy-polish): the "text" part duplicates the turn's
// own Text field and step-start/step-finish are bookkeeping markers, so neither
// is worth its own expandable entry — only "reasoning" and "tool" are.
func agentSteps(parts []swarm.Part) []swarm.Part {
	var out []swarm.Part
	for _, p := range parts {
		switch p.Type {
		case "reasoning", "tool":
			out = append(out, p)
		}
	}
	return out
}

// lastAssistantSteps polls the opencode session's full transcript and returns
// the most recent assistant message's reasoning/tool-call breakdown. It is the
// ONLY mechanism behind "live status throughout": no SSE plumbing crosses the
// swarm/web package boundary, so it just re-fetches Messages (the same public,
// already-polled API the recorder itself uses) — called both while a turn is
// still in flight (the panel poll, so tool calls/reasoning appear as they
// happen) and once more right after a turn completes (runTurn, so the entry
// persists in the log for later review, not just live). Best-effort: nil on any
// error, a nil session, or an empty/non-assistant transcript.
func (s *chatSession) lastAssistantSteps(ctx context.Context) []swarm.Part {
	s.mu.Lock()
	oc := s.oc
	s.mu.Unlock()
	if oc == nil {
		return nil
	}
	msgs, err := oc.Messages(ctx)
	if err != nil || len(msgs) == 0 {
		return nil
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		return nil
	}
	return agentSteps(last.Parts)
}

// base is the file's current content in the edit worktree (git show HEAD:path).
// It is "" when the file does not exist at HEAD (a new-file edit). HEAD advances
// as approvals commit, so the baseline is always the last-approved content —
// successive turns diff against what is really there, not the original.
func (s *chatSession) base(ctx context.Context) (string, error) {
	out, err := s.wt.Show(ctx, "HEAD", s.Path)
	if err != nil {
		return "", nil // absent at HEAD: a new file
	}
	return out, nil
}

// pending returns the in-memory proposal and whether one exists.
func (s *chatSession) pending() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proposal == nil {
		return "", false
	}
	return *s.proposal, true
}

// approve writes the pending proposal to the worktree and commits it there. It is
// the ONLY path that mutates the filesystem/git for a session, so no change lands
// without this explicit human action. The commit stays on the edit branch (never
// published to main by the core task). Nothing-to-commit is tolerated (the agent
// may have re-proposed byte-identical content).
func (s *chatSession) approve(ctx context.Context) error {
	s.mu.Lock()
	if s.proposal == nil {
		s.mu.Unlock()
		return errNoProposal
	}
	content := *s.proposal
	s.mu.Unlock()

	abs := filepath.Join(s.wtPath, filepath.FromSlash(s.Path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return err
	}
	if err := s.wt.CommitPaths(ctx, "chat-edit: "+s.Path, s.Path); err != nil && err != git.ErrNothing {
		return err
	}
	s.mu.Lock()
	s.proposal = nil
	s.log = append(s.log, chatTurn{Role: "system", Text: "Applied and committed the proposed change.", At: s.mgr.now()})
	s.mu.Unlock()
	return nil
}

// reject drops the pending proposal. It is a pure no-op against the worktree and
// git: nothing was ever written, so there is nothing to undo.
func (s *chatSession) reject() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proposal == nil {
		return
	}
	s.proposal = nil
	s.log = append(s.log, chatTurn{Role: "system", Text: "Rejected the proposed change.", At: s.mgr.now()})
}

func (s *chatSession) isBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *chatSession) errText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errMsg
}

// connErrText is the last connect attempt's failure text, or "" once connected
// (or before any attempt). Panel data falls back to it when there is no more
// specific turn error, so an "error" conn-state is never a mute badge.
func (s *chatSession) connErrText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connErr == nil {
		return ""
	}
	return s.connErr.Error()
}

func (s *chatSession) logCopy() []chatTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]chatTurn, len(s.log))
	copy(out, s.log)
	return out
}

// cleanRepoPath normalizes a repo-relative path and rejects anything that could
// escape the repo (absolute, traversal) or reach into git metadata. It does NOT
// require the file to exist — a new-file proposal is legitimate.
func cleanRepoPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("chat-edit: empty path")
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("chat-edit: invalid path %q", raw)
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", fmt.Errorf("chat-edit: refusing to edit git metadata %q", raw)
	}
	return clean, nil
}

// slugPath turns a repo path into a branch-safe slug (edit-<slug>-<unix>). The
// edit- prefix matches the single-file editor's, so its startup GC recognizes and
// reclaims a clean, abandoned chat-edit worktree just like its own.
func slugPath(p string) string {
	r := strings.NewReplacer("/", "-", ".", "-", " ", "-")
	return strings.Trim(r.Replace(p), "-")
}

// extractProposal pulls a full-file proposal out of an agent reply. A proposal is
// the text between proposeOpen and proposeClose; the human-readable message is
// everything before proposeOpen. When both markers are not present the reply is a
// plain answer/question and (ok=false) no proposal is offered. The body is
// normalized to CRLF->LF, one stripped leading newline (the line break after the
// open marker), and exactly one trailing newline (POSIX text file), or empty.
func extractProposal(reply string) (msg, proposed string, ok bool) {
	i := strings.Index(reply, proposeOpen)
	if i < 0 {
		return strings.TrimSpace(reply), "", false
	}
	rest := reply[i+len(proposeOpen):]
	j := strings.Index(rest, proposeClose)
	if j < 0 {
		return strings.TrimSpace(reply), "", false
	}
	msg = strings.TrimSpace(reply[:i])
	return msg, normalizeProposal(rest[:j]), true
}

// normalizeProposal cleans the raw bytes between the markers into file content.
func normalizeProposal(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return ""
	}
	return body + "\n"
}

// chatSystemPrompt instructs the agent to propose (never apply) a full-file
// change wrapped in the markers. The agent has no tool authority in this loop —
// beehived applies the proposal only on human approval — so the system prompt is
// explicit that it must not try to edit or run anything. It also seeds the
// file-specific editing rules (resolveFileContext, chat-diff-file-context) so a
// PLAN.md edit keeps its line format, ROI.md is treated as human-owned intent,
// etc. — the SAME context whether the session was opened from a per-file edit
// link or the generic edit window.
func chatSystemPrompt(path string) string {
	return fmt.Sprintf(`You are a collaborative editor for ONE file in a git repository: %[1]s.

%[4]s

You do NOT have permission to modify files, run git, or use any tools. The system
applies your proposal to %[1]s ONLY after the human approves it.

When you want to change %[1]s, reply with:
1. ONE short sentence describing the change.
2. Then the COMPLETE new contents of %[1]s wrapped EXACTLY between a line
   containing %[2]s and a line containing %[3]s. Include the ENTIRE file, not a
   diff or a fragment, and do not use Markdown code fences.

If the user only asks a question, or the request is too ambiguous to act on,
answer briefly and DO NOT include the markers (that means "no proposal").`,
		path, proposeOpen, proposeClose, resolveFileContext(path))
}

// seedPrompt prepends the file's current content to the first user message so the
// agent can return a full proposed file (later turns reuse the session context).
func seedPrompt(path, current, msg string) string {
	return fmt.Sprintf("The file %s currently contains:\n--- BEGIN CURRENT ---\n%s\n--- END CURRENT ---\n\n%s",
		path, current, msg)
}

// ---- HTTP handlers ----
//
// editEntry (GET /edit, every edit-with-AI link's entry point) and the generic
// per-path open (POST /edit -> chatOpen) and full-page view (GET /edit/{id} ->
// chatPage) are gone; see web.go's editEntry and the file-header note above.

// chatPanel renders the live chat log, diff and proposal controls.
func (s *Server) chatPanel(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chat.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.renderConditional(w, r, "chatedit_panel.html", s.chatPanelData(r.Context(), sess))
}

// chatMessage records the user's message and runs the turn in the BACKGROUND
// (chat-editor-snappy-polish: never strand the user on a bare spinner — the
// handler returns as soon as startChat has recorded the message, so it renders
// immediately, before the agent replies; the panel's own polling, wired in
// bootstrap_agent.html, shows Busy/live-steps progress and the final reply once
// the turn settles). A busy double-submit is silently ignored, same as the
// sibling editor/resolve-agent chat handlers.
func (s *Server) chatMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chat.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if msg := strings.TrimSpace(r.FormValue("message")); msg != "" {
		// Background context: the turn outlives this request; the panel poll shows
		// progress and the final reply.
		_ = sess.startChat(context.Background(), msg)
	}
	s.render(w, "chatedit_panel.html", s.chatPanelData(r.Context(), sess))
}

// chatApprove applies+commits the pending proposal in the edit worktree.
func (s *Server) chatApprove(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chat.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := sess.approve(r.Context())
	data := s.chatPanelData(r.Context(), sess)
	if err != nil {
		data["Error"] = err.Error()
	}
	s.render(w, "chatedit_panel.html", data)
}

// chatReject drops the pending proposal (a no-op against git).
func (s *Server) chatReject(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chat.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	sess.reject()
	s.render(w, "chatedit_panel.html", s.chatPanelData(r.Context(), sess))
}

// chatPanelData projects a session into the panel template model. With no pending
// proposal the diff renders base against itself (the current file as context);
// with one, base against the proposed content highlights the change. The diff is
// syntax-highlighted per sess.Path's detected language when one is recognized
// (chat-editor-snappy-polish); an unrecognized file type falls back to the
// original plain/char-diff rendering (RenderDiffHTML(..., nil, nil) is
// RenderDiff). While busy, LiveSteps carries the in-flight turn's reasoning/
// tool-call breakdown so the panel poll shows real progress instead of a bare
// spinner; ConnState/ConnLabel drive the connecting/connected/working/error
// badge.
func (s *Server) chatPanelData(ctx context.Context, sess *chatSession) map[string]interface{} {
	base, _ := sess.base(ctx)
	proposed, has := sess.pending()
	right := base
	if has {
		right = proposed
	}
	lexer := lexerFor(sess.Path)
	oldHTML := highlightLines(base, lexer)
	newHTML := highlightLines(right, lexer)
	busy := sess.isBusy()
	var live []swarm.Part
	if busy {
		live = sess.lastAssistantSteps(ctx)
	}
	state := sess.connState()
	errText := sess.errText()
	if errText == "" {
		// No turn error to show: fall back to the connect failure's own text (if
		// any) so an "error" conn-state is never a mute badge with no explanation.
		errText = sess.connErrText()
	}
	return map[string]interface{}{
		"ID":          sess.ID,
		"Path":        sess.Path,
		"Log":         sess.logCopy(),
		"Rows":        editor.RenderDiffHTML(base, right, oldHTML, newHTML),
		"HasProposal": has,
		"Busy":        busy,
		"Error":       errText,
		"ConnState":   string(state),
		"ConnLabel":   connLabel(state),
		"LiveSteps":   live,
	}
}
