package submod

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/links"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
}

// srcRepo makes a throwaway git repo with one commit on branch main and returns
// its path, usable as a (file-protocol) submodule url for an offline clone.
func srcRepo(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, p, "init", "-q", "-b", "main")
	runGit(t, p, "config", "user.email", "t@t")
	runGit(t, p, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(p, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, p, "add", "-A")
	runGit(t, p, "commit", "-qm", "init")
	return p
}

// superRepo makes an empty git superproject (a HEAD commit is not required for
// `git submodule add`).
func superRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "t@t")
	runGit(t, root, "config", "user.name", "t")
	return root
}

func TestAddCreatesTrackedSubmodule(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file") // permit the local-path clone offline
	root := superRepo(t)
	src := srcRepo(t, "myrepo")

	name, err := Add(context.Background(), root, src, "", "") // name+branch defaulted
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if name != "myrepo" {
		t.Fatalf("name = %q, want myrepo", name)
	}
	gm, err := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if err != nil {
		t.Fatalf("no .gitmodules (submodule not tracked): %v", err)
	}
	if !strings.Contains(string(gm), "submodules/myrepo/repo") {
		t.Fatalf(".gitmodules missing entry:\n%s", gm)
	}
	if !strings.Contains(string(gm), "branch = main") {
		t.Fatalf(".gitmodules missing default branch:\n%s", gm)
	}
	if _, err := os.Stat(filepath.Join(root, "submodules", "myrepo", "repo", "f.txt")); err != nil {
		t.Fatalf("submodule not checked out: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, "submodules", "myrepo", "worktrees")); err != nil || !fi.IsDir() {
		t.Fatalf("worktrees dir missing: %v", err)
	}
}

func TestAddDerivesNameStrippingGitSuffix(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	root := superRepo(t)
	src := srcRepo(t, "beta.git")

	name, err := Add(context.Background(), root, src, "", "main")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if name != "beta" {
		t.Fatalf("name = %q, want beta", name)
	}
	if _, err := os.Stat(filepath.Join(root, "submodules", "beta", "repo", "f.txt")); err != nil {
		t.Fatalf("submodule not checked out at derived name: %v", err)
	}
}

func TestAddRejectsBadNameAndURL(t *testing.T) {
	root := superRepo(t)
	// Validation happens before any git call, so these need no network.
	if _, err := Add(context.Background(), root, "", "", "main"); !errors.Is(err, ErrURLRequired) {
		t.Fatalf("empty url: got %v, want ErrURLRequired", err)
	}
	for _, bad := range []string{"../evil", "a/b", ".", ".."} {
		if _, err := Add(context.Background(), root, "git@h:o/r.git", bad, "main"); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("name %q: got %v, want ErrInvalidName", bad, err)
		}
	}
}

func TestAddRejectsDuplicate(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	root := superRepo(t)
	src := srcRepo(t, "dup")
	if _, err := Add(context.Background(), root, src, "", "main"); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if _, err := Add(context.Background(), root, src, "", "main"); !errors.Is(err, ErrExists) {
		t.Fatalf("second Add: got %v, want ErrExists", err)
	}
}

func TestLinkSubmodulesWritesBothFiles(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(root, "submodules", n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := LinkSubmodules(root, "a", "b"); err != nil {
		t.Fatalf("LinkSubmodules: %v", err)
	}
	for _, n := range []string{"a", "b"} {
		l, err := links.Load(filepath.Join(root, "submodules", n, "SUBMODULE-LINKS.yaml"))
		if err != nil {
			t.Fatalf("load %s (schema-invalid YAML?): %v", n, err)
		}
		if len(l.Submodules) != 2 || l.Submodules[0] != "a" || l.Submodules[1] != "b" {
			t.Fatalf("%s submodules = %v, want [a b]", n, l.Submodules)
		}
	}
	// idempotent
	if err := LinkSubmodules(root, "a", "b"); err != nil {
		t.Fatalf("repeat LinkSubmodules: %v", err)
	}
	l, _ := links.Load(filepath.Join(root, "submodules", "a", "SUBMODULE-LINKS.yaml"))
	if len(l.Submodules) != 2 {
		t.Fatalf("not idempotent: %v", l.Submodules)
	}
	// self / empty rejected
	if err := LinkSubmodules(root, "a", "a"); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("self link: got %v, want ErrInvalidLink", err)
	}
	if err := LinkSubmodules(root, "", "b"); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("empty link: got %v, want ErrInvalidLink", err)
	}
}

func TestAddDepValidYAMLAndCycleGuard(t *testing.T) {
	root := t.TempDir()
	if err := AddDep(root, "a", "b"); err != nil {
		t.Fatalf("a->b: %v", err)
	}
	if err := AddDep(root, "b", "c"); err != nil {
		t.Fatalf("b->c: %v", err)
	}
	// c->a would close a->b->c->a: rejected, nothing written.
	if err := AddDep(root, "c", "a"); !errors.Is(err, ErrCycle) {
		t.Fatalf("cycle c->a: got %v, want ErrCycle", err)
	}
	l, err := links.Load(filepath.Join(root, "SUBMODULE-LINKS.yaml"))
	if err != nil {
		t.Fatalf("links file not schema-valid: %v", err)
	}
	if l.HasCycle() {
		t.Fatal("persisted dependency graph has a cycle")
	}
	if len(l.Deps) != 2 {
		t.Fatalf("deps = %v, want exactly the two acyclic edges", l.Deps)
	}
	// self-dependency and empty input rejected
	if err := AddDep(root, "x", "x"); !errors.Is(err, ErrCycle) {
		t.Fatalf("self dep: got %v, want ErrCycle", err)
	}
	if err := AddDep(root, "", "y"); !errors.Is(err, ErrInvalidDep) {
		t.Fatalf("empty dep: got %v, want ErrInvalidDep", err)
	}
}
