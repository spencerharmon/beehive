package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
)

// Commit the topology change elsewhere, then merge without deinitializing the
// old checkout, just as an agent can do before the runner's handoff gate.
func nestedPremergedFixture(t *testing.T, rename bool) *nestedCheckFixture {
	t.Helper()
	f := newNestedCheckFixture(t, false, false)
	peer := filepath.Join(t.TempDir(), "topology")
	nestedCheckGit(t, f.wt, "clone", "-q", "-b", "bee-R1", f.origin, peer)
	gitConfig(t, peer)
	nestedCheckGit(t, peer, "rm", "--cached", "nested")
	if rename {
		nestedCheckGit(t, peer, "update-index", "--add", "--cacheinfo", "160000,"+f.pin+",relocated")
		nestedCheckGit(t, peer, "config", "-f", ".gitmodules", "submodule.nested.path", "relocated")
	} else {
		nestedCheckGit(t, peer, "config", "-f", ".gitmodules", "--remove-section", "submodule.nested")
	}
	nestedCheckGit(t, peer, "add", ".gitmodules")
	nestedCheckGit(t, peer, "commit", "-qm", "change top-level topology")
	nestedCheckGit(t, peer, "push", "origin", "HEAD:bee-R1")
	nestedCheckGit(t, f.wt, "fetch", "origin", "bee-R1")
	nestedCheckGit(t, f.wt, "-c", "submodule.recurse=false", "merge", "--no-ff", "--no-edit", "origin/bee-R1")
	if _, err := os.Stat(filepath.Join(f.wt, "nested", ".git")); err != nil {
		t.Fatalf("merge did not leave the old initialized checkout: %v", err)
	}
	return f
}

func TestNestedPremergedTopologyCleanup(t *testing.T) {
	for _, rename := range []bool{false, true} {
		t.Run(fmt.Sprintf("rename=%t", rename), func(t *testing.T) {
			f := nestedPremergedFixture(t, rename)
			f.registerChecks(t)
			other := f.sel.Submodule.RepoDir()
			commands := [][]string{{"rev-parse", "HEAD"}, {"submodule", "status", "--recursive"}, {"status", "--porcelain", "--ignored"}, {"config", "--local", "--list"}}
			before := make([]string, len(commands))
			for i, args := range commands {
				before[i] = nestedCheckGit(t, other, args...)
			}
			if hint, err := f.r.verifyGate(context.Background(), f.sel, f.wt, f.root, "bee-R1"); err != nil || hint != "" {
				t.Fatalf("premerged topology must pass cleanliness: hint=%q err=%v", hint, err)
			}
			if _, err := os.Lstat(filepath.Join(f.wt, "nested")); !os.IsNotExist(err) {
				t.Errorf("obsolete checkout retained: %v", err)
			}
			check := fmt.Sprintf("touch %q; cmp expected.txt expected.txt", f.marker)
			if rename {
				check = fmt.Sprintf("touch %q; cmp expected.txt relocated/value.txt && cmp expected.txt relocated/child/value.txt", f.marker)
				for path, pin := range map[string]string{"relocated": f.pin, "relocated/child": f.childPin} {
					if got := nestedCheckGit(t, filepath.Join(f.wt, path), "rev-parse", "HEAD"); got != pin {
						t.Errorf("%s HEAD = %s, want exact pin %s", path, got, pin)
					}
				}
			}
			if outcome, err := f.r.runCheckIn(context.Background(), f.sel, check, f.wt); err != nil || outcome.exitErr {
				t.Fatalf("prepared topology check: %+v %v", outcome, err)
			}
			for i, args := range commands {
				if got := nestedCheckGit(t, other, args...); got != before[i] {
					t.Errorf("other source git %v changed: %q -> %q", args, before[i], got)
				}
			}
		})
	}
}

