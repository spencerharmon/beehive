package editor

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

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/swarm"
)

// agentClient is the slice of the opencode client the editor needs: a session
// seeded with a system prompt and a first message, returning the reply. Narrowed
// to an interface so tests can inject a fake. (*swarm.Opencode) satisfies it.
type agentClient interface {
	NewSession(ctx context.Context, cwd, system, first string) (swarm.Session, string, error)
}

// mergeMarker is the control token the agent appends to its reply when the user
// has explicitly approved merging. beehived performs the merge on its behalf, so
// "ask the agent to merge" and clicking Merge converge to the same git state.
const mergeMarker = "<<<MERGE>>>"

// editableBasenames are the human-owned coordination files an editor session may
// touch. Everything else (PLAN.md, AGENTS.md, secrets, code) is off limits.
var editableBasenames = map[string]bool{
	repo.ROIFile:   true,
	repo.InfraFile: true,
	repo.LinksFile: true,
}

// ErrDeleteNeedsConfirm is returned by Merge when the pending proposal would
// delete a human-owned file wholesale (an empty/absent worktree file against a
// non-empty base). The merge is default-BLOCKED; a separate, explicit
// confirmation (MergeConfirm) is required so a wrong-base "phantom deletion" can
// never auto-merge — the incident that motivated the editor safety guards.
var ErrDeleteNeedsConfirm = errors.New("editor: refusing to merge a whole-file deletion of a human-owned file without explicit confirmation")

// humanOwnedBasenames are the files the swarm must never delete or rewrite
// without a human in the loop. ROI.md is the human record of intent (the
// pre-commit/pre-receive hooks already forbid honeybee edits to it); the editor
// adds a delete-confirmation guard on top. Beehive-owned coordination files
// (INFRASTRUCTURE.md, SUBMODULE-LINKS.yaml) are editable without this gate.
var humanOwnedBasenames = map[string]bool{
	repo.ROIFile: true,
}

// humanOwned reports whether file (repo-relative) is human-owned/protected,
// aligned with the ROI hook's protected-path notion (ROI.md at minimum).
func humanOwned(file string) bool {
	return humanOwnedBasenames[filepath.Base(filepath.ToSlash(file))]
}

// isWholeFileDeletion reports whether proposed removes a non-empty base entirely
// (empty or absent proposed vs a non-empty base) — a red flag, not a normal diff.
func isWholeFileDeletion(base, proposed string) bool {
	return strings.TrimSpace(base) != "" && strings.TrimSpace(proposed) == ""
}

// ProtectedDeletion reports whether the (base, proposed) pair for file is a
// whole-file deletion of a human-owned file: the case Merge default-blocks. The
// web layer uses it to surface a distinct, explicit confirmation control.
func ProtectedDeletion(file, base, proposed string) bool {
	return humanOwned(file) && isWholeFileDeletion(base, proposed)
}

// Turn is one chat message in a session's log.
type Turn struct {
	Role string    `json:"role"` // "user" | "agent"
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Session is one collaborative single-file edit on its own worktree branch.
type Session struct {
	ID       string `json:"id"`
	File     string `json:"file"` // repo-relative, e.g. submodules/x/ROI.md
	Branch   string `json:"branch"`
	wtPath   string // absolute worktree path
	sys      string // opencode system prompt
	remote   string
	baseMain string

	client agentClient
	wt     *git.Repo // worktree git

	mu   sync.Mutex
	oc   swarm.Session // opencode session (lazy: created on first chat)
	log  []Turn
	busy bool
	err  string // last turn error, surfaced in the panel
}

// Manager owns active editor sessions and the worktrees backing them.
type Manager struct {
	root    string
	absRoot string
	cfg     config.Config
	primary *git.Repo
	client  agentClient

	mu   sync.Mutex
	byID map[string]*Session
}

// NewManager builds a Manager over the beehive repo at root.
func NewManager(root string, cfg config.Config) (*Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Manager{
		root:    root,
		absRoot: abs,
		cfg:     cfg,
		primary: git.New(root),
		client:  &swarm.Opencode{Base: cfg.AgentURL, Model: cfg.Model, Temperature: cfg.Temperature, MaxTokens: cfg.MaxTokens, HTTP: &http.Client{Timeout: 0}},
		byID:    map[string]*Session{},
	}, nil
}

// ValidateFile reports whether file (repo-relative) is an editable coordination
// file and is safe (no traversal). It does not require the file to exist yet.
func ValidateFile(file string) error {
	clean := filepath.ToSlash(filepath.Clean(file))
	if clean == "." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") || strings.Contains(clean, "../") {
		return fmt.Errorf("invalid file path %q", file)
	}
	if !editableBasenames[filepath.Base(clean)] {
		return fmt.Errorf("%q is not an editable file", file)
	}
	return nil
}

