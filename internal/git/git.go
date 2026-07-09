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

// CommitStaged commits whatever is already staged in the index with msg, WITHOUT
// re-reading the working tree (no `add`, no pathspec). It is the safe way to
// record a `git rm --cached` removal of a path whose working-tree directory is
// still a live nested checkout (a code worktree): staging that path by pathspec
// (`git add -- <path>`, as CommitPaths does) would re-add the live checkout as a
// gitlink and undo the removal. Committing the staged index directly records only
// the removal. ErrNothing when the index matches HEAD (nothing staged).
func (r *Repo) CommitStaged(ctx context.Context, msg string) error {
	staged, err := r.Run(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	if strings.TrimSpace(staged) == "" {
		return ErrNothing
	}
	_, err = r.Run(ctx, "commit", "-m", msg)
	return err
}

// RemoveCached drops paths from the index without touching the working tree
// (`git rm --cached`). --ignore-unmatch keeps it a no-op for an already-absent
// path and -q suppresses the per-file listing. The working-tree directory (which
// for an orphan worktree gitlink is a live nested checkout) is left untouched;
// only the tracked index entry is removed. A no-op for an empty path list.
func (r *Repo) RemoveCached(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"rm", "--cached", "-q", "--ignore-unmatch", "--"}, paths...)
	_, err := r.Run(ctx, args...)
	return err
}