func TestNestedPremergedPreservesAmbiguousState(t *testing.T) {
	for _, edit := range []string{
		"tracked", "staged", "untracked", "ignored", "child-tracked", "child-staged", "child-untracked", "child-ignored",
		"head-drift", "child-head-drift", "child-staged-pin", "parent-tracked", "parent-staged", "parent-staged-pin",
		"embedded-git", "child-embedded-git", "foreign-gitfile", "unrelated-repository", "unexpected-git",
		"assume-unchanged", "skip-worktree", "uninitialized-child", "symlink-gitfile", "symlink-checkout",
	} {
		t.Run(edit, func(t *testing.T) {
			f := nestedPremergedFixture(t, true)
			old := filepath.Join(f.wt, "nested")
			child := filepath.Join(old, "child")
			dir, name := old, "value.txt"
			switch edit {
			case "child-tracked", "child-staged", "child-untracked", "child-ignored":
				dir = child
			case "parent-tracked", "parent-staged":
				dir, name = f.wt, "expected.txt"
			}
			var preserved string
			switch edit {
			case "head-drift":
				nestedCheckGit(t, old, "checkout", "-q", f.pin)
			case "child-head-drift":
				nestedCheckGit(t, child, "checkout", "-q", f.childPin)
			case "child-staged-pin":
				nestedCheckGit(t, old, "update-index", "--cacheinfo", "160000,"+f.childPin+",child")
			case "parent-staged-pin":
				pin := nestedCheckGit(t, old, "rev-parse", "HEAD")
				nestedCheckGit(t, f.wt, "update-index", "--cacheinfo", "160000,"+pin+",relocated")
			case "assume-unchanged", "skip-worktree":
				nestedCheckGit(t, old, "update-index", "--"+edit, "value.txt")
				preserved = filepath.Join(old, "value.txt")
				nestedCheckWrite(t, preserved, "hidden authored content\n")
			case "uninitialized-child":
				nestedCheckGit(t, old, "submodule", "deinit", "--all")
			case "unexpected-git":
				path := filepath.Join(old, "metadata", ".git")
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				preserved = filepath.Join(path, "authored")
				nestedCheckWrite(t, preserved, "authored metadata\n")
			case "symlink-gitfile", "symlink-checkout":
				path := old
				if edit == "symlink-gitfile" {
					path = filepath.Join(old, ".git")
				}
				destination := filepath.Join(t.TempDir(), "moved")
				if err := os.Rename(path, destination); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(destination, path); err != nil {
					t.Fatal(err)
				}
			case "embedded-git", "child-embedded-git":
				if edit == "child-embedded-git" {
					dir = child
				}
				gitdir := nestedCheckGit(t, dir, "rev-parse", "--absolute-git-dir")
				if err := os.Remove(filepath.Join(dir, ".git")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(gitdir, filepath.Join(dir, ".git")); err != nil {
					t.Fatal(err)
				}
			case "foreign-gitfile":
				other := filepath.Join(f.sel.Submodule.RepoDir(), "nested")
				gitdir := nestedCheckGit(t, other, "rev-parse", "--absolute-git-dir")
				preserved = filepath.Join(old, ".git")
				nestedCheckWrite(t, preserved, "gitdir: "+gitdir+"\n")
			case "unrelated-repository":
				preserved = filepath.Join(f.wt, "notes", ".git", "HEAD")
				nestedCheckGit(t, f.wt, "init", "-q", filepath.Join(f.wt, "notes"))
			default:
				if edit == "untracked" || edit == "ignored" || edit == "child-untracked" || edit == "child-ignored" {
					name = "agent-notes"
				}
				preserved = filepath.Join(dir, name)
				nestedCheckWrite(t, preserved, "authored content\n")
				if edit == "staged" || edit == "child-staged" || edit == "parent-staged" {
					nestedCheckGit(t, dir, "add", name)
				}
				if edit == "ignored" || edit == "child-ignored" {
					gitdir := nestedCheckGit(t, dir, "rev-parse", "--absolute-git-dir")
					nestedCheckWrite(t, filepath.Join(gitdir, "info", "exclude"), "agent-notes\n")
				}
			}
			var content []byte
			if preserved != "" {
				var err error
				content, err = os.ReadFile(preserved)
				if err != nil {
					t.Fatal(err)
				}
			}
			index := nestedCheckGit(t, f.wt, "ls-files", "--stage")
			prepErr := git.New(f.wt).PrepareCleanNestedCheckout(context.Background())
			if hint, err := f.r.gateCleanCheckout(context.Background(), f.wt); prepErr == nil && err == nil && hint == "" {
				t.Fatal("ambiguous or authored state passed the cleanliness gate")
			}
			if edit != "unrelated-repository" {
				if _, err := os.Lstat(filepath.Join(old, ".git")); err != nil {
					t.Errorf("unsafe cleanup removed old checkout: %v", err)
				}
			}
			if preserved != "" {
				if got, err := os.ReadFile(preserved); err != nil || string(got) != string(content) {
					t.Errorf("authored file changed: %q %v", got, err)
				}
			}
			if got := nestedCheckGit(t, f.wt, "ls-files", "--stage"); got != index {
				t.Errorf("parent index changed: %q -> %q", index, got)
			}
		})
	}
}
