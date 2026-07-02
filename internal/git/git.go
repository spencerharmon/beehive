// Package git is a thin exec wrapper around the git binary. Beehive shells out
// rather than linking go-git so submodule, gpg, and worktree behavior matches
// the system git exactly. All writes happen in worktrees, never the shared checkout.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// Exists reports whether path is present at ref. It is the boolean form of Show:
// a present (even empty) blob -> true; a path absent at ref -> false. Used to
// validate an edit base before cutting a worktree from it, so an existing file
// rendered as a whole-file deletion (because the base lacks it) is caught.
func (r *Repo) Exists(ctx context.Context, ref, path string) bool {
	_, err := r.Run(ctx, "show", ref+":"+path)
	return err == nil
}

// SharesHistory reports whether refs a and b descend from a common ancestor,
// i.e. they belong to the SAME project history. The editor uses it to tell the
// repo's OWN remote (whose main shares history with local main) from a foreign /
// unrelated one before trusting `<remote>/main` as an edit base over local main.
//
// Either ref failing to resolve, or the two histories being unrelated (git
// merge-base exits non-zero with no output), both mean "not provably the same
// history" and yield (false, nil) — the safe signal to fall back to local main.
// Only an unexpected failure resolving a ref that does exist surfaces an error.
func (r *Repo) SharesHistory(ctx context.Context, a, b string) (bool, error) {
	if _, err := r.RevParse(ctx, a); err != nil {
		return false, nil
	}
	if _, err := r.RevParse(ctx, b); err != nil {
		return false, nil
	}
	// Both refs resolve; merge-base now fails ONLY when the histories are
	// unrelated (no common ancestor) — the foreign-remote signal, not a fault.
	out, err := r.Run(ctx, "merge-base", a, b)
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(out) != "", nil
}

// IsAncestor reports whether commit ancestor is contained in the history of
// commit descendant (the test `git merge-base --is-ancestor`). It is how the
// runner confirms a publish ACTUALLY reached the shared main: the local commit
// the completion check validated must be an ancestor of the fetched origin main,
// so a local-only commit whose push never landed is provably not on main. A
// commit is its own ancestor, so an exact match yields true. git exits 0 for
// true and a documented 1 for false; only 1 maps to (false, nil), while any
// other failure (e.g. an unresolvable ref, exit 128) surfaces as an error rather
// than a silent false.
func (r *Repo) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	_, err := r.Run(ctx, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
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
	healed := false
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
		if isNonFastForward(err) {
			// main advanced under us; merge it in and retry.
			ref := "main"
			if remote != "" {
				ref = remote + "/main"
			}
			if _, merr := r.Run(ctx, "merge", "--no-edit", ref); merr != nil {
				_, _ = r.Run(ctx, "merge", "--abort")
				return ErrConflict
			}
			continue
		}
		// Local checked-out main refused the push because its working tree is
		// dirty (updateInstead will not overwrite a dirty tree). That tree is a
		// pure projection of committed history — honeybees author only in their
		// own worktrees and publish by pushing — so any dirtiness is derived
		// drift, most commonly a submodule checkout lagging its just-bumped
		// gitlink because updateInstead does not recurse submodules. Reset the
		// projection tree to HEAD and re-materialize submodules, then retry once.
		// Never silent: if the tree cannot be made clean, surface the error.
		if target == "." && !healed && isDirtyTreeRejection(err) {
			healed = true
			if herr := r.healLocalMain(ctx); herr != nil {
				return fmt.Errorf("git: publish blocked by dirty local main and heal failed: %w", herr)
			}
			continue
		}
		return err
	}
	return fmt.Errorf("git: publish to main exhausted retries")
}

// isDirtyTreeRejection reports whether a push to a local checked-out branch was
// refused because updateInstead found the target working tree dirty (versus a
// non-fast-forward race or a permission/protection rejection).
func isDirtyTreeRejection(err error) bool {
	s := err.Error()
	return strings.Contains(s, "Working directory has") || // "...unstaged/staged changes"
		strings.Contains(s, "would be overwritten") ||
		strings.Contains(s, "needs merge") ||
		strings.Contains(s, "is not up to date")
}

