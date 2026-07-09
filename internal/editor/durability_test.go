package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
)

// setupRemote creates a bare repo, adds it as "origin" on root, and pushes
// local main to it, returning the bare repo's directory and a *git.Repo
// wrapper over it for direct inspection (ls-remote/rev-parse/show) in tests.
func setupRemote(t *testing.T, root string) (bareDir string, bare *git.Repo) {
	t.Helper()
	ctx := context.Background()
	bareDir = t.TempDir()
	if _, err := git.New(bareDir).Run(ctx, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatalf("bare init: %v", err)
	}
	g := git.New(root)
	if _, err := g.Run(ctx, "remote", "add", "origin", bareDir); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if _, err := g.Run(ctx, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("push main: %v", err)
	}
	return bareDir, git.New(bareDir)
}

// TestPushOnTurnReachableFromSecondClone is the push-on-turn acceptance: with a
// remote configured on the root repo, a committed chat turn pushes the
// session's edit branch there, and both the edited file AND the transcript
// sidecar are resolvable from an INDEPENDENT second clone — a stand-in for a
// different host or peer that never shares this process's local worktrees.
func TestPushOnTurnReachableFromSecondClone(t *testing.T) {
	root, _ := setupRepo(t)
	bareDir, _ := setupRemote(t, root)
	ctx := context.Background()

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("pushed goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.remote != "origin" {
		t.Fatalf("want trusted remote origin, got %q", sess.remote)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if e := sess.Err(); e != "" {
		t.Fatalf("unexpected session error: %s", e)
	}

	// Independent second clone proves the branch (and its transcript sidecar)
	// really landed on the remote, not just in this process's own worktree.
	parent := t.TempDir()
	clone2 := filepath.Join(parent, "clone2")
	if _, err := git.New(parent).Run(ctx, "clone", "-q", bareDir, clone2); err != nil {
		t.Fatalf("second clone: %v", err)
	}
	c2 := git.New(clone2)
	out, err := c2.Run(ctx, "show", "origin/"+sess.Branch+":"+file)
	if err != nil {
		t.Fatalf("second clone cannot read pushed file: %v", err)
	}
	if !strings.Contains(out, "pushed goal") {
		t.Fatalf("second clone missing the change: %q", out)
	}
	logOut, err := c2.Run(ctx, "show", "origin/"+sess.Branch+":"+chatLogSidecar)
	if err != nil {
		t.Fatalf("second clone cannot read transcript sidecar: %v", err)
	}
	if !strings.Contains(logOut, "add a goal") {
		t.Fatalf("second clone transcript missing the user turn: %q", logOut)
	}
}

// TestNoRemotePushIsNoop proves the no-remote path is byte-identical to the
// pre-durability behavior: no sidecar is ever written (there is no remote to
// recover it from), nothing is ever pushed, and the ordinary commit/diff/state
// contract is unaffected.
func TestNoRemotePushIsNoop(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("local goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.remote != "" {
		t.Fatalf("want no remote, got %q", sess.remote)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if e := sess.Err(); e != "" {
		t.Fatalf("unexpected session error: %s", e)
	}
	if _, err := os.Stat(filepath.Join(sess.wtPath, chatLogSidecar)); !os.IsNotExist(err) {
		t.Fatalf("sidecar must not be written with no remote (stat err=%v)", err)
	}
	if st := sess.State(ctx); st != "dirty" {
		t.Fatalf("want dirty, got %s", st)
	}
}

// TestPushFailureSurfacedNeverSwallowed proves a durability push failure (the
// remote vanishes after Open already trusted it) surfaces as a visible session
// error rather than being silently swallowed, while the LOCAL commit still
// succeeds: the session degrades to local-only durability with a visible
// warning, per the design, rather than silently proceeding as if pushed.
func TestPushFailureSurfacedNeverSwallowed(t *testing.T) {
	root, _ := setupRepo(t)
	bareDir, _ := setupRemote(t, root)
	ctx := context.Background()

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("unpushed-marker\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.remote == "" {
		t.Fatalf("want a trusted remote before simulating its failure")
	}
	// Simulate the remote vanishing (outage / deletion) AFTER Open already
	// trusted it, so only the mid-session PUSH fails, not Open itself.
	if err := os.RemoveAll(bareDir); err != nil {
		t.Fatalf("remove bare remote: %v", err)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if e := sess.Err(); !strings.Contains(e, "push") {
		t.Fatalf("want a surfaced push error, got %q", e)
	}
	// The local commit still landed: the diff/state contract is unaffected.
	if st := sess.State(ctx); st != "dirty" {
		t.Fatalf("local commit should still succeed, want dirty got %s", st)
	}
	base, proposed, _ := sess.Diff(ctx)
	if strings.Contains(base, "unpushed-marker") || !strings.Contains(proposed, "unpushed-marker") {
		t.Fatalf("local diff should still reflect the edit: base=%q proposed=%q", base, proposed)
	}
}

// TestReloadRecoversSessionAfterLocalWorktreeLost is the remote-recovery-
// after-local-loss acceptance: a session pushed to a trusted remote, whose
// LOCAL worktree, local branch, AND store record are all then lost (the
// "different host / wiped scratch dir" case), is recovered by Reload straight
// from the fetched remote tip instead of being silently dropped.
func TestReloadRecoversSessionAfterLocalWorktreeLost(t *testing.T) {
	root, _ := setupRepo(t)
	setupRemote(t, root)
	ctx := context.Background()

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("recovered goal\n")...), 0o644)
	}
	mA := newTestManager(t, root, fc)
	sess, err := mA.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	branch := sess.Branch
	id := sess.ID
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if e := sess.Err(); e != "" {
		t.Fatalf("unexpected session error: %s", e)
	}

	// Simulate total local loss: worktree gone, LOCAL branch gone too (a truly
	// different host would never have had either), and the store wiped.
	g := git.New(root)
	if err := g.WorktreeRemove(ctx, sess.wtPath); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := g.Run(ctx, "branch", "-D", branch); err != nil {
		t.Fatalf("delete local branch: %v", err)
	}
	if err := mA.store.save(nil); err != nil {
		t.Fatalf("wipe store: %v", err)
	}

	mB := newTestManager(t, root, fc)
	if err := mB.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}

	got, ok := mB.Get(id)
	if !ok {
		t.Fatalf("session %s not recovered from remote after total local loss", id)
	}
	if got.File != file {
		t.Fatalf("recovered file = %q, want %q", got.File, file)
	}
	if st := got.State(ctx); st != "dirty" {
		t.Fatalf("recovered session want dirty, got %s", st)
	}
	base, proposed, _ := got.Diff(ctx)
	if strings.Contains(base, "recovered goal") || !strings.Contains(proposed, "recovered goal") {
		t.Fatalf("recovered diff wrong: base=%q proposed=%q", base, proposed)
	}
	if len(got.Log()) == 0 {
		t.Fatalf("recovered session lost its transcript (should come from the sidecar)")
	}
	// The recovered worktree really was recreated on disk at the canonical path.
	if _, err := os.Stat(got.wtPath); err != nil {
		t.Fatalf("recovered worktree missing on disk: %v", err)
	}
	// Still fully operable: mergeable from the recovered state.
	if err := got.Merge(ctx); err != nil {
		t.Fatalf("merge recovered session: %v", err)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "recovered goal") {
		t.Fatalf("recovered merge did not reach main: %q", string(onMain))
	}
}

