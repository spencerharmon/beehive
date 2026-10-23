package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockWorktree serializes a short read/write/commit transaction with another
// process using this same private worktree (not other swarm worktrees). The OS
// releases the advisory lock if either process exits unexpectedly.
func (r *Repo) LockWorktree(ctx context.Context) (func(), error) {
	dir, err := r.Run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "beehive-bookkeeping.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
