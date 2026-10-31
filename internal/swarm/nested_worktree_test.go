package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

func assertNestedWorktreePins(t *testing.T, dir, dep, leaf string) {
	t.Helper()
	for path, pin := range map[string]string{"deps/lib": dep, "deps/lib/leaf": leaf} {
		if got := nestedRecoveryGit(t, filepath.Join(dir, path), "rev-parse", "HEAD"); got != pin {
			t.Errorf("%s HEAD = %s, want pinned %s", path, got, pin)
		}
		want := nestedRecoveryGit(t, filepath.Join(dir, path), "show", pin+":value.go")
		body, err := os.ReadFile(filepath.Join(dir, path, "value.go"))
		if err != nil || string(body) != want+"\n" {
			t.Errorf("%s source = %q, err = %v, want %q", path, body, err, want+"\n")
		}
		if tip := nestedRecoveryGit(t, filepath.Join(dir, path), "rev-parse", "origin/main"); tip == pin {
			t.Errorf("fixture %s origin must be ahead of the required pin", path)
		}
	}
	if status := nestedRecoveryGit(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("prepared worktree is dirty: %s", status)
	}
}

// Direct helper tests also preserve dirty source and untracked base files.
func preserveNestedWorktreeBase(t *testing.T, dir string, dirty bool) func() {
	t.Helper()
	pins := make(map[string]string)
	files := make(map[string]string)
	for _, path := range []string{"deps/lib", "deps/lib/leaf"} {
		pins[path] = nestedRecoveryGit(t, filepath.Join(dir, path), "rev-parse", "HEAD")
		names := []string{"value.go"}
		if dirty {
			names = append(names, "local-only.txt")
		}
		for _, name := range names {
			rel := filepath.Join(path, name)
			if dirty {
				nestedRecoveryWrite(t, dir, rel, "base checkout: "+rel+"\n")
			}
			body, err := os.ReadFile(filepath.Join(dir, rel))
			if err != nil {
				t.Fatal(err)
			}
			files[rel] = string(body)
		}
	}
	return func() {
		t.Helper()
		for path, pin := range pins {
			if got := nestedRecoveryGit(t, filepath.Join(dir, path), "rev-parse", "HEAD"); got != pin {
				t.Errorf("base %s moved from %s to %s", path, pin, got)
			}
		}
		for rel, want := range files {
			body, err := os.ReadFile(filepath.Join(dir, rel))
			if err != nil || string(body) != want {
				t.Errorf("base %s changed: %q, err = %v", rel, body, err)
			}
		}
	}
}

