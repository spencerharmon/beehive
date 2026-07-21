package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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

// transcriptSidecarPath is the tracked file (repo-relative to the edit
// worktree) that carries a session's running chat log while a remote-durable
// edit is in flight (chat-diff-session-durability): committed on the edit
// branch alongside the proposed target-file change each turn, so a push/fetch
// that recovers the branch also recovers the transcript. It is bookkeeping for
// THIS in-flight session only, never part of the proposed change, so merge()
// strips it from the branch again before ever publishing to main.
const transcriptSidecarPath = ".beehive-editor-session.json"

// editableBasenames are the beehive coordination-file basenames an editor
// session may touch: every member of the per-submodule optional-file set
// (repo.OptionalFiles) and the repo-ROOT instruction-file set
// (repo.RootInstructionFiles) — the exact set the frontend renders an
// edit-with-AI link for (dashboard/explorer/roi_editor,
// ai-edit-publish-to-main) — plus the beehive-owned links file. Building the
// set from those two canonical declarations, rather than a hand-maintained
// list, keeps it in lockstep with every file a real "edit with AI" link ever
// targets. PLAN.md, secrets, and submodule CODE stay categorically off
// limits: they are honeybee/swarm-owned or security-sensitive and are never
// reachable through this editor.
var editableBasenames = buildEditableBasenames()

