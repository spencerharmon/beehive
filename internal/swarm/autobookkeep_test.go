package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// TestAutoBookkeepSynthesizesMissingCommitsTagAndCommits is the regression guard
// for handoff-runner-owned-bookkeeping: a Work pass that flipped its task
// NEEDS-REVIEW and wrote a real change doc, but left the doc uncommitted and the
// `commits=`/`Beehive-Commits` tag unset, is healed automatically — the runner
// discovers the real submodule commit from the code worktree, stamps both the
// PLAN.md `commits=` tag and the doc's `Beehive-Commits` header with it, and
// commits the doc + PLAN.md flip to the hive branch itself — without asking the
// agent to redo mechanical bookkeeping it can derive deterministically.
func TestAutoBookkeepSynthesizesMissingCommitsTagAndCommits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hiveGit := gitInit(t, root)
	repo.Init(root)

	sm := filepath.Join(root, "submodules", "sm")
	if err := os.MkdirAll(filepath.Join(sm, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Submodule source checkout tracked on "main", plus the agent's implementer
	// commit made in a code worktree branched off it.
	repoDir := filepath.Join(sm, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoGit := gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("base"), 0o644)
	if err := repoGit.Commit(ctx, "base"); err != nil {
		t.Fatalf("submodule base commit: %v", err)
	}

	wtAbs := filepath.Join(sm, "worktrees", "bee-T1")
	if err := repoGit.WorktreeAdd(ctx, wtAbs, "bee-T1", "HEAD"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	wtGit := git.New(wtAbs)
	os.WriteFile(filepath.Join(wtAbs, "f"), []byte("changed"), 0o644)
	if err := wtGit.Commit(ctx, "implement T1"); err != nil {
		t.Fatalf("worktree commit: %v", err)
	}
	implSHA, err := wtGit.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("worktree head: %v", err)
	}

	// PLAN.md flip to NEEDS-REVIEW with NO commits= tag, and the change doc
	// written to disk (real substance) but with NO Beehive-Commits header — and
	// neither committed to the hive branch yet, mirroring an agent that flipped
	// the status and wrote the doc but forgot the mechanical tag/commit step.
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	docPath := filepath.Join(sm, "docs", "bee-T1-T1.md")
	os.WriteFile(docPath, []byte("# T1\n\nreal change doc content.\n"), 0o644)
	hiveGit.Commit(ctx, "seed") // establish HEAD before the (as-yet uncommitted) flip

	rp, err := repo.Open(root)
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	subs, err := rp.Submodules()
	if err != nil {
		t.Fatalf("submodules: %v", err)
	}
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.NeedsReview}}

	r := &Runner{Repo: rp, Git: hiveGit}
	if err := r.autoBookkeep(ctx, sel, wtAbs, root, "bee-T1"); err != nil {
		t.Fatalf("autoBookkeep: %v", err)
	}

	// PLAN.md must now carry a commits= tag naming the real implementer sha.
	pb, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Parse(string(pb))
	if err != nil {
		t.Fatalf("parse PLAN.md: %v", err)
	}
	task := p.Find("T1")
	if task == nil || !task.CommitsSet {
		t.Fatalf("expected a synthesized commits= tag on T1: %+v", task)
	}
	if len(task.Commits) != 1 || task.Commits[0] != implSHA {
		t.Fatalf("expected commits=[%s], got %v", implSHA, task.Commits)
	}

	// The doc's first line must carry the matching Beehive-Commits header, with
	// its original prose left intact beneath it.
	db, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(db)
	wantHeader := "<!-- Beehive-Commits: " + implSHA + " -->"
	if !strings.HasPrefix(doc, wantHeader) {
		t.Fatalf("expected doc to start with %q, got:\n%s", wantHeader, doc)
	}
	if !strings.Contains(doc, "real change doc content.") {
		t.Fatalf("expected original doc prose preserved, got:\n%s", doc)
	}

	// Both PLAN.md and the doc must now be committed to the hive branch HEAD (the
	// gate's own committed-artifact invariants), not merely on disk.
	show, err := hiveGit.Run(ctx, "show", "HEAD:submodules/sm/PLAN.md")
	if err != nil {
		t.Fatalf("PLAN.md not committed: %v", err)
	}
	if !strings.Contains(show, "commits="+implSHA) {
		t.Fatalf("committed PLAN.md missing commits= tag: %s", show)
	}
	if _, err := hiveGit.Run(ctx, "show", "HEAD:submodules/sm/docs/bee-T1-T1.md"); err != nil {
		t.Fatalf("change doc not committed: %v", err)
	}
	if status, _ := hiveGit.Run(ctx, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("expected a clean hive worktree after auto-bookkeeping, got:\n%s", status)
	}
}

// TestAutoBookkeepNoopWhenAlreadyConsistent asserts autoBookkeep makes NO git
// calls (and no commit) when the task already carries a commits= tag that
// agrees with the doc's Beehive-Commits header — it must never touch an
// already-consistent handoff.
func TestAutoBookkeepNoopWhenAlreadyConsistent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hiveGit := gitInit(t, root)
	repo.Init(root)

	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.MkdirAll(filepath.Join(sm, "repo"), 0o755)
	gitInit(t, filepath.Join(sm, "repo"))

	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= commits=none -->\ngo\n"), 0o644)
	os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("<!-- Beehive-Commits: none -->\n\ndoc\n"), 0o644)
	if err := hiveGit.CommitPaths(ctx, "seed", "submodules/sm/PLAN.md", "submodules/sm/docs/bee-T1-T1.md"); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	head, err := hiveGit.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	rp, err := repo.Open(root)
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	subs, err := rp.Submodules()
	if err != nil {
		t.Fatalf("submodules: %v", err)
	}
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.NeedsReview}}

	r := &Runner{Repo: rp, Git: hiveGit}
	if err := r.autoBookkeep(ctx, sel, filepath.Join(sm, "worktrees", "bee-T1"), root, "bee-T1"); err != nil {
		t.Fatalf("autoBookkeep: %v", err)
	}
	newHead, err := hiveGit.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newHead != head {
		t.Fatalf("expected no new commit for an already-consistent handoff; HEAD moved %s -> %s", head, newHead)
	}
}