func TestNestedWorktreeHandoff(t *testing.T) {
	for _, kind := range []selectt.Kind{selectt.Work, selectt.Review, selectt.Arbitrate} {
		t.Run(string(kind), func(t *testing.T) {
			f := newNestedRecoveryFixture(t, "branch", "changed")
			base := f.sel.Submodule.RepoDir()
			tracked := nestedRecoveryGit(t, f.origin, "rev-parse", "main")
			// Keep a genuinely unmerged implementation so Review/Arbitrate reach a turn.
			nestedRecoveryGit(t, base, "checkout", "-b", "bee-R1", "origin/bee-R1")
			nestedRecoveryWrite(t, base, "implementation.txt", "pending review\n")
			nestedRecoveryGit(t, base, "add", "implementation.txt")
			nestedRecoveryGit(t, base, "commit", "-qm", "unmerged implementation")
			impl := nestedRecoveryGit(t, base, "rev-parse", "HEAD")
			nestedRecoveryGit(t, base, "push", "origin", "bee-R1")
			nestedRecoveryGit(t, base, "checkout", "--detach", tracked)
			nestedRecoveryGit(t, base, "submodule", "update", "--init", "--recursive")
			f.sel.Kind = kind
			f.sel.Task.Status = plan.StatusTODO
			if kind == selectt.Review {
				f.sel.Task.Status = plan.StatusReview
			} else if kind == selectt.Arbitrate {
				f.sel.Task.Status = plan.StatusArb
			}
			f.sel.Task.ReviewCommit = impl
			f.sel.Task.Commits = []string{impl}
			nestedRecoveryWrite(t, f.sel.Submodule.Path, "PLAN.md", fmt.Sprintf("## R1 [%s] <!-- attempts=2 deps= commits=%s review=%s -->\nreview\nCheck: go test -count=1 ./...\n", f.sel.Task.Status, impl, impl))
			nestedRecoveryWrite(t, f.sel.Submodule.Path, "docs/bee-R1-R1.md", "<!-- Beehive-Commits: "+impl+" -->\n\nPending review.\n")
			nestedRecoveryGit(t, f.root, "add", "submodules/sm")
			nestedRecoveryGit(t, f.root, "commit", "-qm", "seed handoff at tracked pin")
			assertBase := preserveNestedWorktreeBase(t, base, false)
			defer assertBase()
			branch := branchFor(f.sel)
			if kind != selectt.Work {
				branch = reviewBranchFor(f.sel)
			}
			wt := filepath.Join(f.sel.Submodule.WorktreesDir(), branch)
			if _, err := os.Stat(wt); !os.IsNotExist(err) {
				t.Fatalf("handoff worktree must be fresh: %v", err)
			}
			assertTracked := func() {
				t.Helper()
				for _, ref := range []string{"HEAD:submodules/sm/repo", ":submodules/sm/repo"} {
					if got := nestedRecoveryGit(t, f.root, "rev-parse", ref); got != tracked {
						t.Errorf("hive %s = %s, want unchanged tracked pin %s", ref, got, tracked)
					}
				}
				if got := nestedRecoveryGit(t, f.origin, "rev-parse", "main"); got != tracked {
					t.Errorf("origin main moved: %s -> %s", tracked, got)
				}
			}
			sess := &mockSession{onTurn: func(turn int) {
				assertNestedWorktreePins(t, wt, f.dep, f.leaf)
				assertBase()
				assertTracked()
				if got := nestedRecoveryGit(t, wt, "rev-parse", "HEAD"); got != tracked {
					t.Errorf("handoff HEAD = %s, want tracked tip %s", got, tracked)
				}
				if kind != selectt.Work {
					if got := nestedRecoveryGit(t, wt, "rev-parse", "origin/bee-R1"); got != impl {
						t.Errorf("implementer ref = %s, want %s", got, impl)
					}
				}
			}}
			f.r.Client, f.r.MaxTurns = &mockClient{sess: sess}, 1
			res, err := f.r.Run(context.Background(), f.sel, "system", "first")
			if err != nil || sess.prompts != 1 || res.Completed || !res.GCMarked {
				t.Fatalf("Run = %+v, err = %v, turns = %d; want one nonterminal handoff", res, err, sess.prompts)
			}
			assertTracked()
		})
	}
}

func TestNestedWorktreeRepeatedPreparation(t *testing.T) {
	f := newNestedRecoveryFixture(t, "branch", "changed")
	base := f.sel.Submodule.RepoDir()
	oldDep := nestedRecoveryGit(t, base, "rev-parse", "HEAD:deps/lib")
	oldLeaf := nestedRecoveryGit(t, filepath.Join(base, "deps/lib"), "rev-parse", "HEAD:leaf")
	defer preserveNestedWorktreeBase(t, base, true)()
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "reused")
	if err := git.New(base).WorktreeAdd(ctx, wt, "nested-reused", "HEAD"); err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct{ ref, dep, leaf string }{{"HEAD", oldDep, oldLeaf}, {"origin/main", f.dep, f.leaf}} {
		nestedRecoveryGit(t, wt, "checkout", "--detach", state.ref)
		head := nestedRecoveryGit(t, wt, "rev-parse", "HEAD")
		for i := 0; i < 2; i++ {
			if err := git.New(wt).PrepareNestedSubmodules(ctx); err != nil {
				t.Fatal(err)
			}
			assertNestedWorktreePins(t, wt, state.dep, state.leaf)
			if got := nestedRecoveryGit(t, wt, "rev-parse", "HEAD"); got != head {
				t.Errorf("preparation changed source HEAD: %s -> %s", head, got)
			}
		}
	}
}

func TestNestedWorktreeRemoveRecreate(t *testing.T) {
	f := newNestedRecoveryFixture(t, "branch", "changed")
	base := f.sel.Submodule.RepoDir()
	defer preserveNestedWorktreeBase(t, base, true)()
	ctx := context.Background()
	g := git.New(base)
	wt := filepath.Join(t.TempDir(), "recreated")
	for i := 0; i < 2; i++ {
		if err := g.WorktreeAdd(ctx, wt, "nested-recreated", "origin/main"); err != nil {
			t.Fatalf("WorktreeAdd cycle %d: %v", i, err)
		}
		if err := git.New(wt).PrepareNestedSubmodules(ctx); err != nil {
			t.Fatal(err)
		}
		assertNestedWorktreePins(t, wt, f.dep, f.leaf)
		if err := g.WorktreeRemove(ctx, wt); err != nil {
			t.Fatalf("populated nested worktree lifecycle failure in cycle %d: %v", i, err)
		}
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("removed worktree still present: %v", err)
		}
	}
}
