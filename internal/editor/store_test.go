package editor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStoreRoundTripMissingAndClear covers the persistence layer directly: a
// missing store loads empty (fresh install), a saved set round-trips (including
// the chat log), the parent dir is created, saving empty clears to `[]` rather
// than deleting, and the atomic write leaves no temp file behind.
func TestStoreRoundTripMissingAndClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".worktrees", sessionsFile)
	st := newStore(path)

	recs, err := st.load()
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("missing store should be empty, got %d", len(recs))
	}

	want := []sessionRecord{{
		ID:       "edit-a-1",
		File:     "submodules/x/ROI.md",
		Branch:   "edit-a-1",
		WtPath:   filepath.Join(dir, ".worktrees", "edit-a-1"),
		Remote:   "origin",
		BaseMain: "deadbeef",
		Activity: time.Unix(1000, 0).UTC(),
		Log:      []Turn{{Role: "user", Text: "hi", At: time.Unix(1000, 0).UTC()}},
	}}
	if err := st.save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}

	got, err := st.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	r := got[0]
	if r.ID != "edit-a-1" || r.File != "submodules/x/ROI.md" || r.Branch != "edit-a-1" ||
		r.Remote != "origin" || r.BaseMain != "deadbeef" || !r.Activity.Equal(want[0].Activity) {
		t.Fatalf("round-trip mismatch: %+v", r)
	}
	if len(r.Log) != 1 || r.Log[0].Role != "user" || r.Log[0].Text != "hi" {
		t.Fatalf("log not round-tripped: %+v", r.Log)
	}

	// Clear: saving nil writes an empty set (store persists, loads empty).
	if err := st.save(nil); err != nil {
		t.Fatalf("save empty: %v", err)
	}
	got, err = st.load()
	if err != nil {
		t.Fatalf("load after clear: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cleared store should load empty, got %d", len(got))
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("atomic temp file leaked: %v", err)
	}
}

// TestStoreLoadMalformedErrors confirms a corrupt store is a surfaced error, not
// a silently-ignored empty recovery.
func TestStoreLoadMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, sessionsFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newStore(path).load(); err == nil {
		t.Fatal("malformed store should error on load, got nil")
	}
}
