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

// remoteSetup creates a bare origin, configures it as root's "origin" remote,
// and seeds it with root's current main — the shared repo-own-remote fixture
// every durability test needs (mirrors the inline setup TestSessionMergeAuto-
// PushesRemote in editor_test.go already uses).
func remoteSetup(t *testing.T, root string) (bare string, bg *git.Repo) {
	t.Helper()
	ctx := context.Background()
	bare = t.TempDir()
	if _, err := git.New(bare).Run(ctx, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatalf("bare init: %v", err)
	}
	g := git.New(root)
	if _, err := g.Run(ctx, "remote", "add", "origin", bare); err != nil {
		t.Fatalf("remote add origin: %v", err)
	}
	if _, err := g.Run(ctx, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("push origin main: %v", err)
	}
	return bare, git.New(bare)
}

// TestSessionDurabilityPushesEditBranchAfterTurnAndIsFetchable is the core
// push-on-turn acceptance: with a repo-own remote, a chat turn's commit is
// pushed to the edit branch (carrying both the proposed file content and the
// transcript sidecar), and that branch is fetchable from a wholly independent
// second clone of the remote — proving it converged through git like a
// honeybee's bee-<taskid> branch, not just a local-only worktree.
func TestSessionDurabilityPushesEditBranchAfterTurnAndIsFetchable(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	_, bg := remoteSetup(t, root)

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("durable goal\n")...), 0o644)
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

	localTip, err := sess.wt.RevParse(ctx, sess.Branch)
	if err != nil {
		t.Fatalf("local branch tip: %v", err)
	}
	remoteTip, err := bg.RevParse(ctx, sess.Branch)
	if err != nil {
		t.Fatalf("edit branch not pushed to origin: %v", err)
	}
	if remoteTip != localTip {
		t.Fatalf("pushed tip %s != local tip %s", remoteTip, localTip)
	}
	out, err := bg.Run(ctx, "show", sess.Branch+":"+file)
	if err != nil || !strings.Contains(out, "durable goal") {
		t.Fatalf("pushed branch missing the edit: out=%q err=%v", out, err)
	}
	tOut, err := bg.Run(ctx, "show", sess.Branch+":"+transcriptSidecarPath)
	if err != nil || !strings.Contains(tOut, "add a goal") {
		t.Fatalf("pushed branch missing the transcript sidecar: out=%q err=%v", tOut, err)
	}

	// Fetchable from a SECOND, wholly independent clone that never saw the push
	// directly — proves durability through the remote, not local coincidence.
	parent := t.TempDir()
	clone2 := filepath.Join(parent, "clone2")
	if _, err := git.New(parent).Run(ctx, "clone", "-q", bg.Dir, clone2); err != nil {
		t.Fatalf("second clone: %v", err)
	}
	c2 := git.New(clone2)
	if got, err := c2.RevParse(ctx, "refs/remotes/origin/"+sess.Branch); err != nil || got != localTip {
		t.Fatalf("edit branch not fetchable from a second clone: got=%q err=%v", got, err)
	}
}

// TestNoRemoteSkipsDurabilityAsNoOp is the sharing-modes contract: with no
// remote configured, chat-diff-session-durability must be byte-for-byte
// invisible — no sidecar ever written, and Merge/State behavior identical to
// before the feature existed.
func TestNoRemoteSkipsDurabilityAsNoOp(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/ROI.md"
	var wtDir string
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		wtDir = dir
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
		t.Fatalf("no remote configured: want remote \"\", got %q", sess.remote)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if e := sess.Err(); e != "" {
		t.Fatalf("unexpected session error: %s", e)
	}
	if _, err := os.Stat(filepath.Join(wtDir, transcriptSidecarPath)); !os.IsNotExist(err) {
		t.Fatalf("no-remote session must never write the durability sidecar, stat err=%v", err)
	}
	if err := sess.Merge(ctx); err != nil {
		t.Fatalf("merge: %v", err)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "local goal") {
		t.Fatalf("merge did not reach main: %q", string(onMain))
	}
}

