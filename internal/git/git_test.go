package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	r := New(dir)
	ctx := context.Background()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if _, err := r.Run(ctx, a...); err != nil {
			t.Fatalf("setup %v: %v", a, err)
		}
	}
	return r
}

func TestCommitAndClean(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()
	if c, _ := r.Clean(ctx); !c {
		t.Fatal("fresh repo not clean")
	}
	if err := r.Commit(ctx, "m"); err != ErrNothing {
		t.Fatalf("want ErrNothing, got %v", err)
	}
	os.WriteFile(filepath.Join(r.Dir, "a"), []byte("x"), 0o644)
	if err := r.Commit(ctx, "add a"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if c, _ := r.Clean(ctx); !c {
		t.Fatal("tree dirty after commit")
	}
	if _, err := r.LastCommit(ctx, "a"); err != nil {
		t.Fatalf("lastcommit: %v", err)
	}
}

func TestMergeConflict(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(r.Dir, "f"), []byte("base\n"), 0o644)
	r.Commit(ctx, "base")
	r.Run(ctx, "checkout", "-b", "x")
	os.WriteFile(filepath.Join(r.Dir, "f"), []byte("x\n"), 0o644)
	r.Commit(ctx, "x")
	r.Run(ctx, "checkout", "main")
	os.WriteFile(filepath.Join(r.Dir, "f"), []byte("main\n"), 0o644)
	r.Commit(ctx, "main")
	if err := r.Merge(ctx, "x"); err != ErrConflict {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestIsNonFastForward(t *testing.T) {
	retry := []string{
		"! [rejected] main -> main (non-fast-forward)",
		"Updates were rejected because a pushed branch tip is behind its remote counterpart. fetch first",
		"tip of your current branch is behind its remote",
	}
	for _, s := range retry {
		if !isNonFastForward(fmt.Errorf("%s", s)) {
			t.Errorf("want retryable: %q", s)
		}
	}
	// These rejections are terminal: looping is wrong, the real error must surface.
	noRetry := []string{
		"remote: error: GH006: Protected branch update failed for refs/heads/main. ! [remote rejected] (protected branch hook declined)",
		"remote rejected: refusing to update checked out branch refs/heads/main",
		"Permission denied (publickey). ! [rejected]",
	}
	for _, s := range noRetry {
		if isNonFastForward(fmt.Errorf("%s", s)) {
			t.Errorf("want terminal (not retryable): %q", s)
		}
	}
}
