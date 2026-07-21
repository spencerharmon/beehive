package editor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

// TestReclaimableListsStaleCleanOnly proves Reclaimable is the exact read-only
// preview of Reload: it returns only the stale + change-free edit worktrees
// (sorted), never a fresh, pending, or honeybee worktree, and it mutates nothing
// — every worktree and branch is still present afterward. Applying (Reload) then
// removes exactly what Reclaimable named, so the gc skill's dry-run and apply
// agree by construction.
func TestReclaimableListsStaleCleanOnly(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	g := git.New(root)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	addWt := func(branch string) string {
		wt := filepath.Join(root, ".worktrees", branch)
		if _, err := g.Run(ctx, "worktree", "add", "-b", branch, wt, "main"); err != nil {
			t.Fatalf("worktree add %s: %v", branch, err)
		}
		return wt
	}

	// stale + clean -> reclaimable.
	staleBranch := "hive-edit-stale-100"
	staleWt := addWt(staleBranch)
	// fresh record + clean -> kept.
	activeBranch := "hive-edit-active-200"
	activeWt := addWt(activeBranch)
	// record-less worktree with a committed unmerged change -> pending, kept.
	pendingBranch := "hive-edit-pending-300"
	pendingWt := addWt(pendingBranch)
	pf := filepath.Join(pendingWt, "submodules", "sm", repo.InfraFile)
	if err := os.WriteFile(pf, []byte("pending infra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pg := git.New(pendingWt)
	if _, err := pg.Run(ctx, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Run(ctx, "commit", "-m", "pending edit"); err != nil {
		t.Fatal(err)
	}
	// honeybee worktree -> never enumerated.
	beeWt := addWt("bee-sometask-400")

	m := newTestManager(t, root, &fakeClient{})
	m.now = func() time.Time { return now }
	recs := []sessionRecord{
		{ID: staleBranch, File: "submodules/sm/ROI.md", Branch: staleBranch, WtPath: staleWt, Activity: now.Add(-120 * time.Minute)},
		{ID: activeBranch, File: "submodules/sm/" + repo.InfraFile, Branch: activeBranch, WtPath: activeWt, Activity: now.Add(-1 * time.Minute)},
	}
	if err := m.store.save(recs); err != nil {
		t.Fatal(err)
	}

	got, err := m.Reclaimable(ctx)
	if err != nil {
		t.Fatalf("reclaimable: %v", err)
	}
	if want := []string{staleBranch}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reclaimable = %v, want %v", got, want)
	}

	// Read-only: every worktree and the stale branch still present.
	for _, wt := range []string{staleWt, activeWt, pendingWt, beeWt} {
		if _, err := os.Stat(wt); err != nil {
			t.Fatalf("Reclaimable must not remove %s: %v", wt, err)
		}
	}
	if !branchExists(t, g, staleBranch) {
		t.Fatalf("Reclaimable must not delete branch %s", staleBranch)
	}

	// Consistent with Reload: after Reload reclaims the stale one, Reclaimable is
	// empty (idempotent).
	if err := m.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got2, err := m.Reclaimable(ctx)
	if err != nil {
		t.Fatalf("reclaimable after reload: %v", err)
	}
	if len(got2) != 0 {
		t.Fatalf("after Reload reclaimable should be empty, got %v", got2)
	}
}
