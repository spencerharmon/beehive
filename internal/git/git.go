// Package git is a thin exec wrapper around the git binary. Beehive shells out
// rather than linking go-git so submodule, gpg, and worktree behavior matches
// the system git exactly. All writes happen in worktrees, never the shared checkout.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrConflict is returned when a merge produces conflicts.
var ErrConflict = errors.New("git: merge conflict")

// Repo is a git working tree rooted at Dir.
type Repo struct{ Dir string }

// New returns a Repo rooted at dir.
func New(dir string) *Repo { return &Repo{Dir: dir} }

// Run executes git with args in the repo dir, returning trimmed stdout.
func (r *Repo) Run(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), fmt.Errorf("git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Head returns the short SHA of HEAD.
func (r *Repo) Head(ctx context.Context) (string, error) {
	return r.Run(ctx, "rev-parse", "--short", "HEAD")
}

// CurrentBranch returns the checked-out branch name.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	return r.Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
}

// Commit stages everything and commits with msg. ErrNothing if tree clean.
var ErrNothing = errors.New("git: nothing to commit")

func (r *Repo) Commit(ctx context.Context, msg string) error {
	if _, err := r.Run(ctx, "add", "-A"); err != nil {
		return err
	}
	clean, err := r.Clean(ctx)
	if err != nil {
		return err
	}
	if clean {
		return ErrNothing
	}
	_, err = r.Run(ctx, "commit", "-m", msg)
	return err
}

// Clean reports whether the working tree has no staged or unstaged changes.
func (r *Repo) Clean(ctx context.Context) (bool, error) {
	out, err := r.Run(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// Merge merges ref into the current branch. Returns ErrConflict on conflict.
func (r *Repo) Merge(ctx context.Context, ref string) error {
	if _, err := r.Run(ctx, "merge", "--no-edit", ref); err != nil {
		if c, _ := r.HasConflict(ctx); c {
			return ErrConflict
		}
		return err
	}
	return nil
}

// HasConflict reports whether the tree has unmerged paths.
func (r *Repo) HasConflict(ctx context.Context) (bool, error) {
	out, err := r.Run(ctx, "ls-files", "-u")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// LastCommit returns the full SHA of the last commit touching path.
func (r *Repo) LastCommit(ctx context.Context, path string) (string, error) {
	return r.Run(ctx, "log", "-1", "--format=%H", "--", path)
}

// WorktreeAdd creates a worktree at dir on a new branch off base. It is
// idempotent: any stale worktree at dir or pre-existing branch from a crashed
// run is pruned/discarded first so a honeybee can always claim a fresh tree.
func (r *Repo) WorktreeAdd(ctx context.Context, dir, branch, base string) error {
	_, _ = r.Run(ctx, "worktree", "remove", "--force", dir)
	_, _ = r.Run(ctx, "worktree", "prune")
	_, _ = r.Run(ctx, "branch", "-D", branch)
	_, err := r.Run(ctx, "worktree", "add", "-b", branch, dir, base)
	return err
}

// WorktreeRemove removes the worktree at dir, force-discarding changes.
func (r *Repo) WorktreeRemove(ctx context.Context, dir string) error {
	_, err := r.Run(ctx, "worktree", "remove", "--force", dir)
	return err
}
