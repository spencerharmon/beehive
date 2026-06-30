package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitOpen(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err != nil {
		t.Fatalf("open: %v", err)
	}
}

func TestInitCreatesGitRepoOnMain(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if out := gitOut(t, root, "rev-parse", "--show-toplevel"); out != root {
		t.Fatalf("git toplevel = %q, want %q", out, root)
	}
	if out := gitOut(t, root, "branch", "--show-current"); out != "main" {
		t.Fatalf("branch = %q, want main", out)
	}
}

func TestInitExistingGitCreatesAndChecksOutMain(t *testing.T) {
	root := t.TempDir()
	gitOut(t, root, "init")
	gitOut(t, root, "config", "user.name", "Beehive Test")
	gitOut(t, root, "config", "user.email", "beehive-test@example.invalid")
	gitOut(t, root, "checkout", "-b", "dev")
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, "add", "seed.txt")
	gitOut(t, root, "commit", "-m", "seed")

	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if out := gitOut(t, root, "show-ref", "--verify", "refs/heads/main"); out == "" {
		t.Fatal("main ref not created")
	}
	if out := gitOut(t, root, "branch", "--show-current"); out != "main" {
		t.Fatalf("branch = %q, want main", out)
	}
}

func TestSubmoduleStates(t *testing.T) {
	root := t.TempDir()
	Init(root)
	dorm := filepath.Join(root, "submodules", "dormant")
	os.MkdirAll(dorm, 0o755)
	boot := filepath.Join(root, "submodules", "boot")
	os.MkdirAll(boot, 0o755)
	os.WriteFile(filepath.Join(boot, ROIFile), []byte("x"), 0o644)
	done := filepath.Join(root, "submodules", "done")
	os.MkdirAll(done, 0o755)
	os.WriteFile(filepath.Join(done, ROIFile), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(done, PlanFile), []byte("<!-- Beehive-ROI: abc123 -->\n"), 0o644)

	r, _ := Open(root)
	subs, _ := r.Submodules()
	if len(subs) != 3 {
		t.Fatalf("want 3 subs, got %d", len(subs))
	}
	m := map[string]Submodule{}
	for _, s := range subs {
		m[s.Name] = s
	}
	if !m["dormant"].Dormant() {
		t.Error("dormant not detected")
	}
	if !m["boot"].NeedsBootstrap() {
		t.Error("bootstrap not detected")
	}
	if s, _ := m["done"].ROIStamp(); s != "abc123" {
		t.Errorf("stamp = %q", s)
	}
}

// TestInitScaffolds: Init creates the submodules/ tree, an empty INFRASTRUCTURE.md,
// and installs the managed instruction files (AGENTS.md/HONEYBEE.md/BOOTSTRAP.md)
// from the binary defaults so a freshly-init'd repo is immediately valid.
func TestInitScaffolds(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(root, "submodules")); err != nil || !fi.IsDir() {
		t.Fatalf("submodules/ not created: %v", err)
	}
	for _, name := range []string{InfraFile, AgentsFile, "HONEYBEE.md", "BOOTSTRAP.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s not created by Init: %v", name, err)
		}
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
