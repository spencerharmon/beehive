package editor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// hasAgentReply reports whether the session log carries an agent turn — i.e. the
// assistant has produced its reply and it is ready for the human to read.
func hasAgentReply(log []Turn) bool {
	for _, t := range log {
		if t.Role == "agent" {
			return true
		}
	}
	return false
}

// writeBlockingPreReceive installs a pre-receive hook on the bare remote that
// stalls every push until sentinel is removed (capped so a crashed test can
// never hang the suite). It gives the test a deterministic window in which a
// turn's assistant reply is already recorded in the session log while the
// turn's post-reply publish work (transcript commit + branch push) is still
// running in the background — exactly the state the polling UI reads.
func writeBlockingPreReceive(t *testing.T, bare, sentinel string) {
	t.Helper()
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	// Drain stdin (git streams the ref updates there) then spin until the
	// sentinel disappears or the ~20s cap trips.
	script := fmt.Sprintf("#!/bin/sh\ncat >/dev/null 2>&1\ni=0\nwhile [ -e %q ]; do\n  sleep 0.05\n  i=$((i+1))\n  if [ $i -gt 400 ]; then break; fi\ndone\nexit 0\n", sentinel)
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}
}

// TestStatusClearsWhenReplyReadyWhilePublishing reproduces the stuck-status bug
// on the polling path (chat-editor-status-poll-failing-test): the chat-diff
// editor's status indicator ("editing…"/"working…") is driven by
// Session.Busy() (the /editor state poll reports it verbatim), but runTurn keeps
// busy=true across ALL post-reply work — the local commit, the durability
// transcript commit, and the branch push — clearing it only at the very end. So
// once the assistant's reply is already appended to the log and ready to read,
// the status is STILL "working…" until that publish work finishes. With a
// trusted remote whose push is stalled, this window is wide and deterministic.
//
// This test is the intended failing gate for chat-editor-status-poll-fix: it
// FAILS on today's logic (Busy() still true after the reply is ready) and will
// pass once the status is cleared as soon as the reply is recorded.
func TestStatusClearsWhenReplyReadyWhilePublishing(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	bare, _ := remoteSetup(t, root)

	// Sentinel present == pushes block. Kept in its own temp dir so it outlives
	// nothing unexpectedly; removed to release the stalled background push.
	sentinel := filepath.Join(t.TempDir(), "block-push")
	if err := os.WriteFile(sentinel, []byte("x"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	defer os.Remove(sentinel)
	writeBlockingPreReceive(t, bare, sentinel)

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("polled goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.remote != "origin" {
		t.Fatalf("want trusted remote origin, got %q", sess.remote)
	}

	// Drive an assistant turn in the background, exactly as the UI does (the
	// handler returns immediately and the panel polls status).
	if err := sess.StartChat(context.Background(), "add a goal"); err != nil {
		t.Fatalf("start chat: %v", err)
	}

	// Wait until the assistant reply is READY (present in the log). The push is
	// still stalled on the sentinel, so the turn's post-reply publish work has
	// NOT finished.
	deadline := time.Now().Add(10 * time.Second)
	for !hasAgentReply(sess.Log()) {
		if time.Now().After(deadline) {
			os.Remove(sentinel)
			t.Fatalf("assistant reply never became ready: %v", sess.Err())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The reply is ready — the status the poll reports MUST be cleared. On the
	// current polling logic it is not: Busy() is still true (stuck on
	// "working…"/"editing…") because the background push is still in flight.
	stuck := sess.Busy()

	// Release the stalled push and let the background turn fully drain BEFORE
	// returning, so the worktree/remote are quiescent when the temp dirs are
	// torn down (a still-running push otherwise races TempDir cleanup). Busy()
	// itself now clears the instant the reply is ready (the fix under test), so
	// draining on it would return immediately while the commit/transcript/push
	// publish work is still running in the background; wait on the session's
	// internal turn-in-flight flag instead (same package: direct field access
	// under its own lock) so teardown only proceeds once that work is done.
	os.Remove(sentinel)
	drain := time.Now().Add(15 * time.Second)
	for {
		sess.mu.Lock()
		on := sess.turnOn
		sess.mu.Unlock()
		if !on || time.Now().After(drain) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if stuck {
		t.Fatalf("stuck status: the chat-diff editor still reports Busy()==true (indicator stuck on \"working…\"/\"editing…\") after the assistant reply is already ready; the status poll does not clear until post-reply publish work completes")
	}
}