// Open creates a worktree+branch for editing file and registers a session.
//
// Base selection is safety-hardened (editor-safety-guards): a configured remote
// is trusted as the worktree base ONLY when it is the repo's OWN — its main
// shares history with local main. A foreign/unrelated origin/main is ignored in
// favor of local main and is never used as a merge push target. The chosen base
// is then validated to contain the target file whenever local main does, so a
// session can never open onto a destructive whole-file-deletion diff produced by
// a wrong/foreign base. Genuine new-file creation (absent at both) stays allowed.
func (m *Manager) Open(ctx context.Context, file string) (*Session, error) {
	file = filepath.ToSlash(filepath.Clean(file))
	if err := ValidateFile(file); err != nil {
		return nil, err
	}
	remote, err := m.primary.Remote(ctx)
	if err != nil {
		return nil, err
	}
	base := "main" // local main: the safe default
	trusted := ""  // remote to publish to on merge; only a verified repo-own remote
	if remote != "" {
		if err := m.primary.Fetch(ctx, remote, "main"); err != nil {
			return nil, fmt.Errorf("fetch %s main: %w", remote, err)
		}
		// Don't blindly trust origin/main: prefer it over local main ONLY when it
		// is the repo's own remote (shared history). A foreign origin/main is the
		// exact wrong-base that turned "edit ROI.md" into "delete ROI.md".
		own, err := m.primary.SharesHistory(ctx, "main", remote+"/main")
		if err != nil {
			return nil, err
		}
		if own {
			base = remote + "/main"
			trusted = remote
		}
	}
	baseMain, err := m.primary.RevParse(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", base, err)
	}
	// Validate the edit base: the target MUST exist at base when it exists on local
	// main, else the worktree (cut from base) renders an existing file as a
	// destructive deletion. Fail session-open with a clear error instead.
	if m.primary.Exists(ctx, "main", file) && !m.primary.Exists(ctx, base, file) {
		return nil, fmt.Errorf("editor: %s exists on main but not at edit base %q; refusing to open a destructive edit (the base may be a wrong or foreign main)", file, base)
	}
	branch := "edit-" + slugFile(file) + "-" + fmt.Sprint(time.Now().Unix())
	wtPath := filepath.Join(m.absRoot, ".worktrees", branch)
	if err := m.primary.WorktreeAdd(ctx, wtPath, branch, base); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}
	s := &Session{
		ID:       branch,
		File:     file,
		Branch:   branch,
		wtPath:   wtPath,
		remote:   trusted,
		baseMain: baseMain,
		client:   m.client,
		wt:       git.New(wtPath),
		sys:      systemPrompt(file),
	}
	m.mu.Lock()
	m.byID[s.ID] = s
	m.mu.Unlock()
	return s, nil
}

// Get returns a registered session.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	return s, ok
}

// List returns the active sessions (unordered).
func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.byID))
	for _, s := range m.byID {
		out = append(out, s)
	}
	return out
}

// Close removes a session's worktree and branch and unregisters it.
func (m *Manager) Close(ctx context.Context, id string) error {
	m.mu.Lock()
	s, ok := m.byID[id]
	if ok {
		delete(m.byID, id)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	s.mu.Lock()
	if s.oc != nil {
		_ = s.oc.Close()
	}
	s.mu.Unlock()
	_ = m.primary.WorktreeRemove(ctx, s.wtPath)
	_, _ = m.primary.Run(ctx, "branch", "-D", s.Branch)
	return nil
}

// StartChat appends the user's message and runs the agent turn in the
// background, so the HTTP handler can return immediately and the UI poll renders
// the diff as the agent edits the file on disk. One turn at a time per session.
func (s *Session) StartChat(bg context.Context, msg string) error {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return fmt.Errorf("a turn is already in progress")
	}
	s.busy = true
	s.err = ""
	s.log = append(s.log, Turn{Role: "user", Text: msg, At: time.Now()})
	s.mu.Unlock()

	go s.runTurn(bg, msg)
	return nil
}

// Chat runs a turn synchronously and returns the agent's reply (API clients that
// want a blocking call). It still records the turn and may merge.
func (s *Session) Chat(ctx context.Context, msg string) (string, error) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return "", fmt.Errorf("a turn is already in progress")
	}
	s.busy = true
	s.err = ""
	s.log = append(s.log, Turn{Role: "user", Text: msg, At: time.Now()})
	s.mu.Unlock()
	return s.runTurn(ctx, msg)
}

func (s *Session) runTurn(ctx context.Context, msg string) (string, error) {
	reply, err := s.prompt(ctx, msg)
	s.mu.Lock()
	if err != nil {
		s.err = err.Error()
		s.busy = false
		s.mu.Unlock()
		return "", err
	}
	doMerge := strings.Contains(reply, mergeMarker)
	display := strings.TrimSpace(strings.ReplaceAll(reply, mergeMarker, ""))
	s.log = append(s.log, Turn{Role: "agent", Text: display, At: time.Now()})
	s.mu.Unlock()

	// Commit whatever the agent wrote so the branch carries the proposal (and a
	// merge can fast-forward). Nothing-to-commit is fine (agent only answered).
	if cerr := s.wt.CommitPaths(ctx, "editor: "+s.File, s.File); cerr != nil && cerr != git.ErrNothing {
		s.setErr("commit: " + cerr.Error())
	}
	if doMerge {
		// An agent-driven merge can NEVER confirm a protected whole-file deletion;
		// that requires an explicit human action. A phantom deletion (e.g. a wrong
		// base) surfaces here as a panel error instead of silently publishing.
		if merr := s.merge(ctx, false); merr != nil {
			s.setErr("merge: " + merr.Error())
		}
	}
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
	return display, nil
}