// TestReloadRecoversTranscriptFromSidecarWhenStoreLogCleared is the
// transcript-recovery-from-branch acceptance: even when the local worktree AND
// its store record both still exist, a record whose log field was lost or
// corrupted is repaired from the branch's own tracked transcript sidecar
// instead of resuming with an empty conversation.
func TestReloadRecoversTranscriptFromSidecarWhenStoreLogCleared(t *testing.T) {
	root, _ := setupRepo(t)
	setupRemote(t, root)
	ctx := context.Background()

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("goal\n")...), 0o644)
	}
	mA := newTestManager(t, root, fc)
	sess, err := mA.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := sess.ID
	if _, err := sess.Chat(ctx, "please add a distinctive-marker-goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}

	// Corrupt the persisted record's log field only — everything else (file,
	// branch, activity) stays intact, simulating partial store corruption
	// rather than total loss.
	recs, err := mA.store.load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	found := false
	for i := range recs {
		if recs[i].Branch == sess.Branch {
			recs[i].Log = nil
			found = true
		}
	}
	if !found {
		t.Fatalf("no store record for %s", sess.Branch)
	}
	if err := mA.store.save(recs); err != nil {
		t.Fatalf("save corrupted store: %v", err)
	}

	mB := newTestManager(t, root, fc)
	if err := mB.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := mB.Get(id)
	if !ok {
		t.Fatalf("session %s not recovered", id)
	}
	log := got.Log()
	if len(log) == 0 {
		t.Fatalf("transcript not recovered from the branch sidecar")
	}
	joined := ""
	for _, turn := range log {
		joined += turn.Text
	}
	if !strings.Contains(joined, "distinctive-marker-goal") {
		t.Fatalf("recovered transcript missing expected content: %+v", log)
	}
}

