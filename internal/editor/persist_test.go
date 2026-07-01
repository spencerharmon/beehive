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

// seedMainRepo makes a git repo at a temp dir with one commit on main.
func seedMainRepo(t *testing.T) (string, *git.Repo) {
	t.Helper()
	root := t.TempDir()
	g := gitInit(t, root)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(ctx, "seed"); err != nil {
		t.Fatal(err)
	}
	return root, g
}

func addWorktree(t *testing.T, g *git.Repo, root, branch string) string {
	t.Helper()
	wt := filepath.Join(root, ".worktrees", branch)
	if err := g.WorktreeAdd(context.Background(), wt, branch, "main"); err != nil {
		t.Fatalf("worktree add %s: %v", branch, err)
	}
	return wt
}

func branchExists(t *testing.T, g *git.Repo, branch string) bool {
	t.Helper()
	out, err := g.Run(context.Background(), "branch", "--list", branch)
	if err != nil {
		t.Fatalf("branch --list %s: %v", branch, err)
	}
	return strings.TrimSpace(out) != ""
}

// TestSessionStateSurvivesRestart proves an in-flight edit session (with a chat
// log and a committed-but-unmerged proposal on its worktree) is rebuilt after a
// simulated beehived restart: a brand-new Manager over the same repo re-registers
// the session with its log and its proposal intact — nothing is orphaned.
func TestSessionStateSurvivesRestart(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "appended a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("second goal\n")...), 0o644)
	}
	m1 := newTestManager(t, root, fc)
	sess, err := m1.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := sess.ID
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}

	// Simulate a beehived restart: a brand-new Manager over the same repo.
	m2 := newTestManager(t, root, fc)
	if err := m2.Resume(ctx); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, ok := m2.Get(id)
	if !ok {
		t.Fatalf("session %s not resumed after restart", id)
	}
	if got.File != file {
		t.Fatalf("resumed file = %q, want %q", got.File, file)
	}
	if got.Branch != sess.Branch {
		t.Fatalf("resumed branch = %q, want %q", got.Branch, sess.Branch)
	}
	// The chat log survived (user turn + agent reply).
	if log := got.Log(); len(log) < 2 || log[0].Role != "user" {
		t.Fatalf("chat log not preserved across restart: %+v", log)
	}
	// The in-flight proposal survived on the worktree branch (still dirty vs main).
	if st := got.State(ctx); st != "dirty" {
		t.Fatalf("resumed proposal should still be dirty vs main, got %s", st)
	}
	base, proposed, _ := got.Diff(ctx)
	if strings.Contains(base, "second goal") || !strings.Contains(proposed, "second goal") {
		t.Fatalf("resumed diff wrong: base=%q proposed=%q", base, proposed)
	}
}