// OrphanWorktreeGitlinks returns the tracked gitlink paths (index mode 160000)
// that live under submodules/<sm>/worktrees/ and are NOT declared submodules in
// .gitmodules — i.e. honeybee code-worktrees that leaked into the beehive index
// as orphan gitlinks. Such an entry has no submodule URL and wedges
// `git submodule update`, so the runner sweeps it. Declared submodules (real
// gitlinks) and any gitlink outside a worktrees/ path are deliberately excluded,
// so the sweep can only ever remove a leaked worktree, never a real submodule.
func (r *Repo) OrphanWorktreeGitlinks(ctx context.Context) ([]string, error) {
	out, err := r.Run(ctx, "ls-files", "-s")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	declared, err := r.declaredSubmodulePaths(ctx)
	if err != nil {
		return nil, err
	}
	declaredSet := make(map[string]bool, len(declared))
	for _, d := range declared {
		declaredSet[d] = true
	}
	var orphans []string
	for _, line := range strings.Split(out, "\n") {
		// "<mode> <sha> <stage>\t<path>"; gitlinks are mode 160000.
		if !strings.HasPrefix(line, "160000 ") {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		path := line[tab+1:]
		if declaredSet[path] || !isWorktreeGitlinkPath(path) {
			continue
		}
		orphans = append(orphans, path)
	}
	return orphans, nil
}

// isWorktreeGitlinkPath reports whether p is a per-task code-worktree path of the
// form submodules/<sm>/worktrees/<...> — the only shape the orphan-gitlink sweep
// will remove. It intentionally does NOT match submodules/<sm>/repo (a real
// submodule checkout) or any other layout.
func isWorktreeGitlinkPath(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) >= 4 && parts[0] == "submodules" && parts[2] == "worktrees"
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

// CommitReachable reports whether sha — the SPECIFIC commit a task's
// implementer work is recorded at (e.g. the submodule pointer/gitlink a Work
// pass bumped) — is present SOMEWHERE this repo can see: already in this
// repo's own object database (CommitExists), or — when remote is non-empty —
// after fetching branch (the ref that should carry it) from remote. false
// means sha is reachable NOWHERE this host can look: the "reviewable commit
// exists nowhere" defect a review pass cannot recover from (session-audit-003
// F-LIVE).
//
// Checking the specific SHA (not just whether branch resolves to SOMETHING)
// matters: the F-LIVE failure mode leaves branch resolving to the WRONG commit
// — a dead orphan from a prior attempt — while the actually-recorded
// implementer commit is what is missing, so a plain "does the branch exist"
// check would miss it entirely.
//
// remote == "" (local-sharing / a shared checkout with no configured origin)
// skips the fetch entirely instead of running a doomed `git fetch origin`
// against a nonexistent remote (the exact "fatal: 'origin' does not appear to
// be a git repository" fallback failure the F-LIVE finding names) — in that
// mode every honeybee on the host shares the same object database, so a
// reachable commit is already present locally, no fetch required.
//
// A remote fetch failure (network/auth/misconfigured origin, or the remote
// simply lacking branch) is returned as a real error, never silently folded
// into "unreachable": a transient blip must not falsely bounce a good task to
// arbitration. Only "fetched fine, sha still absent" reports (false, nil).
func (r *Repo) CommitReachable(ctx context.Context, remote, branch, sha string) (bool, error) {
	if r.CommitExists(ctx, sha) {
		return true, nil
	}
	if remote == "" {
		return false, nil
	}
	if err := r.Fetch(ctx, remote, branch); err != nil {
		if isRemoteRefMissing(err) || isCouldNotFindRemoteRef(err) {
			return false, nil
		}
		return false, err
	}
	return r.CommitExists(ctx, sha), nil
}

// isCouldNotFindRemoteRef reports whether a fetch failed because the remote
// simply has no such ref — git's "couldn't find remote ref <name>" — a
// definitive, confirmed absence distinct from a network/auth failure talking
// to the remote at all.
func isCouldNotFindRemoteRef(err error) bool {
	return strings.Contains(err.Error(), "couldn't find remote ref")
}

// reconcileOrphan supersedes a divergent "dead orphan" remote ref for branch
// WITHOUT discarding it: it folds remote/branch's current tip in with git's
// "ours" merge strategy — applied via plumbing (commit-tree + update-ref) so it
// works from ANY worktree of this repo without ever checking branch out or
// touching the working tree/index — producing a new commit whose TREE is
// identical to branch's current tree but whose parents are [branch, the fetched
// orphan tip]. The orphan therefore stays reachable as an ancestor instead of
// being discarded by a force-push/delete, and the retried push that follows is
// a genuine fast-forward (never a force-push). Assumes remote/branch has
// already been fetched into the local object database.
func (r *Repo) reconcileOrphan(ctx context.Context, remote, branch string) error {
	ours, err := r.RevParse(ctx, branch)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", branch, err)
	}
	theirs, err := r.RevParse(ctx, remote+"/"+branch)
	if err != nil {
		return fmt.Errorf("resolve %s/%s: %w", remote, branch, err)
	}
	tree, err := r.Run(ctx, "rev-parse", ours+"^{tree}")
	if err != nil {
		return fmt.Errorf("resolve tree of %s: %w", ours, err)
	}
	msg := fmt.Sprintf("beehive: reconcile dead orphan %s/%s (%s) into %s (ours; supersedes, never discards)",
		remote, branch, theirs, branch)
	merged, err := r.Run(ctx, "commit-tree", tree, "-p", ours, "-p", theirs, "-m", msg)
	if err != nil {
		return fmt.Errorf("commit-tree merge of %s and %s: %w", ours, theirs, err)
	}
	if _, err := r.Run(ctx, "update-ref", "refs/heads/"+branch, merged, ours); err != nil {
		return fmt.Errorf("update-ref %s to reconciled merge: %w", branch, err)
	}
	return nil
}

// PushBranchReconciled pushes the local branch to remote, non-destructively
// reconciling a divergent "dead orphan" origin ref left by a prior GC'd attempt
// of the SAME task (session-audit-003 F-LIVE): the unified claim protocol
// guarantees only one live session works a task at a time, so a divergent
// remote ref encountered here can only be a dead prior attempt, never a live
// concurrent peer. On a plain push's non-fast-forward rejection it fetches the
// remote branch and folds it in via reconcileOrphan (fetch + merge -s ours —
// keeps our tree, keeps the orphan reachable as an ancestor) so the retried
// push is a genuine fast-forward. NEVER force-pushes and never deletes the
// remote ref. A non-nil return means the branch could not be made to land even
// after reconciling; callers must not treat the commit as durably shared.
func (r *Repo) PushBranchReconciled(ctx context.Context, remote, branch string) error {
	err := r.Push(ctx, remote, branch)
	if err == nil {
		return nil
	}
	if !isNonFastForward(err) {
		return err
	}
	if ferr := r.Fetch(ctx, remote, branch); ferr != nil {
		return fmt.Errorf("fetch %s/%s to reconcile a dead orphan (push rejected: %v): %w", remote, branch, err, ferr)
	}
	if rerr := r.reconcileOrphan(ctx, remote, branch); rerr != nil {
		return fmt.Errorf("reconcile dead orphan %s/%s (push rejected: %v): %w", remote, branch, err, rerr)
	}
	if perr := r.Push(ctx, remote, branch); perr != nil {
		return fmt.Errorf("push %s after reconciling a dead orphan on %s (original rejection: %v): %w", branch, remote, err, perr)
	}
	return nil
}