// TestMergeNeverPublishesSidecarToMain is a critical correctness safeguard:
// the transcript sidecar is a durability implementation detail of the
// in-progress edit branch and must NEVER ride onto main when a session merges
// — main must stay exactly the beehive coordination-file tree, never an
// editor-internal artifact.
func TestMergeNeverPublishesSidecarToMain(t *testing.T) {
	root, _ := setupRepo(t)
	setupRemote(t, root)
	ctx := context.Background()

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.remote == "" {
		t.Fatalf("want a trusted remote")
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	// The sidecar DID land on the edit branch (durability working as intended).
	if _, err := os.Stat(filepath.Join(sess.wtPath, chatLogSidecar)); err != nil {
		t.Fatalf("sidecar missing from the edit worktree before merge: %v", err)
	}
	if err := sess.Merge(ctx); err != nil {
		t.Fatalf("merge: %v", err)
	}
	// But it must never reach main: neither the primary checkout's working tree
	// nor main's own committed tree.
	if _, err := os.Stat(filepath.Join(root, chatLogSidecar)); !os.IsNotExist(err) {
		t.Fatalf("sidecar leaked onto the primary checkout's main working tree (stat err=%v)", err)
	}
	g := git.New(root)
	if _, err := g.Run(ctx, "cat-file", "-e", "main:"+chatLogSidecar); err == nil {
		t.Fatalf("sidecar leaked into main's committed tree")
	}
}

// TestReloadReclaimAlsoDeletesRemoteBranch proves reclaim's remote cleanup: a
// stale, clean edit-* worktree that ALSO exists on a trusted remote has its
// remote branch deleted too, so a later Reload's remote-recovery pass can
// never resurrect a worktree this prune already removed.
func TestReloadReclaimAlsoDeletesRemoteBranch(t *testing.T) {
	root, _ := setupRepo(t)
	_, bare := setupRemote(t, root)
	ctx := context.Background()
	g := git.New(root)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	staleBranch := "edit-stale-100"
	staleWt := filepath.Join(root, ".worktrees", staleBranch)
	if _, err := g.Run(ctx, "worktree", "add", "-b", staleBranch, staleWt, "main"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if err := g.Push(ctx, "origin", staleBranch); err != nil {
		t.Fatalf("push stale branch to origin: %v", err)
	}
	if _, err := bare.Run(ctx, "rev-parse", "--verify", "refs/heads/"+staleBranch); err != nil {
		t.Fatalf("precondition: stale branch not on remote: %v", err)
	}

	m := newTestManager(t, root, &fakeClient{})
	m.now = func() time.Time { return now }
	recs := []sessionRecord{
		{ID: staleBranch, File: "submodules/sm/ROI.md", Branch: staleBranch, WtPath: staleWt, Activity: now.Add(-120 * time.Minute)},
	}
	if err := m.store.save(recs); err != nil {
		t.Fatalf("save store: %v", err)
	}

	if err := m.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := os.Stat(staleWt); !os.IsNotExist(err) {
		t.Fatalf("stale worktree not removed locally")
	}
	if branchExists(t, g, staleBranch) {
		t.Fatalf("stale branch not deleted locally")
	}
	if _, err := bare.Run(ctx, "rev-parse", "--verify", "refs/heads/"+staleBranch); err == nil {
		t.Fatalf("stale branch not deleted on the remote")
	}

	// A second Reload (fresh Manager, mirroring a later restart) must not
	// resurrect it: the remote copy is gone too, so recoverFromRemote finds
	// nothing to recover.
	m2 := newTestManager(t, root, &fakeClient{})
	if err := m2.Reload(ctx); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if _, ok := m2.Get(staleBranch); ok {
		t.Fatalf("stale session resurrected after being reclaimed")
	}
	if _, err := os.Stat(staleWt); !os.IsNotExist(err) {
		t.Fatalf("stale worktree resurrected on disk")
	}
}
