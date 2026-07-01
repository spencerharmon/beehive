package editor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sessionsFile is the basename of the editor session store, kept under the
// repo's gitignored .worktrees dir (co-located with the edit worktrees it
// tracks) so it is per-repo and never committed.
const sessionsFile = "editor-sessions.json"

// sessionRecord is the on-disk projection of one editor Session: exactly the
// state a beehived restart needs to resume the edit (or prune it) without the
// in-memory Manager. The live proposal itself lives in the edit worktree/branch
// on disk; this record links a worktree back to its file + publish target and
// records last activity for staleness.
type sessionRecord struct {
	ID       string    `json:"id"`
	File     string    `json:"file"`
	Branch   string    `json:"branch"`
	WtPath   string    `json:"wt_path"`
	Remote   string    `json:"remote,omitempty"`
	BaseMain string    `json:"base_main,omitempty"`
	Activity time.Time `json:"activity"`
	Log      []Turn    `json:"log,omitempty"`
}

// store is the small JSON persistence file behind a Manager. It is safe for
// concurrent callers (Open/chat/close all persist) via its own mutex; writes are
// atomic (temp file + rename) so a crash mid-write never truncates the store.
type store struct {
	path string
	mu   sync.Mutex
}

func newStore(path string) *store { return &store{path: path} }

// load reads the persisted records. A missing or empty store is not an error —
// it is a fresh install with no sessions yet (nil records). A present but
// malformed store surfaces as an error so corruption is never silently ignored.
func (st *store) load() ([]sessionRecord, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, err := os.ReadFile(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var recs []sessionRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("editor: parse session store %s: %w", st.path, err)
	}
	return recs, nil
}

// save atomically rewrites the store to exactly recs. It creates the parent
// (.worktrees) dir if needed and writes via a temp file + rename so a reader
// never observes a partial file. An empty set is written as `[]`, clearing the
// store rather than deleting it.
func (st *store) save(recs []sessionRecord) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if recs == nil {
		recs = []sessionRecord{}
	}
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}