// healLocalMain restores the main worktree (the updateInstead push target) to a
// clean projection of its committed HEAD so the next push is accepted. It resets
// tracked-file drift and force re-checks-out every .gitmodules-declared submodule
// to its recorded gitlink (iterating only declared paths so an orphan gitlink —
// a committed honeybee worktree with no .gitmodules URL — cannot fatal the sync).
// Returns an error (never swallows) if the tree is still dirty afterward.
func (r *Repo) healLocalMain(ctx context.Context) error {
	dir, err := r.mainWorktreeDir(ctx)
	if err != nil {
		return err
	}
	m := &Repo{Dir: dir}
	if _, err := m.Run(ctx, "reset", "--hard", "HEAD"); err != nil {
		return err
	}
	_, _ = m.Run(ctx, "submodule", "sync", "--quiet")
	paths, err := m.declaredSubmodulePaths(ctx)
	if err != nil {
		return err
	}
	var failed []string
	for _, p := range paths {
		// --init in case the checkout was never populated; --force to discard the
		// stale checkout updateInstead left behind. submodule update fetches the
		// recorded commit into the submodule when it is missing.
		if _, err := m.Run(ctx, "submodule", "update", "--init", "--force", "--", p); err != nil {
			failed = append(failed, p)
		}
	}
	out, serr := m.Run(ctx, "status", "--porcelain")
	if serr != nil {
		return serr
	}
	if strings.TrimSpace(out) != "" {
		if len(failed) > 0 {
			return fmt.Errorf("git: main worktree still dirty after heal (submodule resync failed for %s)", strings.Join(failed, ", "))
		}
		return fmt.Errorf("git: main worktree still dirty after heal: %s", strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	}
	return nil
}

// mainWorktreeDir returns the path of the worktree that has main checked out
// (the target updateInstead pushes into), found from `git worktree list`.
func (r *Repo) mainWorktreeDir(ctx context.Context) (string, error) {
	wts, err := r.Worktrees(ctx)
	if err != nil {
		return "", err
	}
	for _, w := range wts {
		if w.Branch == "main" {
			return w.Path, nil
		}
	}
	if len(wts) > 0 {
		return wts[0].Path, nil // primary worktree is listed first
	}
	return "", fmt.Errorf("git: no worktrees listed")
}

// declaredSubmodulePaths returns the submodule paths declared in .gitmodules
// (none when there is no .gitmodules), so callers can sync real submodules
// without tripping over orphan gitlinks that have no declaration.
func (r *Repo) declaredSubmodulePaths(ctx context.Context) ([]string, error) {
	if _, statErr := os.Stat(filepath.Join(r.Dir, ".gitmodules")); statErr != nil {
		return nil, nil
	}
	out, err := r.Run(ctx, "config", "-f", ".gitmodules", "--get-regexp", `\.path$`)
	if err != nil {
		return nil, nil // no entries
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		// "submodule.<name>.path <path>"
		if i := strings.LastIndex(line, " "); i >= 0 {
			if p := strings.TrimSpace(line[i+1:]); p != "" {
				paths = append(paths, p)
			}
		}
	}
	return paths, nil
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
	healed := false
	for attempt := 0; attempt < 8; attempt++ {
		_, err := r.Run(ctx, "push", ".", "HEAD:refs/heads/main")
		if err == nil {
			return nil
		}
		if isNonFastForward(err) {
			if _, merr := r.Run(ctx, "merge", "--no-edit", "main"); merr != nil {
				_, _ = r.Run(ctx, "merge", "--abort")
				return ErrConflict
			}
			continue
		}
		// Dirty projection tree (e.g. submodule drift); reset it and retry once.
		if !healed && isDirtyTreeRejection(err) {
			healed = true
			if herr := r.healLocalMain(ctx); herr != nil {
				return fmt.Errorf("git: update local main blocked by dirty tree and heal failed: %w", herr)
			}
			continue
		}
		return err
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