// prompt opens the opencode session on first use (seeding system + first
// message) and reuses it for later turns.
func (s *Session) prompt(ctx context.Context, msg string) (string, error) {
	s.mu.Lock()
	oc := s.oc
	s.mu.Unlock()
	if oc == nil {
		sess, reply, err := s.client.NewSession(ctx, s.wtPath, s.sys, msg)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.oc = sess
		s.mu.Unlock()
		return reply, nil
	}
	return oc.Prompt(ctx, msg)
}

func (s *Session) setErr(msg string) {
	s.mu.Lock()
	s.err = msg
	s.mu.Unlock()
}

// Merge publishes the session branch to main, making the proposal live. It
// DEFAULT-BLOCKS a whole-file deletion of a human-owned file (returns
// ErrDeleteNeedsConfirm) so a wrong-base phantom deletion cannot auto-merge; use
// MergeConfirm for the explicit, separate confirmation. Safe to call when already
// merged (no-op).
func (s *Session) Merge(ctx context.Context) error { return s.merge(ctx, false) }

// MergeConfirm is Merge plus the explicit human confirmation that authorizes a
// whole-file deletion of a human-owned file — the SEPARATE action the delete
// guard requires. Every other merge behaves identically to Merge.
func (s *Session) MergeConfirm(ctx context.Context) error { return s.merge(ctx, true) }

func (s *Session) merge(ctx context.Context, confirmDelete bool) error {
	if cerr := s.wt.CommitPaths(ctx, "editor: "+s.File, s.File); cerr != nil && cerr != git.ErrNothing {
		return cerr
	}
	// Delete guard: never auto-merge a whole-file deletion of a human-owned file.
	// An empty/absent proposed against a non-empty base is a red flag (the wrong-
	// base incident), so require an explicit, separate confirmation.
	if !confirmDelete {
		base, proposed, derr := s.Diff(ctx)
		if derr != nil {
			return derr
		}
		if ProtectedDeletion(s.File, base, proposed) {
			return ErrDeleteNeedsConfirm
		}
	}
	if s.remote != "" {
		// A configured remote is authoritative: publish to remote main (hard fail if
		// the remote rejects). Then fast-forward local main so beehived's own views
		// and the dirty/live state reflect it immediately; a dirty primary tree only
		// downgrades that local sync to a soft warning — the remote already has it.
		if err := s.wt.PublishToMain(ctx, s.remote); err != nil {
			return err
		}
		if err := s.wt.UpdateLocalMain(ctx); err != nil {
			s.setErr("pushed to remote main; local working tree not updated: " + err.Error())
		}
		return nil
	}
	// No remote: local main is the only target and is what makes the proposal live.
	return s.wt.UpdateLocalMain(ctx)
}

// Diff returns the file content on main (base) and in the worktree (proposed).
func (s *Session) Diff(ctx context.Context) (base, proposed string, err error) {
	base, _ = s.wt.Show(ctx, "main", s.File) // "" when the file is new on main
	b, rerr := os.ReadFile(filepath.Join(s.wtPath, filepath.FromSlash(s.File)))
	if rerr != nil && !os.IsNotExist(rerr) {
		return "", "", rerr
	}
	return base, string(b), nil
}

// State reports "dirty" when the worktree file differs from main (an unmerged
// proposal exists) or "live" when they match (nothing pending / merged). This is
// computed from git reality, so an agent-performed merge is detected the same as
// a button merge.
func (s *Session) State(ctx context.Context) string {
	base, proposed, err := s.Diff(ctx)
	if err != nil {
		return "unknown"
	}
	if strings.TrimRight(base, "\n") == strings.TrimRight(proposed, "\n") {
		return "live"
	}
	return "dirty"
}

// Log returns a copy of the chat log.
func (s *Session) Log() []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Turn, len(s.log))
	copy(out, s.log)
	return out
}

// Status is a snapshot for the API/UI.
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *Session) Err() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func slugFile(file string) string {
	r := strings.NewReplacer("/", "-", ".", "-", " ", "-")
	return strings.Trim(r.Replace(file), "-")
}

func systemPrompt(file string) string {
	return fmt.Sprintf(`You are a collaborative editor for ONE file in a git repository: %s.

Rules:
- ONLY edit %s. Never touch any other file. Never run git, never commit; the system commits and merges for you.
- Make exactly the change the user asks for by editing %s directly with your file tools.
- Reply in ONE short sentence naming what you changed, e.g. "I changed the intro and added a Goals section. How does that look?". Do not paste file contents or the diff; the UI shows them.
- Ask a brief clarifying question only if the request is genuinely ambiguous.
- When, and ONLY when, the user explicitly approves merging/publishing the change, end your reply with a final line containing exactly: %s`,
		file, file, file, mergeMarker)
}
