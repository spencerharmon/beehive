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

// RemoteConfig returns the repo's full `remote.*` config block, one "key value"
// per line (empty when there are no remotes). It is a snapshot of the repo's
// remote configuration, used to detect and revert any change to it. git config
// is shared across all worktrees of a repo, so a stray `git remote add` an agent
// runs in its worktree leaks into the live repo; comparing against this snapshot
// lets the runner revert that drift.
func (r *Repo) RemoteConfig(ctx context.Context) (string, error) {
	// `--get-regexp` exits 1 when nothing matches (no remotes). That is not an
	// error for a snapshot, so distinguish it from a real failure by checking the
	// remote list first.
	names, err := r.Run(ctx, "remote")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(names) == "" {
		return "", nil
	}
	out, err := r.Run(ctx, "config", "--get-regexp", "^remote\\.")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RestoreRemotes resets the repo's remotes to a prior snapshot (from
// RemoteConfig): it removes any remote added since and re-creates any removed,
// so the repo's remote configuration is exactly the snapshot. Honeybees must
// never mutate the shared repo config; the runner calls this to revert any
// config drift an agent introduced. It is a no-op when nothing changed, and is
// idempotent against the snapshot (safe to call concurrently from peers that
// share the same baseline).
func (r *Repo) RestoreRemotes(ctx context.Context, snapshot string) error {
	cur, err := r.RemoteConfig(ctx)
	if err != nil {
		return err
	}
	if cur == snapshot {
		return nil
	}
	// Drop every current remote section, then replay the snapshot's keys verbatim.
	names, err := r.Run(ctx, "remote")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(names) {
		if _, err := r.Run(ctx, "config", "--remove-section", "remote."+name); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(snapshot, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			return fmt.Errorf("malformed remote config line %q", line)
		}
		if _, err := r.Run(ctx, "config", key, val); err != nil {
			return err
		}
	}
	return nil
}

// Fetch updates the remote-tracking ref for branch from remote, pruning the
// tracking ref if branch was deleted upstream. The explicit refspec scopes
// --prune to this branch only, so concurrent fetches of other refs are
// untouched. Callers read the advanced remote-tracking ref (e.g. origin/main)
// and fast-forward or re-verify; Fetch never moves the current branch.
func (r *Repo) Fetch(ctx context.Context, remote, branch string) error {
	_, err := r.Run(ctx, "fetch", remote, branch, "--prune")
	return err
}

// Pull fast-forwards the current branch to remote/branch. --ff-only forbids a
// merge commit: a divergent history errors instead of merging. The swarm
// converges by fast-forward + re-verify, so callers (e.g. claim) treat a
// non-fast-forward pull as a lost race rather than reconciling locally.
func (r *Repo) Pull(ctx context.Context, remote, branch string) error {
	_, err := r.Run(ctx, "pull", "--ff-only", remote, branch)
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
		// Pushing to the local repo's checked-out main requires updateInstead, else
		// it is rejected outright. Ensure it (idempotent, repo-level, shared by all
		// worktrees) so single-host (no-remote) honeybees can publish.
		_, _ = r.Run(ctx, "config", "receive.denyCurrentBranch", "updateInstead")
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

// UpdateLocalMain advances the repo's local main branch (and the primary
// working tree) to the current worktree branch's tip. main is checked out in the
// primary worktree, so this uses a push to "." with receive.denyCurrentBranch=
// updateInstead (ensured here, idempotent). A non-fast-forward is retried after
// merging the advanced local main; any other rejection (e.g. a dirty primary
// working tree) is returned verbatim so the caller can surface it.
func (r *Repo) UpdateLocalMain(ctx context.Context) error {
	_, _ = r.Run(ctx, "config", "receive.denyCurrentBranch", "updateInstead")
	for attempt := 0; attempt < 8; attempt++ {
		_, err := r.Run(ctx, "push", ".", "HEAD:refs/heads/main")
		if err == nil {
			return nil
		}
		if !isNonFastForward(err) {
			return err
		}
		if _, err := r.Run(ctx, "merge", "--no-edit", "main"); err != nil {
			_, _ = r.Run(ctx, "merge", "--abort")
			return ErrConflict
		}
	}
	return fmt.Errorf("git: update local main exhausted retries")
}

func isNonFastForward(err error) bool {
	s := err.Error()
	// Only a genuine non-fast-forward is worth retrying after a re-merge. Other
	// rejections (protected branch, no push permission, "refusing to update
	// checked out branch") must surface, not loop — they all contain "rejected".
	return strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "fetch first") ||
		strings.Contains(s, "tip of your current branch is behind")
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
