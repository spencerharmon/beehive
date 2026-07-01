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
	mgr    *Manager  // owner, for persistence on activity

	mu       sync.Mutex
	oc       swarm.Session // opencode session (lazy: created on first chat)
	log      []Turn
	busy     bool
	err      string    // last turn error, surfaced in the panel
	activity time.Time // last open/chat/merge, drives startup staleness
}

// Manager owns active editor sessions and the worktrees backing them.
type Manager struct {
	root    string
	absRoot string
	cfg     config.Config
	primary *git.Repo
	client  agentClient

	store *store           // persisted session state (survives restart)
	ttl   time.Duration    // staleness cutoff for startup prune
	now   func() time.Time // clock (overridable in tests)

	mu   sync.Mutex
	byID map[string]*Session
}

// defaultSessionTTL is the fallback staleness cutoff when config sets no TTL. An
// edit worktree older than this AND carrying no pending change is treated as an
// abandoned leak and reclaimed at startup; a pending (unpublished) change is
// always kept regardless of age.
const defaultSessionTTL = 60 * time.Minute

// NewManager builds a Manager over the beehive repo at root.
func NewManager(root string, cfg config.Config) (*Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(cfg.TTLMinutes) * time.Minute
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	return &Manager{
		root:    root,
		absRoot: abs,
		cfg:     cfg,
		primary: git.New(root),
		client:  &swarm.Opencode{Base: cfg.AgentURL, Model: cfg.Model, Temperature: cfg.Temperature, MaxTokens: cfg.MaxTokens, HTTP: &http.Client{Timeout: 0}},
		store:   newStore(filepath.Join(abs, ".worktrees", sessionsFile)),
		ttl:     ttl,
		now:     time.Now,
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
		mgr:      m,
		sys:      systemPrompt(file),
		activity: m.now(),
	}
	m.mu.Lock()
	m.byID[s.ID] = s
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		return nil, fmt.Errorf("persist session: %w", err)
	}
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
	return m.persist()
}

// snapshot projects a Session to its persistable record under the session lock.
func (s *Session) snapshot() sessionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	logCopy := make([]Turn, len(s.log))
	copy(logCopy, s.log)
	return sessionRecord{
		ID:       s.ID,
		File:     s.File,
		Branch:   s.Branch,
		WtPath:   s.wtPath,
		Remote:   s.remote,
		BaseMain: s.baseMain,
		Activity: s.activity,
		Log:      logCopy,
	}
}

// persist snapshots every live session and atomically rewrites the store. It is
// called on every state change (open, chat turn, close) so a beehived restart
// recovers the exact set of open edits. Lock order is m.mu then each s.mu, never
// the reverse, so this is deadlock-free against the per-session turn path.
func (m *Manager) persist() error {
	m.mu.Lock()
	recs := make([]sessionRecord, 0, len(m.byID))
	for _, s := range m.byID {
		recs = append(recs, s.snapshot())
	}
	m.mu.Unlock()
	return m.store.save(recs)
}

// isEditBranch reports whether a worktree branch is an AI-editor branch (created
// by Open as edit-<slug>-<unix>). The startup prune acts ONLY on these; honeybee
// bee-* branches, the runner's beehive-* pass worktrees, and the primary main
// checkout are never enumerated for removal.
func isEditBranch(branch string) bool {
	return strings.HasPrefix(branch, "edit-")
}

// Reload recovers persisted editor sessions and prunes stale edit worktrees at
// beehived startup. It is the mirror of swarm gc-worktree-reclaim for the editor:
// enumerate the root repo's edit-* worktrees and, for each,
//
//   - KEEP + re-register it as a live Session when it has a fresh persisted record
//     (within ttl) OR carries a pending unpublished change (dirty tree or an
//     unmerged commit) — a pending/approved change is NEVER reclaimed, whatever
//     its age; and
//   - RECLAIM it (git worktree remove + branch -D) when it is both stale (no fresh
//     record) and clean (no pending change) — the abandoned-edit leak.
//
// Non-edit worktrees (bee-*, beehive-*, main) are skipped entirely. The store is
// rewritten to exactly the surviving sessions, so the prune is idempotent: a
// second Reload with nothing new to remove is a no-op. Reload is safe to call
// once at startup before the Manager serves any request.
func (m *Manager) Reload(ctx context.Context) error {
	recs, err := m.store.load()
	if err != nil {
		return err
	}
	byBranch := make(map[string]sessionRecord, len(recs))
	for _, rec := range recs {
		byBranch[rec.Branch] = rec
	}
	wts, err := m.primary.Worktrees(ctx)
	if err != nil {
		return err
	}
	now := m.now()
	for _, w := range wts {
		if !isEditBranch(w.Branch) {
			continue
		}
		rec, hasRec := byBranch[w.Branch]
		pending, perr := m.worktreePending(ctx, w)
		if perr != nil {
			return perr
		}
		fresh := hasRec && now.Sub(rec.Activity) < m.ttl
		if fresh || pending {
			s, serr := m.restore(ctx, w, rec, hasRec)
			if serr != nil {
				return serr
			}
			if s != nil {
				m.mu.Lock()
				m.byID[s.ID] = s
				m.mu.Unlock()
			}
			continue
		}
		if err := m.reclaim(ctx, w); err != nil {
			return err
		}
	}
	return m.persist()
}