// LsRemoteBranch returns the commit SHA the remote currently advertises for
// branch, or "" when the remote has no such branch. It reads the remote's live
// ref advertisement (git ls-remote --heads) without fetching, so a caller can
// decide whether a pushed source branch still exists before trying to delete it
// — distinguishing "already reclaimed by a peer" from a branch still present.
func (r *Repo) LsRemoteBranch(ctx context.Context, remote, branch string) (string, error) {
	out, err := r.Run(ctx, "ls-remote", "--heads", remote, branch)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", nil
	}
	// "<sha>\trefs/heads/<branch>" (one line per match); take the first SHA.
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// ListRemoteBranches returns the branch names on remote whose ref matches
// pattern (a `git ls-remote --heads` glob, e.g. "edit-*"), read from the
// remote's live ref advertisement — no fetch, no local object needed. It is the
// plural counterpart of LsRemoteBranch: discover WHICH of an unknown set of
// branches (e.g. every editor session ever pushed for durability) a remote
// currently carries, rather than checking one named branch. Empty (nil, nil)
// when nothing matches.
func (r *Repo) ListRemoteBranches(ctx context.Context, remote, pattern string) ([]string, error) {
	out, err := r.Run(ctx, "ls-remote", "--heads", remote, pattern)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		// "<sha>\trefs/heads/<name>" per line.
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		names = append(names, strings.TrimPrefix(fields[1], "refs/heads/"))
	}
	return names, nil
}

// IsAncestor reports whether commit maybe is contained in ref's history (an
// ancestor of, or equal to, ref). It wraps `git merge-base --is-ancestor`, which
// exits 0 for true and 1 for false; any other exit (e.g. a bad object) is a real
// error, never silently folded into false. Used to gate destructive source-branch
// reclamation on "the branch is already merged into the tracked main", and by the
// review-already-merged-guard dispatch check (the symmetric counterpart to
// CommitReachable: not just "does this commit exist somewhere" but "is it already
// folded into the tracked branch").
func (r *Repo) IsAncestor(ctx context.Context, maybe, ref string) (bool, error) {
	if _, err := r.Run(ctx, "merge-base", "--is-ancestor", maybe, ref); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GitlinkAt returns the commit SHA recorded for the submodule gitlink at path in
// ref's tree (the "tracked pointer" a superproject commit pins the submodule to),
// or "" when path is not a gitlink there. It reads `git ls-tree <ref> -- <path>`
// and returns the object id only for a mode-160000 (gitlink/commit) entry, so a
// regular file, a missing path, or an empty tree yields "" (not an error) — the
// caller reads "no tracked pointer" as "cannot compare" rather than a failure.
// Used by the prompt-embed drift guard to resolve the beehive submodule's
// tracked-main tip at dispatch time.
func (r *Repo) GitlinkAt(ctx context.Context, ref, path string) (string, error) {
	out, err := r.Run(ctx, "ls-tree", ref, "--", path)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", nil
	}
	// Entry format: "<mode> <type> <sha>\t<path>"; a gitlink is mode 160000.
	if !strings.HasPrefix(out, "160000 ") {
		return "", nil
	}
	fields := strings.Fields(out)
	if len(fields) < 3 {
		return "", nil
	}
	return fields[2], nil
}

