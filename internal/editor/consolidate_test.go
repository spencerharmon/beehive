package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
)

// edit-session-consolidation tests: the generalized OpenSession path (whole-tree
// resolve sessions, the one-shot orientation preamble, and Kind/Meta/whole-tree
// persistence + recovery) that lets the resolve and bootstrap agents ride this
// Manager instead of their own parallel machinery.

// TestOpenSessionWholeTreeCommitsDiffsAndMerges is the resolve-mode core: a
// whole-tree session commits EVERY file the agent writes (git add -A), reports
// them via ChangedFiles/TreeDiff/DiffStat and a dirty State, and Merge lands the
// whole tree on main.
func TestOpenSessionWholeTreeCommitsDiffsAndMerges(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	fc := &fakeClient{reply: "Edited two beehive-layer files."}
	fc.editFn = func(dir string) {
		// Two arbitrary beehive-layer files, one new — exactly what the single-file
		// editor could never express.
		roi := filepath.Join(dir, "submodules", "sm", "ROI.md")
		_ = os.WriteFile(roi, []byte("# ROI\n\noriginal goal\nnew goal\n"), 0o644)
		infra := filepath.Join(dir, "submodules", "sm", "INFRASTRUCTURE.md")
		_ = os.WriteFile(infra, []byte("# infra\ndocumented\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.OpenSession(ctx, Spec{
		WholeTree:    true,
		Unrestricted: true,
		Kind:         KindResolve,
		System:       "resolve system prompt",
		Slug:         "resolve-sm-t1",
		Meta:         map[string]string{"sub": "sm", "task": "t1"},
	})
	if err != nil {
		t.Fatalf("open whole-tree: %v", err)
	}
	if _, err := sess.Chat(ctx, "make the edits"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	files, err := sess.ChangedFiles(ctx)
	if err != nil {
		t.Fatalf("changed files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("whole-tree session must report BOTH changed files, got %v", files)
	}
	if st := sess.State(ctx); st != "dirty" {
		t.Fatalf("state = %q, want dirty", st)
	}
	td, err := sess.TreeDiff(ctx)
	if err != nil || len(td) != 2 {
		t.Fatalf("TreeDiff = %v (err %v), want 2 file changes", td, err)
	}
	if stat, _ := sess.DiffStat(ctx); !strings.Contains(stat, "ROI.md") || !strings.Contains(stat, "INFRASTRUCTURE.md") {
		t.Fatalf("DiffStat missing a changed file: %q", stat)
	}
	if err := sess.Merge(ctx); err != nil {
		t.Fatalf("merge: %v", err)
	}
	// Both files are now on main.
	got := gitShow(t, root, "HEAD", "submodules/sm/INFRASTRUCTURE.md")
	if !strings.Contains(got, "documented") {
		t.Fatalf("whole-tree merge did not land the new file on main: %q", got)
	}
	if st := sess.State(ctx); st != "live" {
		t.Fatalf("state after merge = %q, want live", st)
	}
}

// TestResolvePreambleOnFirstTurnOnly: a Spec.Preamble is prepended to the FIRST
// fresh turn (so the resolve agent investigates before proposing) and never
// again.
func TestResolvePreambleOnFirstTurnOnly(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	fc := &fakeClient{reply: "ok"}
	m := newTestManager(t, root, fc)
	sess, err := m.OpenSession(ctx, Spec{
		WholeTree: true,
		Kind:      KindResolve,
		System:    "sys",
		Slug:      "resolve-sm-t2",
		Preamble:  "PREAMBLE-INVESTIGATE-FIRST",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sess.Chat(ctx, "first message"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(fc.firstSeen, "PREAMBLE-INVESTIGATE-FIRST") || !strings.Contains(fc.firstSeen, "first message") {
		t.Fatalf("first turn must carry the preamble AND the message: %q", fc.firstSeen)
	}
	// A second turn reuses the opencode session (oc != nil), so the preamble is not
	// re-sent; even if the session had to reconnect, preambleUsed guards it.
	if _, err := sess.Chat(ctx, "second message"); err != nil {
		t.Fatalf("chat 2: %v", err)
	}
	// The last NewSession first-message (only ever set on connect) must not have
	// been re-seeded with the preamble for the second turn.
	if strings.Count(fc.firstSeen, "PREAMBLE-INVESTIGATE-FIRST") > 0 && strings.Contains(fc.firstSeen, "second message") {
		t.Fatalf("preamble must not be re-sent on a later turn: %q", fc.firstSeen)
	}
}

// TestKindAndMetaPersistAndRecover: a resolve session's Kind, Meta, whole-tree
// mode, and system prompt survive a Manager restart (store round-trip), so the
// web layer re-associates the recovered session with its task.
func TestKindAndMetaPersistAndRecover(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	fc := &fakeClient{reply: "ok"}
	fc.editFn = func(dir string) {
		_ = os.WriteFile(filepath.Join(dir, "submodules", "sm", "ROI.md"), []byte("# ROI\n\noriginal goal\nedited\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.OpenSession(ctx, Spec{
		WholeTree:    true,
		Unrestricted: true,
		Kind:         KindResolve,
		System:       "blocker system prompt",
		Slug:         "resolve-sm-t3",
		Meta:         map[string]string{"sub": "sm", "task": "t3"},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// A committed change makes the session pending, so recovery keeps it.
	if _, err := sess.Chat(ctx, "edit the roi"); err != nil {
		t.Fatalf("chat: %v", err)
	}

	// Simulate a restart: a fresh Manager over the same root recovers from the store.
	fc2 := &fakeClient{reply: "ok"}
	m2 := newTestManager(t, root, fc2)
	if err := m2.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := m2.Get(sess.ID)
	if !ok {
		t.Fatalf("resolve session %s not recovered after restart", sess.ID)
	}
	if got.Kind() != KindResolve {
		t.Fatalf("recovered kind = %q, want resolve", got.Kind())
	}
	md := got.Meta()
	if md["sub"] != "sm" || md["task"] != "t3" {
		t.Fatalf("recovered meta lost: %v", md)
	}
	// Still whole-tree: reports the changed file via the range diff, not a single File.
	files, err := got.ChangedFiles(ctx)
	if err != nil || len(files) != 1 {
		t.Fatalf("recovered whole-tree session changed files = %v (err %v)", files, err)
	}
}

// TestBootstrapKindSingleFileMergeableRecovers: a bootstrap session is a
// single-file (LOCALS.md) editor session with AutoMerge off; it persists as
// KindBootstrap and recovers.
func TestBootstrapKindSingleFileMergeableRecovers(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	fc := &fakeClient{reply: "Drafted LOCALS.md."}
	fc.editFn = func(dir string) {
		_ = os.WriteFile(filepath.Join(dir, "LOCALS.md"), []byte("# LOCALS\nhost: x\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.OpenSession(ctx, Spec{
		File:   "LOCALS.md",
		Kind:   KindBootstrap,
		System: "bootstrap system prompt",
		Slug:   "bootstrap",
		Intro:  []Turn{{Role: "system", Text: "welcome"}},
	})
	if err != nil {
		t.Fatalf("open bootstrap: %v", err)
	}
	if got := sess.Log(); len(got) != 1 || got[0].Role != "system" {
		t.Fatalf("intro not seeded: %+v", got)
	}
	if _, err := sess.Chat(ctx, "draft locals"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if sess.State(ctx) != "dirty" {
		t.Fatalf("bootstrap draft must be dirty until merged")
	}
	// Recovers as KindBootstrap.
	fc2 := &fakeClient{reply: "ok"}
	m2 := newTestManager(t, root, fc2)
	if err := m2.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := m2.Get(sess.ID)
	if !ok || got.Kind() != KindBootstrap {
		t.Fatalf("bootstrap session not recovered as KindBootstrap (ok=%v)", ok)
	}
}

// gitShow returns the trimmed content of path at ref in the repo at dir.
func gitShow(t *testing.T, dir, ref, path string) string {
	t.Helper()
	out, err := git.New(dir).Show(context.Background(), ref, path)
	if err != nil {
		t.Fatalf("git show %s:%s: %v", ref, path, err)
	}
	return strings.TrimRight(out, "\n")
}
