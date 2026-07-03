package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// TestOrphanWorktreeGitlinks locks the classifier that decides what the GC sweep
// is allowed to remove. It seeds the four index shapes side by side and asserts
// the method returns EXACTLY the leaked code-worktree gitlink. The undeclared
// submodules/<sm>/repo case is the load-bearing safety check: a real submodule
// checkout whose .gitmodules entry has not landed yet (e.g. mid-bootstrap) is a
// 160000 entry with no declaration, and it must NEVER be classified as an orphan
// worktree — only the submodules/<sm>/worktrees/<...> shape may be.
func TestOrphanWorktreeGitlinks(t *testing.T) {
	ctx := context.Background()
	r := initRepo(t)

	// A DECLARED submodule: a .gitmodules entry plus a gitlink at its path.
	gm := "[submodule \"dep\"]\n\tpath = submodules/dep/repo\n\turl = ../dep.git\n"
	if err := os.WriteFile(filepath.Join(r.Dir, ".gitmodules"), []byte(gm), 0o644); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	for _, p := range []string{
		"submodules/dep/repo",           // declared submodule    -> excluded (declaredSet)
		"submodules/sm/repo",            // UNDECLARED repo checkout -> excluded (path guard)
		"submodules/sm/worktrees/bee-x", // leaked code worktree  -> the ONLY orphan
	} {
		if _, err := r.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+sha+","+p); err != nil {
			t.Fatalf("seed gitlink %s: %v", p, err)
		}
	}
	// A plain blob at a submodules/ path must never be mistaken for a gitlink.
	if err := os.MkdirAll(filepath.Join(r.Dir, "submodules", "sm"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r, "submodules/sm/PLAN.md", "plan\n")
	if _, err := r.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add blob: %v", err)
	}

	got, err := r.OrphanWorktreeGitlinks(ctx)
	if err != nil {
		t.Fatalf("OrphanWorktreeGitlinks: %v", err)
	}
	if len(got) != 1 || got[0] != "submodules/sm/worktrees/bee-x" {
		t.Fatalf("orphans = %v, want exactly [submodules/sm/worktrees/bee-x]", got)
	}
}

// TestRemoveCachedAndCommitStaged locks the two primitives the orphan sweep is
// built from. RemoveCached must drop a path from the index WITHOUT touching the
// working tree (so a live nested checkout survives), and CommitStaged must record
// exactly the staged index — no add, no pathspec — returning ErrNothing when the
// index matches HEAD so an empty sweep never manufactures a commit.
func TestRemoveCachedAndCommitStaged(t *testing.T) {
	ctx := context.Background()
	r := initRepo(t)

	// A clean index is a no-op sentinel, never an empty commit.
	if err := r.CommitStaged(ctx, "nothing"); err != ErrNothing {
		t.Fatalf("CommitStaged on clean index: want ErrNothing, got %v", err)
	}
	// Empty path list is a no-op (and must not error).
	if err := r.RemoveCached(ctx); err != nil {
		t.Fatalf("RemoveCached(no paths): %v", err)
	}

	commitFile(t, r, "keep", "v\n", "base")
	writeFile(t, r, "gone", "body\n")
	if _, err := r.Run(ctx, "add", "gone"); err != nil {
		t.Fatalf("add gone: %v", err)
	}
	if err := r.CommitStaged(ctx, "track gone"); err != nil {
		t.Fatalf("CommitStaged(track): %v", err)
	}

	// RemoveCached un-tracks it but must leave the file on disk.
	if err := r.RemoveCached(ctx, "gone"); err != nil {
		t.Fatalf("RemoveCached: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "gone")); err != nil {
		t.Fatalf("RemoveCached deleted the working-tree file: %v", err)
	}
	if staged, _ := r.Run(ctx, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "gone" {
		t.Fatalf("expected only 'gone' staged for removal, got %q", staged)
	}
	// CommitStaged records only the removal; it must not re-add the on-disk file.
	if err := r.CommitStaged(ctx, "drop gone"); err != nil {
		t.Fatalf("CommitStaged(removal): %v", err)
	}
	if r.Exists(ctx, "HEAD", "gone") {
		t.Fatal("CommitStaged did not record the removal (gone still tracked at HEAD)")
	}
	if !r.Exists(ctx, "HEAD", "keep") {
		t.Fatal("CommitStaged disturbed an unrelated tracked file")
	}
}

// TestEnsureCleanCheckout covers the honeybee startup preflight guard: a clean
// tree is a cheap no-op (healed=false); tracked drift is reset to HEAD and
// reported as healed=true so the caller can WARN; drift that survives a reset
// (here an untracked file, standing in for any un-resettable state such as an
// orphan gitlink) returns healed=true AND a non-nil error so the caller aborts
// before starting the agent.
func TestEnsureCleanCheckout(t *testing.T) {
	ctx := context.Background()

	// (1) clean tree -> no-op
	r := initRepo(t)
	os.WriteFile(filepath.Join(r.Dir, "f"), []byte("base\n"), 0o644)
	if err := r.Commit(ctx, "base"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if healed, err := r.EnsureCleanCheckout(ctx); healed || err != nil {
		t.Fatalf("clean tree: want (false,nil), got (%v,%v)", healed, err)
	}

	// (2) tracked-file drift -> reset, healed=true, clean afterward
	os.WriteFile(filepath.Join(r.Dir, "f"), []byte("DRIFT\n"), 0o644)
	healed, err := r.EnsureCleanCheckout(ctx)
	if !healed || err != nil {
		t.Fatalf("tracked drift: want (true,nil), got (%v,%v)", healed, err)
	}
	if st, _ := r.Status(ctx); st != "" {
		t.Fatalf("tree not clean after heal: %q", st)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "f")); string(b) != "base\n" {
		t.Fatalf("reset did not restore committed content: %q", b)
	}

	// (3) un-resettable drift (untracked file survives reset) -> healed=true, error
	r2 := initRepo(t)
	os.WriteFile(filepath.Join(r2.Dir, "f"), []byte("x\n"), 0o644)
	if err := r2.Commit(ctx, "base"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	os.WriteFile(filepath.Join(r2.Dir, "stray"), []byte("untracked\n"), 0o644)
	healed, err = r2.EnsureCleanCheckout(ctx)
	if !healed || err == nil {
		t.Fatalf("un-resettable drift: want (true,err), got (%v,%v)", healed, err)
	}
}

// TestConflictErrNamesPaths verifies the publish-conflict instrumentation: the
// error returned when a merge conflicts still satisfies errors.Is(ErrConflict)
// (so every existing consumer keeps working) AND names the conflicted path so the
// log says WHAT clashed instead of a bare "merge conflict".
func TestConflictErrNamesPaths(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(r.Dir, "f"), []byte("base\n"), 0o644)
	r.Commit(ctx, "base")
	r.Run(ctx, "checkout", "-q", "-b", "x")
	os.WriteFile(filepath.Join(r.Dir, "f"), []byte("x\n"), 0o644)
	r.Commit(ctx, "x")
	r.Run(ctx, "checkout", "-q", "main")
	os.WriteFile(filepath.Join(r.Dir, "f"), []byte("main\n"), 0o644)
	r.Commit(ctx, "main")
	// Merge x into main -> conflicts on f, leaving unmerged state.
	if _, err := r.Run(ctx, "merge", "--no-edit", "x"); err == nil {
		t.Fatal("expected merge to conflict")
	}
	err := r.conflictErr(ctx)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("wrapped error must match ErrConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "conflicted:") || !strings.Contains(err.Error(), "f") {
		t.Fatalf("error must name the conflicted path, got %q", err.Error())
	}
}
