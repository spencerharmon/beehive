package web

import (
	"context"
	"errors"
	"fmt"
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
// Parts is the reasoning/tool-call breakdown behind an agent turn's flattened
// Text (nil for user/system turns, and for a client with no structured history —
// see swarm.Session.Messages): each is rendered expandable, inline, alongside
// the turn (chat-editor-snappy-polish "show ALL agent output").
type chatTurn struct {
	Role  string
	Text  string
	At    time.Time
	Parts []swarm.Part
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
// throwaway worktree branch. The opencode session is opened EAGERLY in the
// background as soon as the session is created (see prewarm) rather than on the
// first turn, so the panel's connecting -> connected transition (connState)
// happens on its own instead of waiting on the user's first message — a session
// that sits open and unused still tells the human it is reachable. The pending
// proposal lives only in memory until approved.
type chatSession struct {
	ID     string
	Path   string // repo-relative, slash form (any file, e.g. submodules/x/notes.md)
	Branch string
	wtPath string // absolute worktree path
	sys    string // opencode system prompt
	mgr    *chatManager
	wt     *git.Repo // the per-edit worktree

	// openMu serializes opencode session creation: the background prewarm and the
	// user's first real turn can both reach ensureOpen concurrently, and this
	// guarantees exactly one of them calls the client's Open while the other
	// reuses its result.
	openMu sync.Mutex

	mu         sync.Mutex
	oc         swarm.Session // set once ensureOpen succeeds (prewarm, or lazily)
	connectErr string        // set when the initial connect attempt failed
	seeded     bool          // whether the first turn's file-content seed has been sent
	log        []chatTurn
	proposal   *string // pending full-file proposal, nil when none
	busy       bool
	errMsg     string
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
	go s.prewarm()
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
// proposal out of the reply, and records the agent message. Used directly by
// callers (and tests) that want to block for the result; the HTTP path uses the
// asynchronous startChat instead (chat-editor-snappy-polish) so the panel that
// renders after a POST never blocks on the whole, often-slow turn. One turn at a
// time either way.
func (s *chatSession) chat(ctx context.Context, msg string) error {
	if err := s.beginTurn(msg); err != nil {
		return err
	}
	return s.runTurn(ctx, msg)
}

// startChat records the user message and runs the turn in the BACKGROUND, so the
// caller (the HTTP handler) returns immediately with the message already in the
// log and a busy/"working" state visible — the human is never stranded on a bare
// spinner waiting for the whole agent turn before anything happens on screen.
// Mirrors resolveSession.startChat's sync/async split. One turn at a time.
func (s *chatSession) startChat(msg string) error {
	if err := s.beginTurn(msg); err != nil {
		return err
	}
	go s.runTurn(context.Background(), msg)
	return nil
}

// beginTurn claims the busy flag and records the user's message, rejecting a
// turn already in progress. Split out of chat/startChat so both the
// synchronous and background paths record the message identically and
// IMMEDIATELY — the part of "the user's own message appears immediately on
// send" that happens before any network call, and before the caller decides
// whether to await the rest of the turn or return right away.
func (s *chatSession) beginTurn(msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return errBusy
	}
	s.busy = true
	s.errMsg = ""
	s.log = append(s.log, chatTurn{Role: "user", Text: msg, At: s.mgr.now()})
	return nil
}

// runTurn drives the assistant turn to completion and records its outcome: the
// pending proposal (if any), the flattened display text, and — when the client
// exposes structured history — the reasoning/tool-call parts behind that reply,
// captured AFTER the turn settles so the persisted history shows the final
// status of every part (see chatPanelData, which calls lastAssistantParts again
// while Busy for the DURING-the-turn view of the same parts).
func (s *chatSession) runTurn(ctx context.Context, msg string) error {
	reply, err := s.prompt(ctx, msg)
	if err != nil {
		s.mu.Lock()
		s.busy = false
		s.errMsg = err.Error()
		s.mu.Unlock()
		return err
	}
	// Fetched before taking s.mu below: it issues its own HTTP call (via the
	// opencode session) and must never run while holding the session lock.
	parts := s.lastAssistantParts(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = false
	display, proposed, ok := extractProposal(reply)
	if ok {
		if strings.TrimSpace(display) == "" {
			display = "Proposed a change."
		}
		p := proposed
		s.proposal = &p
	}
	s.log = append(s.log, chatTurn{Role: "agent", Text: display, At: s.mgr.now(), Parts: parts})
	return nil
}

// prewarmTimeout bounds the background connect kicked off at session-open (see
// prewarm), so a wedged/unreachable opencode server can never leak the goroutine
// past a bounded window. It does not gate anything else: a slow or failed
// prewarm never blocks the user's first real message, which retries the connect
// itself via ensureOpen.
const prewarmTimeout = 30 * time.Second

// prewarm eagerly opens the underlying opencode session as soon as a chat
// session is created, instead of waiting for the user's first message, so the
// panel's connecting -> connected transition (connState) happens on its own —
// the point of showing "connecting" right at session-open time rather than only
// after the human has already typed something.
func (s *chatSession) prewarm() {
	ctx, cancel := context.WithTimeout(context.Background(), prewarmTimeout)
	defer cancel()
	_, _ = s.ensureOpen(ctx)
}

// ensureOpen opens the opencode session on first use and reuses it afterward.
// openMu serializes a background prewarm racing the user's first real turn, so
// exactly one caller invokes the client's Open and the other reuses its result
// (or its failure) instead of opening a second, orphaned session.
func (s *chatSession) ensureOpen(ctx context.Context) (swarm.Session, error) {
	s.openMu.Lock()
	defer s.openMu.Unlock()
	s.mu.Lock()
	oc := s.oc
	s.mu.Unlock()
	if oc != nil {
		return oc, nil
	}
	sess, err := s.mgr.client.Open(ctx, s.wtPath, s.sys)
	s.mu.Lock()
	if err != nil {
		s.connectErr = err.Error()
	} else {
		s.oc = sess
		s.connectErr = ""
	}
	s.mu.Unlock()
	return sess, err
}

// connState reports the session's connection lifecycle for the panel:
// "connecting" before the opencode session is established (prewarm still in
// flight, or never attempted), "connected" once it is open, or "error" when the
// connect attempt failed — surfaced explicitly instead of silently waiting
// forever (chat-editor-snappy-polish: never strand the user on a bare spinner).
func (s *chatSession) connState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oc != nil {
		return "connected"
	}
	if s.connectErr != "" {
		return "error"
	}
	return "connecting"
}

// connectError returns the last connect failure, "" when none (or connected).
func (s *chatSession) connectError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectErr
}

// lastAssistantParts returns the most recent assistant message's parts from the
// live opencode session — the reasoning/tool-call breakdown behind a reply, used
// both for the DURING-the-turn live preview (busy) and the persisted per-turn
// history once it settles (runTurn). Returns nil before the session has
// connected, on a Messages error, or when the client exposes no structured
// history (Messages returning nil, nil — true of every test double that does
// not opt in).
func (s *chatSession) lastAssistantParts(ctx context.Context) []swarm.Part {
	s.mu.Lock()
	oc := s.oc
	s.mu.Unlock()
	if oc == nil {
		return nil
	}
	msgs, err := oc.Messages(ctx)
	if err != nil {
		return nil
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			return msgs[i].Parts
		}
	}
	return nil
}

