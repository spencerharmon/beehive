package swarm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
)

func TestNestedChangedURL(t *testing.T) {
	f := newNestedRecoveryFixture(t, "branch", "changed")
	cwd := f.cwd
	dep := filepath.Join(cwd, "deps/lib")
	leaf := filepath.Join(dep, "leaf")
	newOrigin := filepath.Join(t.TempDir(), "leaf.git")
	nestedRecoveryGit(t, cwd, "clone", "--bare", leaf, newOrigin)
	gitConfig(t, leaf)
	nestedRecoveryWrite(t, leaf, "value.go", "package leaf\nconst Value = 42\n")
	nestedRecoveryGit(t, leaf, "add", ".")
	nestedRecoveryGit(t, leaf, "commit", "-qm", "new origin only")
	leafPin := nestedRecoveryGit(t, leaf, "rev-parse", "HEAD")
	nestedRecoveryGit(t, leaf, "push", newOrigin, "HEAD:refs/heads/new")
	gitConfig(t, dep)
	nestedRecoveryGit(t, dep, "config", "-f", ".gitmodules", "submodule.leaf.url", newOrigin)
	nestedRecoveryGit(t, dep, "add", ".")
	nestedRecoveryGit(t, dep, "commit", "-qm", "move leaf origin")
	depPin := nestedRecoveryGit(t, dep, "rev-parse", "HEAD")
	nestedRecoveryGit(t, dep, "push", "origin", "HEAD:refs/heads/new")
	wt := filepath.Join(t.TempDir(), "reused")
	if err := git.New(cwd).WorktreeAdd(context.Background(), wt, "url-reuse", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := git.New(wt).PrepareNestedSubmodules(context.Background()); err != nil {
		t.Fatal(err)
	}
	nestedRecoveryGit(t, wt, "update-index", "--cacheinfo", "160000,"+depPin+",deps/lib")
	if err := git.New(wt).PrepareNestedSubmodules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := nestedRecoveryGit(t, filepath.Join(wt, "deps/lib/leaf"), "rev-parse", "HEAD"); got != leafPin {
		t.Fatalf("leaf = %s, want %s", got, leafPin)
	}
}

func TestNestedActiveAndMissingModules(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "inactive", true: "missing-modules"}[missing], func(t *testing.T) {
			f := newNestedRecoveryFixture(t, "branch", "introduced")
			nestedRecoveryGit(t, f.cwd, "checkout", "--detach", "origin/main")
			if missing {
				nestedRecoveryGit(t, f.cwd, "rm", ".gitmodules")
			} else {
				nestedRecoveryGit(t, f.cwd, "config", "submodule.active", "nonexistent")
				nestedRecoveryGit(t, f.cwd, "config", "submodule.deps/lib.active", "false")
			}
			err := git.New(f.cwd).PrepareNestedSubmodules(context.Background())
			if missing {
				if err == nil {
					t.Fatal("missing .gitmodules with gitlinks must fail preparation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertNestedWorktreePins(t, f.cwd, f.dep, f.leaf)
			}
		})
	}
}
