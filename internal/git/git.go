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

// CommitPaths stages and commits only the given pathspecs, leaving any other
// working-tree changes untouched. Each honeybee works in its own beehive-repo
// worktree (its own index), so there is no cross-process lock contention; this
// keeps a commit scoped to exactly the files a step produced. ErrNothing if none
// of the paths changed.
func (r *Repo) CommitPaths(ctx context.Context, msg string, paths ...string) error {
	if len(paths) == 0 {
		return ErrNothing
	}
	args := append([]string{"add", "--"}, paths...)
	if _, err := r.Run(ctx, args...); err != nil {
		return err
	}
	statusArgs := append([]string{"status", "--porcelain", "--"}, paths...)
	out, err := r.Run(ctx, statusArgs...)
	if err != nil {
		return err
	}
	if out == "" {
		return ErrNothing
	}
	commitArgs := append([]string{"commit", "-m", msg, "--"}, paths...)
	_, err = r.Run(ctx, commitArgs...)
	return err
}

// Remote returns the name of the repo's default push remote ("origin" if
// present, else the first configured remote), or "" when the repo has none.
func (r *Repo) Remote(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "remote")
	if err != nil {
		return "", err
	}
	names := strings.Fields(out)
	for _, n := range names {
		if n == "origin" {
			return "origin", nil
		}
	}
	if len(names) > 0 {
		return names[0], nil
	}
	return "", nil
}

// Fetch updates remote-tracking refs for branch from remote.
func (r *Repo) Fetch(ctx context.Context, remote, branch string) error {
	_, err := r.Run(ctx, "fetch", remote, branch)
	return err
}

// Push pushes refspec to remote.
func (r *Repo) Push(ctx context.Context, remote, refspec string) error {
	_, err := r.Run(ctx, "push", remote, refspec)
	return err
}

// HardReset discards the worktree and index to ref.
func (r *Repo) HardReset(ctx context.Context, ref string) error {
	_, err := r.Run(ctx, "reset", "--hard", ref)
	return err
}

// RevParse resolves ref to a full commit SHA.
func (r *Repo) RevParse(ctx context.Context, ref string) (string, error) {
	out, err := r.Run(ctx, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Show returns the contents of path at ref (git show ref:path). An error means
// the path does not exist at that ref (e.g. the file was deleted).
func (r *Repo) Show(ctx context.Context, ref, path string) (string, error) {
	return r.Run(ctx, "show", ref+":"+path)
}

// DiffPaths reports whether path changed between commits a and b.
func (r *Repo) DiffPaths(ctx context.Context, a, b, path string) (bool, error) {
	out, err := r.Run(ctx, "diff", "--name-only", a, b, "--", path)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// PublishToMain advances main to the current worktree branch's tip and pushes,
// the conflict-free way honeybees converge. It merges the latest main into the
// branch first (distinct session/plan files auto-merge), then updates main. With
// a remote it fetches+merges+pushes remote main; without one it updates local
// main (requires receive.denyCurrentBranch=updateInstead, set by `beehive init`).
// Concurrent publishers race only on the main ref: a non-fast-forward push is
// retried after re-merging the advanced main, never blocking on a write lock.
func (r *Repo) PublishToMain(ctx context.Context, remote string) error {
	target := remote
	if target == "" {
		target = "."
	}
	for attempt := 0; attempt < 8; attempt++ {
		if remote != "" {
			if err := r.Fetch(ctx, remote, "main"); err != nil {
				return err
			}
			if _, err := r.Run(ctx, "merge", "--no-edit", "FETCH_HEAD"); err != nil {
				_, _ = r.Run(ctx, "merge", "--abort")
				return ErrConflict
			}
		}
		_, err := r.Run(ctx, "push", target, "HEAD:refs/heads/main")
		if err == nil {
			return nil
		}
		if !isNonFastForward(err) {
			return err
		}
		// main advanced under us; merge it in and retry.
		ref := "main"
		if remote != "" {
			ref = remote + "/main"
		}
		if _, err := r.Run(ctx, "merge", "--no-edit", ref); err != nil {
			_, _ = r.Run(ctx, "merge", "--abort")
			return ErrConflict
		}
	}
	return fmt.Errorf("git: publish to main exhausted retries")
}

// Worktree is one entry from `git worktree list`.
type Worktree struct {
	Path   string
	Branch string
	HEAD   string
}

// Worktrees lists the repo's worktrees (including the primary).
func (r *Repo) Worktrees(ctx context.Context) ([]Worktree, error) {
	out, err := r.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	var cur Worktree
	flush := func() {
		if cur.Path != "" {
			wts = append(wts, cur)
		}
		cur = Worktree{}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			cur.HEAD = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush()
	return wts, nil
}

func isNonFastForward(err error) bool {
	s := err.Error()
	return strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "fetch first") ||
		strings.Contains(s, "rejected")
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
