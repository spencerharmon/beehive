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

// TestSharesHistoryRelatedVsForeign locks the repo-own-remote check the editor
// base selection relies on: a remote whose main descends from the SAME history
// shares an ancestor (true), an unrelated/foreign main does not (false), and an
// absent ref is not provably related (false) — all without surfacing an error.
func TestSharesHistoryRelatedVsForeign(t *testing.T) {
	ctx := context.Background()
	r := initRepo(t)
	commitFile(t, r, "f", "base\n", "base")

	// A "remote" tracking ref that descends from local main shares history.
	if _, err := r.Run(ctx, "update-ref", "refs/remotes/origin/main", "main"); err != nil {
		t.Fatalf("update-ref own: %v", err)
	}
	if ok, err := r.SharesHistory(ctx, "main", "origin/main"); err != nil || !ok {
		t.Fatalf("repo-own remote should share history: ok=%v err=%v", ok, err)
	}

	// A foreign history: a second root commit with NO common ancestor, parked on
	// its own ref. merge-base must report unrelated -> false (no error).
	if _, err := r.Run(ctx, "checkout", "--orphan", "foreign"); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if _, err := r.Run(ctx, "rm", "-rf", "."); err != nil {
		t.Fatalf("rm: %v", err)
	}
	commitFile(t, r, "z", "alien\n", "alien root")
	if _, err := r.Run(ctx, "update-ref", "refs/remotes/foreign/main", "foreign"); err != nil {
		t.Fatalf("update-ref foreign: %v", err)
	}
	if _, err := r.Run(ctx, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	if ok, err := r.SharesHistory(ctx, "main", "foreign/main"); err != nil || ok {
		t.Fatalf("foreign remote must NOT share history: ok=%v err=%v", ok, err)
	}

	// A non-existent ref is not provably related and never errors.
	if ok, err := r.SharesHistory(ctx, "main", "origin/does-not-exist"); err != nil || ok {
		t.Fatalf("absent ref: want (false,nil), got ok=%v err=%v", ok, err)
	}
}

// TestExistsAtRef locks the boolean form of Show used to validate an edit base.
func TestExistsAtRef(t *testing.T) {
	ctx := context.Background()
	r := initRepo(t)
	commitFile(t, r, "present.md", "hi\n", "seed")
	if !r.Exists(ctx, "main", "present.md") {
		t.Fatal("present.md should exist at main")
	}
	if r.Exists(ctx, "main", "missing.md") {
		t.Fatal("missing.md must not exist at main")
	}
	// An empty (but tracked) blob still counts as present.
	commitFile(t, r, "empty.md", "", "empty")
	if !r.Exists(ctx, "main", "empty.md") {
		t.Fatal("empty tracked file should count as present")
	}
}

// TestIsAncestor locks the merge-base --is-ancestor wrapper the runner uses to
// verify a publish reached main: an older commit is an ancestor of a newer one
// (and of itself), the reverse is not, and an unresolvable ref surfaces an error
// rather than a silent false.
func TestIsAncestor(t *testing.T) {
	ctx := context.Background()
	r := initRepo(t)
	base := commitFile(t, r, "f", "base\n", "base")
	next := commitFile(t, r, "f", "next\n", "next")

	// base -> next: base is an ancestor of next.
	if ok, err := r.IsAncestor(ctx, base, next); err != nil || !ok {
		t.Fatalf("base ancestor of next: ok=%v err=%v", ok, err)
	}
	// A commit is its own ancestor (reflexive) — an exact match must be true, so a
	// published head equal to origin main reads as advanced.
	if ok, err := r.IsAncestor(ctx, next, next); err != nil || !ok {
		t.Fatalf("commit should be its own ancestor: ok=%v err=%v", ok, err)
	}
	// next is NOT an ancestor of base: a local-only commit ahead of main fails.
	if ok, err := r.IsAncestor(ctx, next, base); err != nil || ok {
		t.Fatalf("next must not be ancestor of base: ok=%v err=%v", ok, err)
	}
	// An unresolvable ref is a real error (exit 128), never a silent (false, nil).
	if _, err := r.IsAncestor(ctx, base, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err == nil {
		t.Fatal("absent descendant ref must surface an error, not a silent false")
	}
}