// worktreePending reports whether an edit worktree holds an unpublished change
// that must be preserved: uncommitted working-tree changes, or commits on the
// branch not yet on main. The three-dot diff (merge-base(main,branch)..branch)
// scopes "unmerged" to what the branch itself changed, so main advancing after
// the fork does not read as pending.
func (m *Manager) worktreePending(ctx context.Context, w git.Worktree) (bool, error) {
	wg := git.New(w.Path)
	clean, err := wg.Clean(ctx)
	if err != nil {
		return false, err
	}
	if !clean {
		return true, nil
	}
	out, err := wg.Run(ctx, "diff", "--name-only", "main..."+w.Branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// reclaim removes a stale edit worktree and deletes its branch, mirroring the
// swarm gc-worktree-reclaim cleanup (force-remove the worktree, then branch -D).
func (m *Manager) reclaim(ctx context.Context, w git.Worktree) error {
	if err := m.primary.WorktreeRemove(ctx, w.Path); err != nil {
		return err
	}
	_, _ = m.primary.Run(ctx, "branch", "-D", w.Branch)
	return nil
}

// restore rebuilds a live *Session from a surviving edit worktree so the resumed
// edit is fully operable (diff/state/merge) after a restart. With a persisted
// record it uses the recorded file/remote/base/log/activity verbatim; without one
// (a pending worktree whose store entry was lost) it best-effort derives the
// edited file from git and the publish remote from the repo, returning (nil, nil)
// only when no edited file can be determined (the worktree is still kept on disk,
// never destroyed, by Reload's pending guard).
func (m *Manager) restore(ctx context.Context, w git.Worktree, rec sessionRecord, hasRec bool) (*Session, error) {
	file := rec.File
	remote := rec.Remote
	baseMain := rec.BaseMain
	log := rec.Log
	activity := rec.Activity
	if !hasRec {
		f, err := m.editedFile(ctx, w)
		if err != nil {
			return nil, err
		}
		if f == "" {
			return nil, nil
		}
		file = f
		if r, rerr := m.primary.Remote(ctx); rerr == nil {
			remote = r
		}
		activity = m.now()
	}
	s := &Session{
		ID:       w.Branch,
		File:     file,
		Branch:   w.Branch,
		wtPath:   w.Path,
		remote:   remote,
		baseMain: baseMain,
		client:   m.client,
		wt:       git.New(w.Path),
		mgr:      m,
		sys:      systemPrompt(file),
		log:      log,
		activity: activity,
	}
	return s, nil
}

// editedFile derives the repo-relative file an edit worktree changed, for the
// recovery of a pending worktree whose store record was lost. It prefers the
// committed change (three-dot diff vs main), falling back to the uncommitted
// working-tree change, and returns the first path that is a valid editable file.
func (m *Manager) editedFile(ctx context.Context, w git.Worktree) (string, error) {
	wg := git.New(w.Path)
	committed, err := wg.Run(ctx, "diff", "--name-only", "main..."+w.Branch)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(committed, "\n") {
		if f := strings.TrimSpace(line); f != "" && ValidateFile(f) == nil {
			return filepath.ToSlash(filepath.Clean(f)), nil
		}
	}
	status, err := wg.Run(ctx, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Porcelain: "XY <path>" (rename shows "orig -> new"; take the new path).
		fields := strings.Fields(line)
		f := fields[len(fields)-1]
		if ValidateFile(f) == nil {
			return filepath.ToSlash(filepath.Clean(f)), nil
		}
	}
	return "", nil
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
	s.activity = s.mgr.now()
	s.mu.Unlock()
	if perr := s.mgr.persist(); perr != nil {
		s.setErr("persist: " + perr.Error())
	}
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