// TestStartupPrunePreservesActivePendingBeeAndRemovesStale is the core prune
// guarantee: at startup, only STALE + CLEAN edit worktrees are reclaimed, while
// an active session, a stale-but-unmerged (pending) proposal, honeybee bee-*
// worktrees, and the main checkout are all preserved.
func TestStartupPrunePreservesActivePendingBeeAndRemovesStale(t *testing.T) {
	root, g := seedMainRepo(t)
	ctx := context.Background()

	staleWt := addWorktree(t, g, root, "edit-stale-clean") // clean, matches main, stale record
	activeWt := addWorktree(t, g, root, "edit-active")     // clean, matches main, fresh record
	pendingWt := addWorktree(t, g, root, "edit-pending")   // committed ahead of main, stale record
	beeWt := addWorktree(t, g, root, "bee-task1")          // honeybee worktree, must never be touched

	// edit-pending holds a committed-but-unmerged proposal (ahead of main).
	pg := git.New(pendingWt)
	if err := os.WriteFile(filepath.Join(pendingWt, "README.md"), []byte("proposed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pg.Commit(ctx, "pending proposal"); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	old := now.Add(-2 * time.Hour) // beyond the 60m default TTL
	m := newTestManager(t, root, &fakeClient{})
	recs := []persistedSession{
		{ID: "edit-active", File: "submodules/sm/ROI.md", Branch: "edit-active", WtPath: activeWt, LastActive: now},
		{ID: "edit-stale-clean", File: "submodules/sm/ROI.md", Branch: "edit-stale-clean", WtPath: staleWt, LastActive: old},
		{ID: "edit-pending", File: "submodules/sm/ROI.md", Branch: "edit-pending", WtPath: pendingWt, LastActive: old},
		// A record whose worktree no longer exists: must be dropped from the store.
		{ID: "edit-gone", File: "submodules/sm/ROI.md", Branch: "edit-gone", WtPath: filepath.Join(root, ".worktrees", "edit-gone"), LastActive: old},
	}
	if err := writeStore(m.storePath, recs); err != nil {
		t.Fatal(err)
	}

	if err := m.Resume(ctx); err != nil {
		t.Fatalf("resume: %v", err)
	}

	// Stale + clean -> reclaimed (worktree removed, branch deleted, not registered).
	if _, err := os.Stat(staleWt); !os.IsNotExist(err) {
		t.Fatalf("edit-stale-clean worktree should be removed, stat err=%v", err)
	}
	if branchExists(t, g, "edit-stale-clean") {
		t.Fatal("edit-stale-clean branch should be deleted")
	}
	if _, ok := m.Get("edit-stale-clean"); ok {
		t.Fatal("edit-stale-clean must not be registered")
	}

	// Active -> kept + resumed.
	if _, err := os.Stat(activeWt); err != nil {
		t.Fatalf("edit-active worktree should survive: %v", err)
	}
	if !branchExists(t, g, "edit-active") {
		t.Fatal("edit-active branch should survive")
	}
	if _, ok := m.Get("edit-active"); !ok {
		t.Fatal("edit-active should be resumed")
	}

	// Pending (stale by time but unmerged) -> preserved + resumed (safety veto).
	if _, err := os.Stat(pendingWt); err != nil {
		t.Fatalf("edit-pending worktree must be preserved: %v", err)
	}
	if !branchExists(t, g, "edit-pending") {
		t.Fatal("edit-pending branch must be preserved")
	}
	if _, ok := m.Get("edit-pending"); !ok {
		t.Fatal("edit-pending should be resumed")
	}

	// Honeybee worktree -> never touched, never registered.
	if _, err := os.Stat(beeWt); err != nil {
		t.Fatalf("bee-task1 worktree must not be touched: %v", err)
	}
	if !branchExists(t, g, "bee-task1") {
		t.Fatal("bee-task1 branch must not be deleted")
	}
	if _, ok := m.Get("bee-task1"); ok {
		t.Fatal("bee-task1 is not an editor session; must not be registered")
	}

	// Main checkout untouched.
	if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
		t.Fatalf("main checkout must be intact: %v", err)
	}

	// Store now lists exactly the surviving sessions.
	saved, err := loadStore(m.storePath)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range saved {
		got[r.Branch] = true
	}
	if !got["edit-active"] || !got["edit-pending"] {
		t.Fatalf("store must keep active+pending, got %v", got)
	}
	if got["edit-stale-clean"] || got["edit-gone"] {
		t.Fatalf("store must drop stale/gone records, got %v", got)
	}

	// Idempotent: a second Resume is a stable no-op on the survivors.
	if err := m.Resume(ctx); err != nil {
		t.Fatalf("resume#2: %v", err)
	}
	if _, ok := m.Get("edit-active"); !ok {
		t.Fatal("edit-active lost on second resume")
	}
	if _, ok := m.Get("edit-pending"); !ok {
		t.Fatal("edit-pending lost on second resume")
	}
	if _, err := os.Stat(pendingWt); err != nil {
		t.Fatalf("edit-pending must still exist after second resume: %v", err)
	}
}

// TestResumeNoStoreNoWorktreesIsNoop confirms Resume on a fresh repo (no store,
// no edit worktrees) is a clean no-op — the daemon-startup common case.
func TestResumeNoStoreNoWorktreesIsNoop(t *testing.T) {
	root, _ := seedMainRepo(t)
	m := newTestManager(t, root, &fakeClient{})
	if err := m.Resume(context.Background()); err != nil {
		t.Fatalf("resume on fresh repo: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("no sessions expected, got %d", len(m.List()))
	}
}