// prompt ensures the opencode session is open — reusing a prewarm's connect
// when it already completed, or connecting now (and retrying on a prior prewarm
// failure) — then sends msg, seeding the current file contents into exactly the
// first REAL turn (tracked by s.seeded, independent of when the connect itself
// happened) so the agent can return a full proposed file on that turn.
func (s *chatSession) prompt(ctx context.Context, msg string) (string, error) {
	oc, err := s.ensureOpen(ctx)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	seeded := s.seeded
	s.seeded = true
	s.mu.Unlock()
	if seeded {
		return oc.Prompt(ctx, msg)
	}
	base, _ := s.base(ctx)
	return oc.Prompt(ctx, seedPrompt(s.Path, base, msg))
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
	s.render(w, "chatedit_panel.html", s.chatPanelData(r.Context(), sess))
}

// chatMessage starts the turn in the BACKGROUND (startChat) and returns the
// panel immediately: the user's message and a "working" state are already in
// it, well before the (often slow) agent turn finishes — the panel's own
// polling (hx-trigger="load, every ...") picks up progress and the final reply
// as they happen. A prior busy turn (errBusy) is simply not started again; the
// message is dropped rather than queued, matching the previous synchronous
// behavior's one-turn-at-a-time contract.
func (s *Server) chatMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chat.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if msg := strings.TrimSpace(r.FormValue("message")); msg != "" {
		_ = sess.startChat(msg)
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
// colorized by RenderDiffFile using sess.Path to pick a language (falling back to
// RenderDiff's plain rendering when none matches). ConnState/ConnectError surface
// the connecting/connected/error lifecycle (connState) and LiveParts is the
// in-flight assistant turn's reasoning/tool-call breakdown while Busy, so the
// panel never strands the human on a bare spinner (chat-editor-snappy-polish).
func (s *Server) chatPanelData(ctx context.Context, sess *chatSession) map[string]interface{} {
	base, _ := sess.base(ctx)
	proposed, has := sess.pending()
	right := base
	if has {
		right = proposed
	}
	busy := sess.isBusy()
	var live []swarm.Part
	if busy {
		live = sess.lastAssistantParts(ctx)
	}
	return map[string]interface{}{
		"ID":           sess.ID,
		"Path":         sess.Path,
		"Log":          sess.logCopy(),
		"Rows":         editor.RenderDiffFile(base, right, sess.Path),
		"HasProposal":  has,
		"Busy":         busy,
		"Error":        sess.errText(),
		"ConnState":    sess.connState(),
		"ConnectError": sess.connectError(),
		"LiveParts":    live,
	}
}
