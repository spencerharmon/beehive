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

// bareOrigin inits an empty bare repo (default branch main) to act as a shared
// origin two clones push to and fetch from.
func bareOrigin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := New(dir).Run(context.Background(), "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatalf("bare init: %v", err)
	}
	return dir
}

// cloneOf clones origin into a fresh temp dir named name, with a committer
// identity configured so commits succeed.
func cloneOf(t *testing.T, origin, name string) *Repo {
	t.Helper()
	ctx := context.Background()
	parent := t.TempDir()
	dir := filepath.Join(parent, name)
	if _, err := New(parent).Run(ctx, "clone", "-q", origin, dir); err != nil {
		t.Fatalf("clone %s: %v", name, err)
	}
	r := New(dir)
	for _, a := range [][]string{
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if _, err := r.Run(ctx, a...); err != nil {
			t.Fatalf("config %v: %v", a, err)
		}
	}
	return r
}

func writeFile(t *testing.T, r *Repo, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readFile(t *testing.T, r *Repo, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.Dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func commitFile(t *testing.T, r *Repo, name, body, msg string) string {
	t.Helper()
	writeFile(t, r, name, body)
	if err := r.Commit(context.Background(), msg); err != nil {
		t.Fatalf("commit %s: %v", msg, err)
	}
	sha, err := r.RevParse(context.Background(), "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return sha
}

// TestRemoteRoundTrip exercises Push -> Fetch -> Pull through a bare origin:
// clone a seeds origin, clone b advances it via Push, a's Fetch advances the
// remote-tracking ref (without moving HEAD), and a's Pull fast-forwards HEAD to
// b's tip with the new content on disk.
func TestRemoteRoundTrip(t *testing.T) {
	ctx := context.Background()
	origin := bareOrigin(t)
	a := cloneOf(t, origin, "a")

	// a seeds origin's main.
	v1 := commitFile(t, a, "f", "v1\n", "v1")
	if err := a.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("a push v1: %v", err)
	}
	if got, _ := New(origin).RevParse(ctx, "main"); got != v1 {
		t.Fatalf("Push did not update origin: origin main %s != %s", got, v1)
	}

	// b clones the seeded origin and advances main.
	b := cloneOf(t, origin, "b")
	v2 := commitFile(t, b, "f", "v2\n", "v2")
	if err := b.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("b push v2: %v", err)
	}
	if got, _ := New(origin).RevParse(ctx, "main"); got != v2 {
		t.Fatalf("Push did not advance origin to v2: %s != %s", got, v2)
	}

	// a fetches: origin/main advances to v2 but HEAD stays at v1.
	if err := a.Fetch(ctx, "origin", "main"); err != nil {
		t.Fatalf("a fetch: %v", err)
	}
	if got, _ := a.RevParse(ctx, "origin/main"); got != v2 {
		t.Fatalf("Fetch did not advance origin/main: %s != %s", got, v2)
	}
	if got, _ := a.RevParse(ctx, "HEAD"); got != v1 {
		t.Fatalf("Fetch moved HEAD: %s != %s", got, v1)
	}

	// a pulls --ff-only: HEAD fast-forwards to v2 with v2 content on disk.
	if err := a.Pull(ctx, "origin", "main"); err != nil {
		t.Fatalf("a pull: %v", err)
	}
	if got, _ := a.RevParse(ctx, "HEAD"); got != v2 {
		t.Fatalf("Pull did not fast-forward HEAD: %s != %s", got, v2)
	}
	if got := readFile(t, a, "f"); got != "v2\n" {
		t.Fatalf("Pull content not updated: %q", got)
	}
}

// TestHardResetDiscards proves HardReset moves HEAD to ref and discards both
// committed and uncommitted local changes.
func TestHardResetDiscards(t *testing.T) {
	ctx := context.Background()
	r := initRepo(t)
	base := commitFile(t, r, "f", "base\n", "base")
	commitFile(t, r, "f", "second\n", "second") // HEAD now ahead of base
	writeFile(t, r, "f", "dirty\n")             // plus an uncommitted edit

	if err := r.HardReset(ctx, base); err != nil {
		t.Fatalf("HardReset: %v", err)
	}
	if got, _ := r.RevParse(ctx, "HEAD"); got != base {
		t.Fatalf("HardReset did not move HEAD to base: %s != %s", got, base)
	}
	if got := readFile(t, r, "f"); got != "base\n" {
		t.Fatalf("HardReset did not discard edits: %q", got)
	}
	if c, _ := r.Clean(ctx); !c {
		t.Fatal("tree dirty after HardReset")
	}
}

// TestPullFFOnlyDivergence is the caveat: on divergent histories Pull must error
// (no merge commit) and leave the current branch untouched, so callers can treat
// it as a lost race.
func TestPullFFOnlyDivergence(t *testing.T) {
	ctx := context.Background()
	origin := bareOrigin(t)
	a := cloneOf(t, origin, "a")
	commitFile(t, a, "f", "v1\n", "v1")
	if err := a.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("a push v1: %v", err)
	}

	b := cloneOf(t, origin, "b")

	// a advances origin down one line of history...
	commitFile(t, a, "g", "a-side\n", "a-side")
	if err := a.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("a push a-side: %v", err)
	}
	// ...while b commits a divergent line locally.
	bTip := commitFile(t, b, "h", "b-side\n", "b-side")

	if err := b.Pull(ctx, "origin", "main"); err == nil {
		t.Fatal("Pull --ff-only must error on divergence, got nil")
	}
	if got, _ := b.RevParse(ctx, "HEAD"); got != bTip {
		t.Fatalf("Pull moved HEAD on a divergent ff-only pull: %s != %s", got, bTip)
	}
	if c, _ := b.HasConflict(ctx); c {
		t.Fatal("Pull left the tree in a conflicted/merging state")
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

func TestRemoteConfigSnapshotRestore(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()

	// Baseline: a local-only repo with no remotes.
	snap, err := r.RemoteConfig(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap != "" {
		t.Fatalf("fresh repo has remotes: %q", snap)
	}

	// Simulate an agent leaking a remote into the shared config.
	if _, err := r.Run(ctx, "remote", "add", "origin", "git@example.com:x/y.git"); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if got, _ := r.RemoteConfig(ctx); got == "" {
		t.Fatal("remote not recorded after add")
	}

	// Restore reverts to the empty baseline.
	if err := r.RestoreRemotes(ctx, snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, _ := r.RemoteConfig(ctx); got != "" {
		t.Fatalf("remote not reverted: %q", got)
	}

	// Now a baseline that HAS a remote: restore must re-add a removed one and
	// drop an extra one, landing exactly on the snapshot.
	if _, err := r.Run(ctx, "remote", "add", "origin", "git@example.com:keep.git"); err != nil {
		t.Fatalf("remote add origin: %v", err)
	}
	base, err := r.RemoteConfig(ctx)
	if err != nil {
		t.Fatalf("snapshot2: %v", err)
	}
	if _, err := r.Run(ctx, "remote", "remove", "origin"); err != nil {
		t.Fatalf("remote remove: %v", err)
	}
	if _, err := r.Run(ctx, "remote", "add", "bogus", "git@example.com:bogus.git"); err != nil {
		t.Fatalf("remote add bogus: %v", err)
	}
	if err := r.RestoreRemotes(ctx, base); err != nil {
		t.Fatalf("restore2: %v", err)
	}
	got, _ := r.RemoteConfig(ctx)
	if got != base {
		t.Fatalf("restore mismatch:\n got=%q\nwant=%q", got, base)
	}
	if u, _ := r.Run(ctx, "remote", "get-url", "origin"); u != "git@example.com:keep.git" {
		t.Fatalf("origin url not restored: %q", u)
	}
}

// TestPublishToMainHealsDirtyLocalTree proves the reset fallback: when the local
// checked-out main worktree is dirty (a tracked file modified out-of-band, the
// same way a lagging submodule checkout dirties it), a publish from a linked
// worktree branch is not lost — PublishToMain resets the projection tree to HEAD
// and lands the commit. Mirrors the production single-host (no-remote) path.
func TestPublishToMainHealsDirtyLocalTree(t *testing.T) {
	ctx := context.Background()
	r := initRepo(t)
	// Seed main with a tracked file and a committed baseline.
	if err := os.WriteFile(filepath.Join(r.Dir, "f"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(ctx, "base"); err != nil {
		t.Fatalf("base commit: %v", err)
	}
	base, _ := r.Head(ctx)

	// A honeybee worktree on its own branch with a new commit to publish.
	wt := filepath.Join(t.TempDir(), "wt")
	if err := r.WorktreeAdd(ctx, wt, "bee-x", "main"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	w := New(wt)
	if err := os.WriteFile(filepath.Join(wt, "g"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(ctx, "work"); err != nil {
		t.Fatalf("work commit: %v", err)
	}
	want, _ := w.Head(ctx)

	// Dirty the primary main worktree out-of-band: updateInstead will refuse the
	// push until the tree is reset. (A drifted submodule checkout looks identical.)
	if err := os.WriteFile(filepath.Join(r.Dir, "f"), []byte("DIRTY-DRIFT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if clean, _ := r.Clean(ctx); clean {
		t.Fatal("precondition: main tree should be dirty")
	}

	// Publish must NOT be lost: the heal resets the dirty tree and lands the commit.
	if err := w.PublishToMain(ctx, ""); err != nil {
		t.Fatalf("publish (heal expected): %v", err)
	}
	head, _ := r.Head(ctx)
	if head != want {
		t.Fatalf("main did not advance to the published commit: head=%s want=%s base=%s", head, want, base)
	}
	if clean, _ := r.Clean(ctx); !clean {
		t.Fatal("main tree still dirty after heal+publish")
	}
	// The out-of-band drift was reset (file reflects committed history, advanced).
	if got, _ := os.ReadFile(filepath.Join(r.Dir, "g")); string(got) != "work\n" {
		t.Fatalf("published content missing in main worktree: %q", got)
	}
}

// TestSourceBranchReclaimVerbs exercises the remote-branch reclamation primitives
// end to end through a bare origin: LsRemoteBranch sees a pushed branch (and the
// absence of one), IsAncestor distinguishes a merged branch from a divergent one,
// DeleteRemoteBranch removes a present branch and is a no-op (no error) on an
// already-absent one, and DeleteBranch drops a local ref.
func TestSourceBranchReclaimVerbs(t *testing.T) {
	ctx := context.Background()
	origin := bareOrigin(t)
	a := cloneOf(t, origin, "a")

	// Seed origin/main, then push a "merged" bee branch (== main) and a divergent
	// "unmerged" bee branch (main + 1 commit not on main).
	main := commitFile(t, a, "f", "v1\n", "v1")
	if err := a.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("push main: %v", err)
	}
	if _, err := a.Run(ctx, "push", "origin", "main:refs/heads/bee-merged"); err != nil {
		t.Fatalf("push bee-merged: %v", err)
	}
	unmerged := commitFile(t, a, "g", "side\n", "side") // advances a's HEAD past origin/main
	if _, err := a.Run(ctx, "push", "origin", "HEAD:refs/heads/bee-unmerged"); err != nil {
		t.Fatalf("push bee-unmerged: %v", err)
	}

	// A second clone is the "reclaimer" that inspects/deletes via the verbs.
	b := cloneOf(t, origin, "b")

	// LsRemoteBranch: present branches resolve to their tips; an absent one is "".
	if got, err := b.LsRemoteBranch(ctx, "origin", "bee-merged"); err != nil || got != main {
		t.Fatalf("LsRemoteBranch bee-merged = %q,%v want %s", got, err, main)
	}
	if got, err := b.LsRemoteBranch(ctx, "origin", "bee-unmerged"); err != nil || got != unmerged {
		t.Fatalf("LsRemoteBranch bee-unmerged = %q,%v want %s", got, err, unmerged)
	}
	if got, err := b.LsRemoteBranch(ctx, "origin", "bee-absent"); err != nil || got != "" {
		t.Fatalf("LsRemoteBranch bee-absent = %q,%v want empty", got, err)
	}

	// Bring the branch tips local so IsAncestor can read their objects.
	if err := b.Fetch(ctx, "origin", "main"); err != nil {
		t.Fatalf("fetch main: %v", err)
	}
	if err := b.Fetch(ctx, "origin", "bee-unmerged"); err != nil {
		t.Fatalf("fetch bee-unmerged: %v", err)
	}
	// IsAncestor: the merged tip is contained in origin/main; the divergent one is not.
	if ok, err := b.IsAncestor(ctx, main, "origin/main"); err != nil || !ok {
		t.Fatalf("IsAncestor(merged, origin/main) = %v,%v want true", ok, err)
	}
	if ok, err := b.IsAncestor(ctx, unmerged, "origin/main"); err != nil || ok {
		t.Fatalf("IsAncestor(unmerged, origin/main) = %v,%v want false", ok, err)
	}

	// DeleteRemoteBranch removes a present branch...
	if err := b.DeleteRemoteBranch(ctx, "origin", "bee-merged"); err != nil {
		t.Fatalf("DeleteRemoteBranch bee-merged: %v", err)
	}
	if got, _ := b.LsRemoteBranch(ctx, "origin", "bee-merged"); got != "" {
		t.Fatalf("bee-merged still on origin after delete: %s", got)
	}
	// ...and is an idempotent no-op (no error) on an already-absent branch.
	if err := b.DeleteRemoteBranch(ctx, "origin", "bee-merged"); err != nil {
		t.Fatalf("DeleteRemoteBranch of an absent branch must be a no-op, got %v", err)
	}
	if err := b.DeleteRemoteBranch(ctx, "origin", "bee-never-existed"); err != nil {
		t.Fatalf("DeleteRemoteBranch of a never-existing branch must be a no-op, got %v", err)
	}

	// DeleteBranch drops a local ref (create one, then delete it).
	if _, err := b.Run(ctx, "branch", "bee-local", "main"); err != nil {
		t.Fatalf("create local branch: %v", err)
	}
	if err := b.DeleteBranch(ctx, "bee-local"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if _, err := b.Run(ctx, "rev-parse", "--verify", "refs/heads/bee-local"); err == nil {
		t.Fatal("local branch bee-local still present after DeleteBranch")
	}
}
