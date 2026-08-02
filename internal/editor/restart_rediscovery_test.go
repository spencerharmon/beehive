package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRestartRediscoveryRecoversSessionWhenRemoteUnreachable is the editor-
// session-restart-rediscovery acceptance test. It reproduces the observed live
// failure: an operator's in-progress editor session (a real committed change on
// its edit branch PLUS an uncommitted dirty working-tree edit on top) VANISHED
// from the editor after a beehived restart, because startup rediscovery (Reload)
// aborted the moment the repo's trusted remote was momentarily unreachable —
// dropping every LOCAL in-flight session even though not one of them needs the
// network to be re-registered.
//
// The test:
//  1. opens a session via the runtime API (m.Open) with a reachable repo-own
//     remote, lands a committed change on its branch (a chat turn), and adds a
//     dirty uncommitted edit on top;
//  2. makes the remote unreachable (the bare origin is removed) — exactly the
//     restart-time flakiness that triggered the loss;
//  3. constructs a FRESH Manager over the same repo + .worktrees state (no shared
//     memory with the old one — a real process restart) and runs the startup
//     recovery (Reload, the path beehived's RecoverEditors drives); and
//  4. asserts the fresh instance's list/get/diff surface shows the session with
//     BOTH its committed and its uncommitted changes.
//
// It FAILS against the pre-fix code (Reload returns the remote fetch error and
// the session is never registered) and PASSES once Reload degrades an
// unreachable remote to local-only recovery.
func TestRestartRediscoveryRecoversSessionWhenRemoteUnreachable(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	bare, _ := remoteSetup(t, root)
	file := "submodules/sm/ROI.md"

	fc := &fakeClient{reply: "committed the goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("committed goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.remote != "origin" {
		t.Fatalf("want trusted remote origin at open, got %q", sess.remote)
	}
	branch := sess.Branch
	// A chat turn commits the proposal onto the edit branch.
	if _, err := sess.Chat(ctx, "add the committed goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if e := sess.Err(); e != "" {
		t.Fatalf("unexpected session error: %s", e)
	}
	// An uncommitted dirty working-tree edit ON TOP of the commit.
	wtFile := filepath.Join(sess.wtPath, filepath.FromSlash(file))
	b, _ := os.ReadFile(wtFile)
	if err := os.WriteFile(wtFile, append(b, []byte("uncommitted dirty goal\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remote becomes unreachable at restart time (origin gone).
	if err := os.RemoveAll(bare); err != nil {
		t.Fatal(err)
	}

	// A brand-new Manager (no shared memory) over the same repo — a real restart —
	// runs the startup recovery beehived drives at boot.
	m2 := newTestManager(t, root, fc)
	if err := m2.Reload(ctx); err != nil {
		t.Fatalf("startup rediscovery must not fail when the remote is unreachable: %v", err)
	}

	got, ok := m2.Get(branch)
	if !ok {
		t.Fatalf("session %s vanished after restart with an unreachable remote", branch)
	}
	inList := false
	for _, s := range m2.List() {
		if s.Branch == branch {
			inList = true
		}
	}
	if !inList {
		t.Fatalf("rediscovered session %s not present in List() after restart", branch)
	}

	_, proposed, derr := got.Diff(ctx)
	if derr != nil {
		t.Fatalf("diff of rediscovered session: %v", derr)
	}
	if !strings.Contains(proposed, "committed goal") {
		t.Fatalf("rediscovered session is missing its COMMITTED change: %q", proposed)
	}
	if !strings.Contains(proposed, "uncommitted dirty goal") {
		t.Fatalf("rediscovered session is missing its UNCOMMITTED dirty change: %q", proposed)
	}
}
