package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// runIn runs a git command in dir, failing the test on error (test helper).
func runIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// TestReconcileMainOnceDrainsLocalAhead exercises the full beehived reconcile
// wrapper: a commit stranded on local main (the crash-window state) is drained to
// origin/main, and reconcileErr stays nil.
func TestReconcileMainOnceDrainsLocalAhead(t *testing.T) {
	s, root := setup(t)
	ctx := context.Background()
	runIn(t, root, "checkout", "-B", "main")
	runIn(t, root, "add", "-A")
	runIn(t, root, "commit", "-m", "init")

	bare := filepath.Join(t.TempDir(), "origin.git")
	if _, err := s.git.Run(ctx, "init", "--bare", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	if _, err := s.git.Run(ctx, "remote", "add", "origin", bare); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if _, err := s.git.Run(ctx, "push", "origin", "main"); err != nil {
		t.Fatalf("push main: %v", err)
	}
	// Strand a commit on local main only (never pushed) — the direct-on-primary
	// crash window.
	if err := os.WriteFile(filepath.Join(root, "stranded.txt"), []byte("flip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.git.CommitPaths(ctx, "plan: stranded flip", "stranded.txt"); err != nil {
		t.Fatalf("commit stranded: %v", err)
	}
	localTip, _ := s.git.RevParse(ctx, "HEAD")

	s.reconcileMainOnce(ctx)

	s.syncMu.Lock()
	rerr := s.reconcileErr
	s.syncMu.Unlock()
	if rerr != nil {
		t.Fatalf("reconcile recorded an error draining a clean local-ahead state: %v", rerr)
	}
	// origin/main now carries the stranded commit.
	out, err := s.git.Run(ctx, "ls-remote", "origin", "refs/heads/main")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if len(out) < 40 || out[:40] != localTip {
		t.Fatalf("stranded commit not drained to origin: ls-remote=%q want prefix %s", out, localTip)
	}
}

// TestReconcileMainOnceRecordsConflict: a conflicting fork is surfaced into
// reconcileErr (loud, not swallowed) and origin/main is left untouched.
func TestReconcileMainOnceRecordsConflict(t *testing.T) {
	s, root := setup(t)
	ctx := context.Background()
	runIn(t, root, "checkout", "-B", "main")
	runIn(t, root, "add", "-A")
	runIn(t, root, "commit", "-m", "init")

	bare := filepath.Join(t.TempDir(), "origin.git")
	if _, err := s.git.Run(ctx, "init", "--bare", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	if _, err := s.git.Run(ctx, "remote", "add", "origin", bare); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	// Seed a shared file and push the base.
	if err := os.WriteFile(filepath.Join(root, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.git.CommitPaths(ctx, "base shared", "shared.txt"); err != nil {
		t.Fatalf("commit base: %v", err)
	}
	if _, err := s.git.Run(ctx, "push", "origin", "main"); err != nil {
		t.Fatalf("push base: %v", err)
	}

	// Advance origin down one line (a peer), conflicting on the same file...
	worktree := filepath.Join(t.TempDir(), "peer")
	if _, err := s.git.Run(ctx, "clone", bare, worktree); err != nil {
		t.Fatalf("clone peer: %v", err)
	}
	runIn(t, worktree, "config", "user.email", "t@t")
	runIn(t, worktree, "config", "user.name", "t")
	runIn(t, worktree, "checkout", "main")
	os.WriteFile(filepath.Join(worktree, "shared.txt"), []byte("remote-side\n"), 0o644)
	runIn(t, worktree, "commit", "-am", "remote edit")
	runIn(t, worktree, "push", "origin", "main")

	// ...and edit the same file on local main (the fork's other line).
	os.WriteFile(filepath.Join(root, "shared.txt"), []byte("local-side\n"), 0o644)
	if err := s.git.CommitPaths(ctx, "local edit", "shared.txt"); err != nil {
		t.Fatalf("commit local edit: %v", err)
	}
	// The legitimate remote tip the reconcile must NOT force past on a conflict.
	remoteBefore, _ := s.git.Run(ctx, "ls-remote", "origin", "refs/heads/main")

	s.reconcileMainOnce(ctx)

	s.syncMu.Lock()
	rerr := s.reconcileErr
	s.syncMu.Unlock()
	if rerr == nil {
		t.Fatal("conflicting fork was not recorded in reconcileErr (silently swallowed)")
	}
	if c, _ := s.git.HasConflict(ctx); c {
		t.Fatal("reconcile left a half-merged conflicted state on the primary")
	}
	afterRemote, _ := s.git.Run(ctx, "ls-remote", "origin", "refs/heads/main")
	if afterRemote != remoteBefore {
		t.Fatalf("origin/main mutated on a conflicting fork: before=%q after=%q", remoteBefore, afterRemote)
	}
}

// TestReconcileMainOnceNoRemoteIsSafe: a local-only hive (no remote) reconcile is
// a pure no-op and never records an error.
func TestReconcileMainOnceNoRemoteIsSafe(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	s.reconcileMainOnce(ctx)
	s.syncMu.Lock()
	rerr := s.reconcileErr
	s.syncMu.Unlock()
	if rerr != nil {
		t.Fatalf("no-remote reconcile recorded an error: %v", rerr)
	}
}

// TestStartMainReconcilersStops: the background reconcilers exit when their
// context is cancelled (no goroutine leak / no panic).
func TestStartMainReconcilersStops(t *testing.T) {
	s, _ := setup(t)
	s.reconcileIvl = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	s.StartMainReconcilers(ctx)
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond) // let the ticker loop observe ctx.Done and return
}
