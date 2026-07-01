package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
)

// storeBasename is the persisted-session file name. It lives under the repo's git
// dir (see resolveStorePath): never committed, per-repo, and always writable.
const storeBasename = "beehive-editor-sessions.json"

// editBranchPrefix is the name prefix of every editor worktree branch (see
// Manager.Open: "edit-<slug>-<unix>"). Honeybee code worktrees are "bee-*" and
// the primary checkout is "main"; the startup prune only ever touches "edit-*".
const editBranchPrefix = "edit-"

// persistedSession is the on-disk record of one editor session. It carries only
// what a restart needs to rebuild a Session over its existing worktree: the live
// opencode connection (Session.oc) is intentionally NOT persisted — it is a
// process-local HTTP session and is recreated lazily on the next chat turn, in
// the same worktree, which still holds the in-progress edits on disk.
type persistedSession struct {
	ID         string    `json:"id"`
	File       string    `json:"file"`
	Branch     string    `json:"branch"`
	WtPath     string    `json:"wt_path"`
	Remote     string    `json:"remote"`
	BaseMain   string    `json:"base_main"`
	Log        []Turn    `json:"log"`
	LastActive time.Time `json:"last_active"`
}

// resolveStorePath returns the persisted-session file path for the repo at
// absRoot, under its git common dir (so every worktree of the repo shares one
// store and it is never committed). Returns "" when absRoot is not a git repo,
// which disables persistence rather than erroring at construction.
func resolveStorePath(absRoot string) string {
	d, err := git.New(absRoot).Run(context.Background(), "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(d) {
		d = filepath.Join(absRoot, d)
	}
	return filepath.Join(d, storeBasename)
}

// isEditBranch reports whether branch is an editor worktree branch (never a
// honeybee "bee-*" branch, "main", or a detached ("") worktree).
func isEditBranch(branch string) bool {
	return strings.HasPrefix(branch, editBranchPrefix)
}

// ttl is the staleness window for the startup prune, mirroring the swarm GC
// heartbeat TTL (config TTLMinutes, 60m fallback).
func (m *Manager) ttl() time.Duration {
	mins := m.cfg.TTLMinutes
	if mins <= 0 {
		mins = 60
	}
	return time.Duration(mins) * time.Minute
}

// snapshot copies the session's persistable state under its lock.
func (s *Session) snapshot() persistedSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := make([]Turn, len(s.log))
	copy(log, s.log)
	return persistedSession{
		ID:         s.ID,
		File:       s.File,
		Branch:     s.Branch,
		WtPath:     s.wtPath,
		Remote:     s.remote,
		BaseMain:   s.baseMain,
		Log:        log,
		LastActive: s.lastActive,
	}
}

// fromPersisted rebuilds a live Session over an existing worktree from a stored
// record. The opencode session stays nil (recreated on the next chat).
func (m *Manager) fromPersisted(p persistedSession) *Session {
	return &Session{
		ID:         p.ID,
		File:       p.File,
		Branch:     p.Branch,
		wtPath:     p.WtPath,
		remote:     p.Remote,
		baseMain:   p.BaseMain,
		client:     m.client,
		wt:         git.New(p.WtPath),
		mgr:        m,
		sys:        systemPrompt(p.File),
		log:        p.Log,
		lastActive: p.LastActive,
	}
}

// save writes every registered session to the store. Writes are serialized
// (saveMu) and atomic (temp + rename) so a crash mid-write cannot corrupt the
// file. Persistence is a best-effort resume cache — git worktrees/branches are
// the authoritative state — so a write error is returned to the caller to surface
// (in the panel or up the call stack), never silently dropped.
func (m *Manager) save() error {
	if m.storePath == "" {
		return nil
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.byID))
	for _, s := range m.byID {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	recs := make([]persistedSession, 0, len(sessions))
	for _, s := range sessions {
		recs = append(recs, s.snapshot())
	}
	return writeStore(m.storePath, recs)
}