func buildEditableBasenames() map[string]bool {
	m := map[string]bool{repo.LinksFile: true}
	for _, f := range repo.OptionalFiles {
		m[f] = true
	}
	for _, f := range repo.RootInstructionFiles {
		m[f.File] = true
	}
	return m
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
	busy     bool      // UI-facing "working…" status; clears the instant the reply is ready (chat-editor-status-poll-fix), NOT when publish work finishes
	turnOn   bool      // serializes turns end-to-end (reply + commit/transcript/push/merge); NEVER surfaced to the UI
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

// trustedRemote resolves the primary repo's configured remote and verifies it
// is this repo's OWN — it shares history with local main (editor-safety-
// guards) — before returning it. Both "no remote configured" (local sharing)
// and "a foreign/unrelated remote is configured" (never trusted) report as ""
// — the safe sharing-modes no-op signal. A trusted remote is the ONLY remote
// ever used as an edit base, a merge push target, or (chat-diff-session-
// durability) a per-turn durability push/fetch/recovery target.
func (m *Manager) trustedRemote(ctx context.Context) (string, error) {
	remote, err := m.primary.Remote(ctx)
	if err != nil {
		return "", err
	}
	if remote == "" {
		return "", nil
	}
	if err := m.primary.Fetch(ctx, remote, "main"); err != nil {
		return "", fmt.Errorf("fetch %s main: %w", remote, err)
	}
	own, err := m.primary.SharesHistory(ctx, "main", remote+"/main")
	if err != nil {
		return "", err
	}
	if !own {
		return "", nil
	}
	return remote, nil
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
	// trusted is "" unless the configured remote is verified as the repo's OWN
	// (shared history) — never a foreign/unrelated one. Only a trusted remote is
	// used as the edit base, the merge push target, and (chat-diff-session-
	// durability) the per-turn durability push/fetch target.
	trusted, err := m.trustedRemote(ctx)
	if err != nil {
		return nil, err
	}
	base := "main" // local main: the safe default
	if trusted != "" {
		base = trusted + "/main"
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
	if s.remote != "" {
		// chat-diff-session-durability: an intentionally closed session must
		// never resurrect itself from its own pushed copy on a later Reload's
		// remote scan. Best-effort — DeleteRemoteBranch already tolerates
		// "already gone" as success, and a real failure here (network/auth) must
		// not block Close.
		_ = m.primary.DeleteRemoteBranch(ctx, s.remote, s.Branch)
	}
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
//   - RECLAIM it (git worktree remove + branch -D, plus — chat-diff-session-
//     durability — deleting its pushed copy on a trusted remote, so it can never
//     be resurrected by a later remote scan below) when it is both stale (no
//     fresh record) and clean (no pending change) — the abandoned-edit leak.
//
// chat-diff-session-durability extends this to survive a LOST local worktree,
// not just a restarted process over the same checkout:
//
//   - a dangling worktree registration whose checkout directory was removed
//     out of band (e.g. a wiped scratch dir) is pruned and rebuilt — from the
//     surviving local branch when one exists (never discarding a not-yet-
//     pushed commit in favor of an older remote tip), else, only on a verified
//     repo-own remote, fetched and rebuilt from that remote's tip; and
//   - when a trusted remote exists, every one of ITS edit-* branches with NO
//     local trace at all (a different host, or a fully-lost local repo) is
//     likewise fetched and rebuilt.
//
// Either recovery path then runs through the SAME fresh/pending KEEP-or-RECLAIM
// decision as any other worktree, and restore() recovers the chat transcript
// from the branch's own committed sidecar when the local record can't supply it.
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
	trusted, err := m.trustedRemote(ctx)
	if err != nil {
		return err
	}
	wts, err := m.primary.Worktrees(ctx)
	if err != nil {
		return err
	}
	now := m.now()
	seen := make(map[string]bool, len(wts))
	for _, w := range wts {
		if !isEditBranch(w.Branch) {
			continue
		}
		seen[w.Branch] = true
		missing, serr := dirMissing(w.Path)
		if serr != nil {
			return serr
		}
		if missing {
			// The checkout directory is gone (removed out of band) but git's own
			// worktree admin metadata still lists it; clear that dangling
			// registration before attempting to rebuild at the same path.
			_, _ = m.primary.Run(ctx, "worktree", "prune")
			wtPath, ok, rerr := m.recoverMissingWorktree(ctx, w.Branch, trusted)
			if rerr != nil {
				return rerr
			}
			if !ok {
				continue // gone everywhere reachable; nothing left to keep or reclaim
			}
			w.Path = wtPath
		}
		if err := m.evaluateWorktree(ctx, w, byBranch, now, trusted); err != nil {
			return err
		}
	}
	if trusted != "" {
		// Recover sessions with NO local trace whatsoever — a different host, or
		// a fully-lost local repo — from every edit-* branch the trusted remote
		// still carries that this local loop never saw.
		branches, err := m.primary.ListRemoteBranches(ctx, trusted, "edit-*")
		if err != nil {
			return err
		}
		for _, branch := range branches {
			if seen[branch] {
				continue
			}
			wtPath, ok, rerr := m.recoverMissingWorktree(ctx, branch, trusted)
			if rerr != nil {
				return rerr
			}
			if !ok {
				continue
			}
			w := git.Worktree{Path: wtPath, Branch: branch}
			if err := m.evaluateWorktree(ctx, w, byBranch, now, trusted); err != nil {
				return err
			}
		}
	}
	return m.persist()
}

// evaluateWorktree applies Reload's KEEP-or-RECLAIM decision to one edit
// worktree (freshly recovered or already present): KEEP + register when it
// carries a fresh persisted record or a pending unpublished change, else
// RECLAIM it — and, when trusted names a verified repo-own remote, delete its
// pushed copy there too, so a reclaimed session is never resurrected by a
// later Reload's remote scan (chat-diff-session-durability).
func (m *Manager) evaluateWorktree(ctx context.Context, w git.Worktree, byBranch map[string]sessionRecord, now time.Time, trusted string) error {
	rec, hasRec := byBranch[w.Branch]
	pending, err := m.worktreePending(ctx, w)
	if err != nil {
		return err
	}
	fresh := hasRec && now.Sub(rec.Activity) < m.ttl
	if fresh || pending {
		s, err := m.restore(ctx, w, rec, hasRec, trusted)
		if err != nil {
			return err
		}
		if s != nil {
			m.mu.Lock()
			m.byID[s.ID] = s
			m.mu.Unlock()
		}
		return nil
	}
	if err := m.reclaim(ctx, w); err != nil {
		return err
	}
	if trusted != "" {
		_ = m.primary.DeleteRemoteBranch(ctx, trusted, w.Branch) // best-effort; never resurrect
	}
	return nil
}

// dirMissing reports whether path does not currently exist on disk. Any other
// stat failure (e.g. a permissions error) is a real error, never folded into
// "missing".
func dirMissing(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// recoverMissingWorktree rebuilds branch's worktree at its standard path when
// its checkout directory is gone (chat-diff-session-durability): reused as-is
// from the surviving LOCAL branch ref when one exists — so a not-yet-pushed
// commit is never discarded in favor of an older remote tip — else, only when
// trusted names a verified repo-own remote, fetched and rebuilt from that
// remote's tip. ok=false means branch survives NOWHERE reachable; there is
// nothing to recover (treated exactly like an already-reclaimed session).
func (m *Manager) recoverMissingWorktree(ctx context.Context, branch, trusted string) (wtPath string, ok bool, err error) {
	wtPath = filepath.Join(m.absRoot, ".worktrees", branch)
	if _, verr := m.primary.RevParse(ctx, "refs/heads/"+branch); verr == nil {
		if _, err := m.primary.Run(ctx, "worktree", "add", wtPath, branch); err != nil {
			return "", false, err
		}
		return wtPath, true, nil
	}
	if trusted == "" {
		return "", false, nil
	}
	tip, err := m.primary.LsRemoteBranch(ctx, trusted, branch)
	if err != nil {
		return "", false, err
	}
	if tip == "" {
		return "", false, nil // gone everywhere; nothing to recover
	}
	if err := m.primary.Fetch(ctx, trusted, branch); err != nil {
		return "", false, err
	}
	if err := m.primary.WorktreeAdd(ctx, wtPath, branch, trusted+"/"+branch); err != nil {
		return "", false, err
	}
	return wtPath, true, nil
}

// Reclaimable reports, without mutating anything, the edit-branch worktrees a
// Reload would reclaim: those both stale (no fresh persisted record) and clean
// (no pending unpublished change). It is the read-only half of Reload, so a gc
// skill can present an exact dry-run plan ("these branches will be removed")
// that applying (Reload) then performs. Branch names are sorted for a
// deterministic plan. Non-edit worktrees (bee-*, beehive-*, main) are ignored.
//
// This is a purely LOCAL preview: it does not fetch a remote's edit-* branches,
// so a session with no local trace at all (chat-diff-session-durability's
// cross-host recovery) is never listed here — Reload alone recovers those.
func (m *Manager) Reclaimable(ctx context.Context) ([]string, error) {
	recs, err := m.store.load()
	if err != nil {
		return nil, err
	}
	byBranch := make(map[string]sessionRecord, len(recs))
	for _, rec := range recs {
		byBranch[rec.Branch] = rec
	}
	wts, err := m.primary.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	now := m.now()
	var out []string
	for _, w := range wts {
		if !isEditBranch(w.Branch) {
			continue
		}
		rec, hasRec := byBranch[w.Branch]
		pending, perr := m.worktreePending(ctx, w)
		if perr != nil {
			return nil, perr
		}
		fresh := hasRec && now.Sub(rec.Activity) < m.ttl
		if fresh || pending {
			continue
		}
		out = append(out, w.Branch)
	}
	sort.Strings(out)
	return out, nil
}

// worktreePending reports whether an edit worktree holds an unpublished change
// that must be preserved: uncommitted working-tree changes, or commits on the
// branch not yet on main. The three-dot diff (merge-base(main,branch)..branch)
// scopes "unmerged" to what the branch itself changed, so main advancing after
// the fork does not read as pending. The durability sidecar (transcriptSidecarPath)
// is excluded from the committed-diff check: it is bookkeeping, not a proposal,
// so a session that only ever chatted (no real file edit) stays reclaimable
// after its TTL exactly as it was before chat-diff-session-durability existed.
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
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" && f != transcriptSidecarPath {
			return true, nil
		}
	}
	return false, nil
}

