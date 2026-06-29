package editor

import (
	"context"
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
		client:  &swarm.Opencode{Base: cfg.AgentURL, Model: cfg.Model, HTTP: &http.Client{Timeout: 0}},
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

// Open creates a worktree+branch for editing file and registers a session. The
// branch is cut from the freshest main (remote when configured).
func (m *Manager) Open(ctx context.Context, file string) (*Session, error) {
	file = filepath.ToSlash(filepath.Clean(file))
	if err := ValidateFile(file); err != nil {
		return nil, err
	}
	remote, err := m.primary.Remote(ctx)
	if err != nil {
		return nil, err
	}
	base := "main"
	if remote != "" {
		if err := m.primary.Fetch(ctx, remote, "main"); err != nil {
			return nil, fmt.Errorf("fetch %s main: %w", remote, err)
		}
		base = remote + "/main"
	}
	baseMain, err := m.primary.RevParse(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", base, err)
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
		remote:   remote,
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
		if merr := s.merge(ctx); merr != nil {
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

// Merge publishes the session branch to main, making the proposal live. Safe to
// call when already merged (no-op).
func (s *Session) Merge(ctx context.Context) error { return s.merge(ctx) }

func (s *Session) merge(ctx context.Context) error {
	if cerr := s.wt.CommitPaths(ctx, "editor: "+s.File, s.File); cerr != nil && cerr != git.ErrNothing {
		return cerr
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