// DeleteRemoteBranch deletes branch on remote (`git push remote --delete
// refs/heads/branch`). It is idempotent against a peer that deleted the branch
// first: git's "remote ref does not exist" rejection is reported as success, so
// concurrent reclamation never errors. Any other failure (no permission,
// protected branch) surfaces unchanged.
func (r *Repo) DeleteRemoteBranch(ctx context.Context, remote, branch string) error {
	if _, err := r.Run(ctx, "push", remote, "--delete", "refs/heads/"+branch); err != nil {
		if isRemoteRefMissing(err) {
			return nil
		}
		return err
	}
	return nil
}

// isRemoteRefMissing reports whether a push --delete failed only because the
// remote branch was already gone (a concurrent peer reclaimed it). git phrases
// this exactly as "remote ref does not exist"; that case is benign for an
// idempotent delete, distinct from a real rejection (permission/protected).
func isRemoteRefMissing(err error) bool {
	return strings.Contains(err.Error(), "remote ref does not exist")
}

// DeleteBranch deletes the local branch (`git branch -D`), discarding it even if
// unmerged. Used to drop a reclaimed source branch's local ref after its worktree
// is removed. git errors when the branch is absent, so callers that treat the
// local-ref cleanup as best-effort should ignore the returned error.
func (r *Repo) DeleteBranch(ctx context.Context, branch string) error {
	_, err := r.Run(ctx, "branch", "-D", branch)
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

// CommitExists reports whether sha is present in this repo's OWN object
// database as a commit — a pure local check (`git cat-file -e sha^{commit}`;
// no fetch, no network) that succeeds for a commit reachable from ANY ref, or
// even a dangling one not on any ref at all (not yet garbage-collected). Used
// to verify a task's recorded implementer commit is reachable in the shared
// module store before trusting it (session-audit-003 F-LIVE: a reviewer must
// never be dispatched against a commit that exists nowhere).
func (r *Repo) CommitExists(ctx context.Context, sha string) bool {
	if strings.TrimSpace(sha) == "" {
		return false
	}
	_, err := r.Run(ctx, "cat-file", "-e", sha+"^{commit}")
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

// DiffPaths reports whether path changed between commits a and b.
func (r *Repo) DiffPaths(ctx context.Context, a, b, path string) (bool, error) {
	out, err := r.Run(ctx, "diff", "--name-only", a, b, "--", path)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// conflictErr wraps ErrConflict with the currently-conflicted (unmerged) paths, so
// a publish failure names WHAT clashed (the submodule gitlink, PLAN.md, a session
// transcript, ...) in the log instead of a bare "merge conflict". Capture it BEFORE
// `git merge --abort`, which clears the conflict state. Kept
// errors.Is(err, ErrConflict)-compatible via %w.
func (r *Repo) conflictErr(ctx context.Context) error {
	paths := "unknown"
	if out, err := r.Run(ctx, "diff", "--name-only", "--diff-filter=U"); err == nil {
		if f := strings.Fields(out); len(f) > 0 {
			paths = strings.Join(f, ",")
		} else {
			paths = "none"
		}
	}
	return fmt.Errorf("%w (conflicted: %s)", ErrConflict, paths)
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
				cerr := r.conflictErr(ctx)
				_, _ = r.Run(ctx, "merge", "--abort")
				return cerr
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
				cerr := r.conflictErr(ctx)
				_, _ = r.Run(ctx, "merge", "--abort")
				return cerr
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
// a committed honeybee worktree with no .gitmodules URL — cannot fatal the sync),
// then git-cleans UNTRACKED cruft out of each declared submodule checkout —
// `submodule update --force` only restores TRACKED content, so a stray untracked
// file inside submodules/<name>/repo (an operator's Emacs #autosave#/.#lock turds,
// a leftover build dir) would otherwise keep the gitlink "modified (untracked
// content)" and wedge every pass. Returns an error (never swallows) if the tree is
// still dirty afterward.
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
			continue
		}
		// submodule update --force restores TRACKED content to the recorded commit
		// but leaves UNTRACKED files in place. Stray untracked content in the
		// checkout (an operator's Emacs #autosave#/.#lock turds from editing the
		// live checkout, a leftover build dir, a stray clone) keeps the superproject
		// gitlink "modified (untracked content)", so status never reaches a clean
		// HEAD projection and every preflight/publish aborts "still dirty after
		// heal" until cleaned by hand. Clean the checkout to a pure projection:
		// -d recurse untracked dirs, -ff also drop an untracked nested git dir (a
		// tracked/registered nested submodule is index content and is never
		// touched). Scoped to the declared submodules/<name>/repo checkout only,
		// never the operator worktrees under submodules/<name>/worktrees. Ignored
		// files (-x) are intentionally left: they do not dirty the gitlink (so they
		// never wedge a pass), and leaving them keeps this consistent with the
		// tracked reset above, which likewise never removes ignored files. Never
		// silent: a path clean cannot fix is recorded in failed and surfaced by the
		// dirty-tree check below (and the leftover keeps status non-empty regardless
		// of git-clean's own exit code).
		sub := &Repo{Dir: filepath.Join(dir, p)}
		if _, err := sub.Run(ctx, "clean", "-ffd"); err != nil {
			failed = append(failed, p)
		}
	}
	out, serr := m.Run(ctx, "status", "--porcelain")
	if serr != nil {
		return serr
	}
	if strings.TrimSpace(out) != "" {
		if len(failed) > 0 {
			return fmt.Errorf("git: main worktree still dirty after heal (submodule resync/clean failed for %s)", strings.Join(failed, ", "))
		}
		return fmt.Errorf("git: main worktree still dirty after heal: %s", strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	}
	return nil
}

// Status returns `git status --porcelain` (trimmed; empty string == clean tree).
func (r *Repo) Status(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// EnsureCleanCheckout makes this checkout a clean projection of committed HEAD so
// a honeybee never starts a token-costly LLM turn on top of drift that can only
// fail to publish at the very end (the failure mode that let a wedged checkout
// spin the swarm for two days with no progress and no cheap signal).
//
// It returns healed=true when the tree WAS dirty and had to be reset. The caller
// MUST surface that as a WARNING: a dirty live checkout is not normal operation —
// the tree is a pure projection of committed history, so any drift signals a bug
// in the honeybee protocol (agent instructions), the honeybee or beehived
// process, or a rogue model writing outside its worktree. Reset is always safe,
// but it always means something upstream misbehaved.
//
// When the tree cannot be made clean — a filesystem error, or drift that survives
// a reset (e.g. an orphan gitlink that wedges `git submodule update`, or stray
// untracked files) — it returns a non-nil error so the caller aborts BEFORE
// spending any tokens. A clean tree is the cheap path: one `git status`.
func (r *Repo) EnsureCleanCheckout(ctx context.Context) (healed bool, err error) {
	st, err := r.Status(ctx)
	if err != nil {
		return false, err
	}
	if st == "" {
		return false, nil
	}
	if herr := r.healLocalMain(ctx); herr != nil {
		return true, herr
	}
	return true, nil
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
				cerr := r.conflictErr(ctx)
				_, _ = r.Run(ctx, "merge", "--abort")
				return cerr
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

// UnmergedPaths lists the paths currently in a conflicted/unmerged index state
// (`git diff --name-only --diff-filter=U`) — empty once every conflict, including
// a submodule gitlink, has been resolved and staged.
func (r *Repo) UnmergedPaths(ctx context.Context) ([]string, error) {
	out, err := r.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// AbortMerge unwinds an in-progress merge, restoring the pre-merge branch state.
func (r *Repo) AbortMerge(ctx context.Context) error {
	_, err := r.Run(ctx, "merge", "--abort")
	return err
}

// HasConflictMarkers reports whether any of paths still contains a git conflict
// marker (`<<<<<<< `) — the safety net that stops a half-resolved file from being
// committed. Non-regular paths (a submodule gitlink has no file) and unreadable
// paths are skipped; the caller's UnmergedPaths check covers those.
func (r *Repo) HasConflictMarkers(ctx context.Context, paths []string) (bool, error) {
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(r.Dir, p))
		if err != nil {
			continue
		}
		if bytes.HasPrefix(b, []byte("<<<<<<< ")) || bytes.Contains(b, []byte("\n<<<<<<< ")) {
			return true, nil
		}
	}
	return false, nil
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