// reclaim removes a stale edit worktree and deletes its branch, mirroring the
// swarm gc-worktree-reclaim cleanup (force-remove the worktree, then branch -D).
// Deleting its pushed remote copy, when one exists, is the caller's job
// (evaluateWorktree) — reclaim itself stays purely local.
func (m *Manager) reclaim(ctx context.Context, w git.Worktree) error {
	if err := m.primary.WorktreeRemove(ctx, w.Path); err != nil {
		return err
	}
	_, _ = m.primary.Run(ctx, "branch", "-D", w.Branch)
	return nil
}

// restore rebuilds a live *Session from a surviving edit worktree so the resumed
// edit is fully operable (diff/state/merge) after a restart. With a persisted
// record it uses the recorded file/base/log/activity verbatim; without one (a
// pending worktree whose store entry was lost) it best-effort derives the
// edited file from git, returning (nil, nil) only when no edited file can be
// determined (the worktree is still kept on disk, never destroyed, by Reload's
// pending guard). trusted (Reload's already-verified repo-own remote, "" for
// local sharing or a foreign remote) is used, for a record-less recovery, in
// place of the record's own Remote — the same trust gate every other remote use
// in this package goes through (editor-safety-guards).
//
// The edit branch's own committed transcript sidecar (chat-diff-session-
// durability) is preferred over whatever the record carried, when the worktree
// has one: it is what survives a lost-and-rebuilt worktree when the record
// itself was also lost, and is otherwise just as fresh (both are written every
// turn). An older session that predates the sidecar, or one that never had a
// trusted remote so never wrote one, falls back to the record's own log.
func (m *Manager) restore(ctx context.Context, w git.Worktree, rec sessionRecord, hasRec bool, trusted string) (*Session, error) {
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
		remote = trusted
		activity = m.now()
	}
	if sidecar, ok := readTranscriptSidecar(w.Path); ok {
		log = sidecar
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

// readTranscriptSidecar loads the durability sidecar (transcriptSidecarPath)
// from an edit worktree, if present and well-formed. A missing or malformed
// sidecar is not an error: restore() falls back to the local store's own
// record in that case (an older session that predates the sidecar, or one
// that never had a trusted remote so never wrote it).
func readTranscriptSidecar(wtPath string) ([]Turn, bool) {
	b, err := os.ReadFile(filepath.Join(wtPath, transcriptSidecarPath))
	if err != nil {
		return nil, false
	}
	var log []Turn
	if err := json.Unmarshal(b, &log); err != nil {
		return nil, false
	}
	return log, true
}

// StartChat appends the user's message and runs the agent turn in the
// background, so the HTTP handler can return immediately and the UI poll renders
// the diff as the agent edits the file on disk. One turn at a time per session.
func (s *Session) StartChat(bg context.Context, msg string) error {
	s.mu.Lock()
	if s.turnOn {
		s.mu.Unlock()
		return fmt.Errorf("a turn is already in progress")
	}
	s.turnOn = true
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
	if s.turnOn {
		s.mu.Unlock()
		return "", fmt.Errorf("a turn is already in progress")
	}
	s.turnOn = true
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
		s.turnOn = false
		s.mu.Unlock()
		return "", err
	}
	doMerge := strings.Contains(reply, mergeMarker)
	display := strings.TrimSpace(strings.ReplaceAll(reply, mergeMarker, ""))
	s.log = append(s.log, Turn{Role: "agent", Text: display, At: time.Now()})
	// chat-editor-status-poll-fix: the assistant's reply is now ready and
	// recorded — clear the UI-facing status HERE, not after the trailing
	// commit/transcript/push/merge publish work below. Those steps still run
	// (serialized by turnOn, not busy) so a concurrent turn cannot race the
	// worktree, but the human-visible "working…" indicator must not linger
	// once the reply the human is waiting for is already in the log.
	s.busy = false
	s.mu.Unlock()

	// Commit whatever the agent wrote so the branch carries the proposal (and a
	// merge can fast-forward). Nothing-to-commit is fine (agent only answered).
	if cerr := s.wt.CommitPaths(ctx, "editor: "+s.File, s.File); cerr != nil && cerr != git.ErrNothing {
		s.setErr("commit: " + cerr.Error())
	}
	// Remote durability (chat-diff-session-durability): a trusted remote means
	// this session's edit branch converges through git like a honeybee's
	// bee-<taskid> branch, so a crash/restart/different host can resume it.
	// Commit the running transcript as a tracked sidecar (merge() strips it
	// again before ever publishing to main) and push the branch, mirroring
	// honeybee's own push-after-commit. Obeying sharing-modes: with no trusted
	// remote (local sharing, or a foreign/unrelated one) this whole step is a
	// no-op, so behavior is unchanged from before this feature existed. A
	// failure here is surfaced via the session's error state, never swallowed.
	if s.remote != "" {
		if terr := s.commitTranscript(ctx); terr != nil {
			s.setErr("transcript commit: " + terr.Error())
		} else if perr := s.wt.PushBranchReconciled(ctx, s.remote, s.Branch); perr != nil {
			s.setErr("push: " + perr.Error())
		}
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
	s.turnOn = false
	s.activity = s.mgr.now()
	s.mu.Unlock()
	if perr := s.mgr.persist(); perr != nil {
		s.setErr("persist: " + perr.Error())
	}
	return display, nil
}

// commitTranscript writes the session's current chat log to the durability
// sidecar (transcriptSidecarPath) and commits it on the edit branch, so an
// immediately-following push (runTurn, when a trusted remote exists) carries
// the transcript along with the proposed change. It is bookkeeping only —
// never part of the proposal — so merge() strips it again before ever
// publishing to main. Nothing-to-commit (the log serializes identically to
// what is already committed) is tolerated.
func (s *Session) commitTranscript(ctx context.Context) error {
	s.mu.Lock()
	logCopy := make([]Turn, len(s.log))
	copy(logCopy, s.log)
	s.mu.Unlock()
	b, err := json.MarshalIndent(logCopy, "", "  ")
	if err != nil {
		return err
	}
	abs := filepath.Join(s.wtPath, transcriptSidecarPath)
	if err := os.WriteFile(abs, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := s.wt.CommitPaths(ctx, "editor: session transcript", transcriptSidecarPath); err != nil && err != git.ErrNothing {
		return err
	}
	return nil
}

// stripTranscriptSidecar removes the durability sidecar (if present) from the
// edit worktree and commits the removal, so a publish (merge) never carries
// it into main. Already-absent is a no-op (a session that never had a trusted
// remote, so never wrote one).
func (s *Session) stripTranscriptSidecar(ctx context.Context) error {
	abs := filepath.Join(s.wtPath, transcriptSidecarPath)
	if _, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.Remove(abs); err != nil {
		return err
	}
	if err := s.wt.CommitPaths(ctx, "editor: drop session transcript before publish", transcriptSidecarPath); err != nil && err != git.ErrNothing {
		return err
	}
	return nil
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
	// The durability sidecar (chat-diff-session-durability), if this session
	// ever committed one, is bookkeeping for the IN-FLIGHT session only — it
	// must never reach main. Strip it from the branch before publishing; a
	// no-op when none was ever written (no trusted remote existed for this
	// session).
	if serr := s.stripTranscriptSidecar(ctx); serr != nil {
		return serr
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