// TestReloadRebuildsWorktreeWhenOnlyCheckoutDirWiped covers the general
// robustness fix chat-diff-session-durability's recovery logic depends on
// (needed even with NO remote): git's own `worktree list` still registers a
// worktree whose checkout directory was removed out of band (e.g. a wiped
// scratch dir) as a dangling/"prunable" entry. Reload must prune that stale
// registration and rebuild the checkout from the surviving LOCAL branch,
// rather than erroring out or losing the session.
func TestReloadRebuildsWorktreeWhenOnlyCheckoutDirWiped(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("wiped-dir goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	branch := sess.Branch
	wtPath := sess.wtPath
	if _, err := sess.Chat(ctx, "add a goal please"); err != nil {
		t.Fatalf("chat: %v", err)
	}

	// Wipe ONLY the checkout directory, out of band (no git command at all).
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}
	if err := m.store.save(nil); err != nil { // drop the persisted record too
		t.Fatal(err)
	}

	m2 := newTestManager(t, root, fc)
	if err := m2.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := m2.Get(branch)
	if !ok {
		t.Fatalf("session %s not recovered after checkout dir was wiped", branch)
	}
	if _, err := os.Stat(got.wtPath); err != nil {
		t.Fatalf("worktree not rebuilt on disk: %v", err)
	}
	_, proposed, derr := got.Diff(ctx)
	if derr != nil {
		t.Fatalf("diff: %v", derr)
	}
	if !strings.Contains(proposed, "wiped-dir goal") {
		t.Fatalf("recovered content wrong: %q", proposed)
	}
}

// TestReloadRecoversAfterLocalWorktreeAndBranchLost is the headline chat-diff-
// session-durability acceptance: simulating a TOTAL local loss (checkout
// directory gone, local branch ref gone, persisted store record dropped —
// exactly what a different host, or a fully wiped local repo, looks like)
// still recovers the session on Reload, purely from the trusted remote: the
// worktree is rebuilt from the fetched tip AND the chat transcript is
// recovered from the branch's own committed sidecar.
func TestReloadRecoversAfterLocalWorktreeAndBranchLost(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	_, bg := remoteSetup(t, root)
	g := git.New(root)

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("recovered goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	branch := sess.Branch
	wtPath := sess.wtPath
	if _, err := sess.Chat(ctx, "please add a goal, we'll talk more later"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if e := sess.Err(); e != "" {
		t.Fatalf("unexpected session error before loss: %s", e)
	}
	if _, err := bg.RevParse(ctx, branch); err != nil {
		t.Fatalf("branch not pushed before simulating loss: %v", err)
	}

	// Simulate TOTAL local loss: proper worktree removal, local branch deleted,
	// and the persisted store record dropped. Only the remote's pushed copy
	// (from the turn above) survives.
	if _, err := g.Run(ctx, "worktree", "remove", "--force", wtPath); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := g.Run(ctx, "branch", "-D", branch); err != nil {
		t.Fatalf("delete local branch: %v", err)
	}
	if err := m.store.save(nil); err != nil {
		t.Fatalf("drop store record: %v", err)
	}

	// A brand-new Manager (no shared memory with m) recovers purely from the
	// trusted remote — mirrors a beehived restart on a different host.
	m2 := newTestManager(t, root, fc)
	if err := m2.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := m2.Get(branch)
	if !ok {
		t.Fatalf("session %s not recovered from remote after total local loss", branch)
	}
	if got.File != file {
		t.Fatalf("recovered file = %q, want %q", got.File, file)
	}
	if _, err := os.Stat(got.wtPath); err != nil {
		t.Fatalf("worktree not rebuilt on disk: %v", err)
	}
	base, proposed, derr := got.Diff(ctx)
	if derr != nil {
		t.Fatalf("diff: %v", derr)
	}
	if strings.Contains(base, "recovered goal") || !strings.Contains(proposed, "recovered goal") {
		t.Fatalf("recovered diff wrong: base=%q proposed=%q", base, proposed)
	}
	log := got.Log()
	if len(log) == 0 {
		t.Fatalf("transcript not recovered from branch")
	}
	foundUser := false
	for _, turn := range log {
		if turn.Role == "user" && strings.Contains(turn.Text, "please add a goal") {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatalf("recovered log missing the original user turn: %+v", log)
	}
	// Still mergeable after recovery.
	if err := got.Merge(ctx); err != nil {
		t.Fatalf("merge recovered session: %v", err)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "recovered goal") {
		t.Fatalf("recovered merge did not reach main: %q", string(onMain))
	}
}

// TestMergeStripsTranscriptSidecarBeforePublish proves the durability sidecar
// never leaks into main: it exists mid-session (proving the strip step does
// something real) but is gone from both the local main working tree and the
// published remote main immediately after Merge.
func TestMergeStripsTranscriptSidecarBeforePublish(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	_, bg := remoteSetup(t, root)

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("clean publish goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sess.wtPath, transcriptSidecarPath)); err != nil {
		t.Fatalf("sidecar should exist pre-merge: %v", err)
	}
	if err := sess.Merge(ctx); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, transcriptSidecarPath)); !os.IsNotExist(err) {
		t.Fatalf("sidecar must never reach the local main working tree: stat err=%v", err)
	}
	if _, err := bg.Run(ctx, "show", "main:"+transcriptSidecarPath); err == nil {
		t.Fatalf("sidecar must never reach the published remote main")
	}
}

// TestPushFailureSurfacesNeverSwallowed proves a durability push failure is
// surfaced (via the session's error state), never silently swallowed, and
// that the underlying edit itself is not lost when only the push fails.
func TestPushFailureSurfacesNeverSwallowed(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	bare, _ := remoteSetup(t, root)

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("push failure goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.remote != "origin" {
		t.Fatalf("want trusted remote origin, got %q", sess.remote)
	}
	// Break the remote AFTER Open (which already verified + trusted it): remove
	// the bare repo entirely, so this turn's push fails with a real git error.
	if err := os.RemoveAll(bare); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat itself must not fail (a push failure surfaces via Err(), not a returned error): %v", err)
	}
	if e := sess.Err(); e == "" || !strings.Contains(e, "push") {
		t.Fatalf("want a surfaced push failure, got %q", e)
	}
	_, proposed, derr := sess.Diff(ctx)
	if derr != nil {
		t.Fatalf("diff: %v", derr)
	}
	if !strings.Contains(proposed, "push failure goal") {
		t.Fatalf("edit lost after push failure: %q", proposed)
	}
}

