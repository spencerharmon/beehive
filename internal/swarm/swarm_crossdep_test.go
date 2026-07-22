package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// bareRepoWithFile creates a bare origin holding one commit on main that carries a
// single marker file, and returns the bare repo's path (a file:// submodule URL).
func bareRepoWithFile(t *testing.T, marker string) string {
	t.Helper()
	ctx := context.Background()
	bare := t.TempDir()
	if _, err := git.New(bare).Run(ctx, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	work := t.TempDir()
	wg := gitInit(t, work)
	os.WriteFile(filepath.Join(work, marker), []byte("dep code\n"), 0o644)
	if _, err := wg.Run(ctx, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wg.Run(ctx, "commit", "-q", "-m", "dep base"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := wg.Run(ctx, "remote", "add", "origin", bare); err != nil {
		t.Fatalf("remote: %v", err)
	}
	if _, err := wg.Run(ctx, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("push: %v", err)
	}
	return bare
}

// TestInitLinkedSubmoduleCheckoutsPopulatesCrossDep is the regression test for the
// empty-sibling-checkout bug: a work pass whose task cross-depends on another
// submodule must be able to READ that dependency's real code, not an empty gitlink.
// A fresh beehive worktree leaves sibling submodules/<sm>/repo empty; the runner must
// check the named cross-dep submodule out at its recorded gitlink before the pass.
func TestInitLinkedSubmoduleCheckoutsPopulatesCrossDep(t *testing.T) {
	ctx := context.Background()
	depOrigin := bareRepoWithFile(t, "MARKER")

	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	// Allow file:// submodule transport for this repo (test-local origins).
	if _, err := g.Run(ctx, "config", "protocol.file.allow", "always"); err != nil {
		t.Fatalf("config protocol.file.allow: %v", err)
	}
	// Own submodule "sm" (a plain nested checkout is enough — the runner never
	// re-inits the pass's own submodule here) and its beehive-layer files.
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	gitInit(t, filepath.Join(sm, "repo"))
	os.WriteFile(filepath.Join(sm, "repo", "f"), []byte("x"), 0o644)
	git.New(filepath.Join(sm, "repo")).Commit(ctx, "own base")
	os.WriteFile(sm+"/PLAN.md", []byte("## T [TODO] <!-- attempts=0 deps=dep:base-job -->\ngo\n"), 0o644)

	// Real dependency submodule "dep" at submodules/dep/repo, tracked as a gitlink.
	if _, err := g.Run(ctx, "submodule", "add", depOrigin, "submodules/dep/repo"); err != nil {
		t.Fatalf("submodule add dep: %v", err)
	}
	os.MkdirAll(filepath.Join(root, "submodules", "dep", "docs"), 0o755)
	os.WriteFile(root+"/submodules/dep/PLAN.md", []byte("## base-job [DONE] <!-- attempts=0 deps= commits=none -->\ngo\n"), 0o644)
	if _, err := g.Run(ctx, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-q", "-m", "seed hive with real dep submodule"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Fresh beehive worktree: `git worktree add` does NOT populate submodules, so the
	// sibling dep checkout starts EMPTY — exactly the state a honeybee pass gets.
	wt := filepath.Join(root, ".worktrees", "bee-x")
	if err := g.WorktreeAdd(ctx, wt, "bee-x", "main"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	depRepoInWT := filepath.Join(wt, "submodules", "dep", "repo")
	if isSourceCheckout(ctx, depRepoInWT) {
		t.Fatalf("precondition failed: dep checkout is already populated in a fresh worktree")
	}

	rp, err := repo.Open(wt)
	if err != nil {
		t.Fatalf("open worktree repo: %v", err)
	}
	subs, _ := rp.Submodules()
	var own repo.Submodule
	for _, s := range subs {
		if s.Name == "sm" {
			own = s
		}
	}
	if own.Name == "" {
		t.Fatalf("own submodule sm not found among %v", subs)
	}
	r := &Runner{Repo: rp, Git: git.New(wt)}
	sel := &selectt.Selection{
		Kind:      selectt.Work,
		Submodule: own,
		Task:      plan.Task{ID: "T", Status: plan.TODO, Deps: []string{"dep:base-job"}},
	}

	r.initLinkedSubmoduleCheckouts(ctx, sel, wt)

	// The cross-dep submodule is now a real checkout carrying its committed code.
	if !isSourceCheckout(ctx, depRepoInWT) {
		t.Fatalf("cross-dep submodule was not checked out into the worktree")
	}
	if _, err := os.Stat(filepath.Join(depRepoInWT, "MARKER")); err != nil {
		t.Fatalf("dependency code (MARKER) not readable after checkout: %v", err)
	}
}

// TestInitLinkedSubmoduleCheckoutsSkipsLocalAndOwnAndUnknown proves the selector
// only acts on cross-submodule deps that name a real sibling: a bare local dep, the
// pass's own submodule, and an unknown submodule name are all ignored, and the call
// is a harmless no-op (best-effort, never aborts).
func TestInitLinkedSubmoduleCheckoutsSkipsLocalAndOwnAndUnknown(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	gitInit(t, filepath.Join(sm, "repo"))
	os.WriteFile(filepath.Join(sm, "repo", "f"), []byte("x"), 0o644)
	git.New(filepath.Join(sm, "repo")).Commit(ctx, "base")
	os.WriteFile(sm+"/PLAN.md", []byte("## T [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	if _, err := g.Run(ctx, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	r := &Runner{Repo: rp, Git: g}
	sel := &selectt.Selection{
		Kind:      selectt.Work,
		Submodule: subs[0],
		// local dep (no colon), own submodule, and an unknown submodule — none should
		// trigger a checkout or an error.
		Task: plan.Task{ID: "T", Status: plan.TODO, Deps: []string{"some-local-dep", "sm:other", "ghost:x"}},
	}
	// Must not panic or abort; nothing to assert beyond a clean return.
	r.initLinkedSubmoduleCheckouts(ctx, sel, root)
}
