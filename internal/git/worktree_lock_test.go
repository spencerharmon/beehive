package git

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestWorktreeLockSerializesAndCancels(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	g := New(dir)
	unlock, err := g.LockWorktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if release, err := New(dir).LockWorktree(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("second writer must wait/cancel: %v", err)
	}
	unlock()
	release, err := New(dir).LockWorktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}
