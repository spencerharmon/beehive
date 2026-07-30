package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
)

// resolveBeehiveWorktree prefers $BEEHIVE_WORKTREE, else falls back to the
// enclosing beehive repo root (findRoot).
func TestResolveBeehiveWorktree(t *testing.T) {
	root, _ := newHive(t)

	t.Setenv("BEEHIVE_WORKTREE", "/explicit/hive")
	t.Setenv("SUBMODULE_WORKTREE", "")
	if got, err := resolveBeehiveWorktree(); err != nil || got != "/explicit/hive" {
		t.Fatalf("env override: got %q err %v", got, err)
	}

	os.Unsetenv("BEEHIVE_WORKTREE")
	inDir(t, root, func() {
		got, err := resolveBeehiveWorktree()
		if err != nil {
			t.Fatal(err)
		}
		// findRoot may resolve through symlinks (e.g. /tmp -> /private/tmp); compare
		// by base identity via EvalSymlinks.
		if !samePath(t, got, root) {
			t.Fatalf("fallback: got %q want %q", got, root)
		}
	})
}

// resolveSubmoduleWorktree order: $SUBMODULE_WORKTREE, then the per-pass env
// file, then the sole .submodule-worktrees/<sm>/<branch> dir; ambiguity errors.
func TestResolveSubmoduleWorktree(t *testing.T) {
	root, _ := newHive(t)

	// (1) explicit env wins.
	t.Setenv("SUBMODULE_WORKTREE", "/explicit/code")
	inDir(t, root, func() {
		if got, err := resolveSubmoduleWorktree(); err != nil || got != "/explicit/code" {
			t.Fatalf("env override: got %q err %v", got, err)
		}
	})

	// (2) per-pass file wins when env is unset.
	os.Unsetenv("SUBMODULE_WORKTREE")
	fileWT := filepath.Join(root, ".submodule-worktrees", "flux", "bee-a")
	writeFileMW(t, root, repo.WorktreeEnvFile,
		"BEEHIVE_WORKTREE="+root+"\nSUBMODULE_WORKTREE="+fileWT+"\n")
	inDir(t, root, func() {
		if got, err := resolveSubmoduleWorktree(); err != nil || got != fileWT {
			t.Fatalf("file channel: got %q want %q err %v", got, fileWT, err)
		}
	})

	// (3) sole-worktree derivation when neither env nor file names it.
	os.Remove(filepath.Join(root, repo.WorktreeEnvFile))
	sole := repo.SubmoduleWorktreePath(root, "flux", "bee-only")
	if err := os.MkdirAll(sole, 0o755); err != nil {
		t.Fatal(err)
	}
	inDir(t, root, func() {
		if got, err := resolveSubmoduleWorktree(); err != nil || !samePath(t, got, sole) {
			t.Fatalf("sole derivation: got %q want %q err %v", got, sole, err)
		}
	})

	// (4) ambiguity (two worktrees) errors rather than guessing.
	if err := os.MkdirAll(repo.SubmoduleWorktreePath(root, "gostream", "bee-two"), 0o755); err != nil {
		t.Fatal(err)
	}
	inDir(t, root, func() {
		if _, err := resolveSubmoduleWorktree(); err == nil {
			t.Fatal("ambiguous submodule worktrees must error")
		}
	})
}

// samePath compares two paths after resolving symlinks so /tmp vs /private/tmp
// (macOS) or other symlinked temp roots don't cause spurious mismatches.
func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}