// TestReclaimDeletesRemoteBranchPreventingResurrection proves reclaim's
// remote-branch cleanup: a session whose only branch/main diff is the
// durability sidecar itself (a Q&A-only turn — no real file edit) is NOT
// "pending" and is reclaimed once stale, on a fresh Reload — and that reclaim
// also deletes the branch's pushed remote copy, so a LATER Reload can never
// resurrect it from that leftover.
func TestReclaimDeletesRemoteBranchPreventingResurrection(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	_, bg := remoteSetup(t, root)
	g := git.New(root)

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "just answering, no edit."} // editFn nil: never touches the file
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	branch := sess.Branch
	if _, err := sess.Chat(ctx, "what does this file say?"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if e := sess.Err(); e != "" {
		t.Fatalf("unexpected session error: %s", e)
	}
	if _, err := bg.RevParse(ctx, branch); err != nil {
		t.Fatalf("branch not pushed before reclaim test: %v", err)
	}

	// A fresh Manager (mirrors a beehived restart) with the clock advanced well
	// past TTL: stale + non-pending -> reclaimed, locally AND on the remote.
	m2 := newTestManager(t, root, &fakeClient{})
	future := time.Now().Add(2 * m2.ttl)
	m2.now = func() time.Time { return future }
	if err := m2.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if branchExists(t, g, branch) {
		t.Fatalf("stale session's local branch should be reclaimed")
	}
	if _, err := bg.RevParse(ctx, branch); err == nil {
		t.Fatalf("stale session's REMOTE branch must also be deleted (else a later Reload resurrects it)")
	}

	// Resurrection check: yet another fresh manager/reload must not bring it
	// back from any remote leftover.
	m3 := newTestManager(t, root, &fakeClient{})
	if err := m3.Reload(ctx); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if _, ok := m3.Get(branch); ok {
		t.Fatalf("reclaimed session must never be resurrected from a stale remote leftover")
	}
}

// TestCloseDeletesRemoteBranchPreventingResurrection proves an intentionally
// closed session's pushed remote copy is cleaned up too, so it can never come
// back from a later Reload's remote scan.
func TestCloseDeletesRemoteBranchPreventingResurrection(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	_, bg := remoteSetup(t, root)

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("closed goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	branch := sess.Branch
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, err := bg.RevParse(ctx, branch); err != nil {
		t.Fatalf("branch not pushed before close: %v", err)
	}
	if err := m.Close(ctx, sess.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := bg.RevParse(ctx, branch); err == nil {
		t.Fatalf("closed session's remote branch must be deleted too")
	}
}