// Resume rebuilds in-flight editor sessions and prunes stale edit worktrees at
// startup. For each "edit-*" worktree of the root repo it either keeps it (a
// fresh persisted session within TTL, OR a worktree with pending changes that
// must never be destroyed) and re-registers the session, or — when stale AND
// clean (nothing unpublished to lose) — reclaims it: `git worktree remove` +
// delete the branch, mirroring swarm gc-worktree-reclaim. Non-edit worktrees
// (honeybee "bee-*", the "main" checkout) are never touched. Records whose
// worktree no longer exists are dropped (and any dangling branch deleted). The
// surviving set is re-persisted. Call once, before serving.
func (m *Manager) Resume(ctx context.Context) error {
	recs, err := loadStore(m.storePath)
	if err != nil {
		return err
	}
	byBranch := make(map[string]persistedSession, len(recs))
	for _, r := range recs {
		byBranch[r.Branch] = r
	}
	wts, err := m.primary.Worktrees(ctx)
	if err != nil {
		return fmt.Errorf("editor resume: list worktrees: %w", err)
	}
	now := time.Now()
	ttl := m.ttl()
	seen := make(map[string]bool)
	for _, w := range wts {
		if !isEditBranch(w.Branch) {
			continue // never touch bee-*, main, or detached worktrees
		}
		seen[w.Branch] = true
		rec, hasRec := byBranch[w.Branch]
		fresh := hasRec && now.Sub(rec.LastActive) < ttl
		// Safety veto: a worktree with uncommitted or committed-but-unmerged
		// changes holds a pending proposal; never auto-destroy it regardless of
		// age (the "approved changes pending publish" guard).
		pending := m.worktreeHasPending(ctx, w.Path)
		if fresh || pending {
			if hasRec {
				s := m.fromPersisted(rec)
				m.mu.Lock()
				m.byID[s.ID] = s
				m.mu.Unlock()
			}
			continue
		}
		// Stale and clean: reclaim the orphaned worktree + branch. Best-effort on
		// each throwaway worktree (matches gc-worktree-reclaim / Manager.Close).
		_ = m.primary.WorktreeRemove(ctx, w.Path)
		_, _ = m.primary.Run(ctx, "branch", "-D", w.Branch)
	}
	// A record whose worktree is already gone: drop it, and best-effort delete a
	// dangling branch so it does not accumulate.
	for _, r := range recs {
		if !seen[r.Branch] {
			_, _ = m.primary.Run(ctx, "branch", "-D", r.Branch)
		}
	}
	return m.save()
}

// worktreeHasPending reports whether the worktree at wtPath holds changes not yet
// on main: an unclean working tree (uncommitted edits) or a branch ahead of local
// main (a committed proposal awaiting merge/publish). On any uncertainty (git
// error, unresolved main) it returns true — the prune must never destroy a
// worktree it cannot prove is safe to reclaim.
func (m *Manager) worktreeHasPending(ctx context.Context, wtPath string) bool {
	wt := git.New(wtPath)
	clean, err := wt.Clean(ctx)
	if err != nil {
		return true
	}
	if !clean {
		return true
	}
	out, err := wt.Run(ctx, "rev-list", "main..HEAD")
	if err != nil {
		return true
	}
	return strings.TrimSpace(out) != ""
}

// loadStore reads the persisted-session records. A missing or empty store is an
// empty set (first run), not an error; an unreadable or malformed store is a real
// error (surfaced at startup, never guessed past).
func loadStore(path string) ([]persistedSession, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("editor: read session store %s: %w", path, err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, nil
	}
	var recs []persistedSession
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("editor: parse session store %s: %w", path, err)
	}
	return recs, nil
}

// writeStore atomically writes recs to path (temp file + rename), creating the
// parent dir if needed.
func writeStore(path string, recs []persistedSession) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
