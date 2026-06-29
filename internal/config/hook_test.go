package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallROIHook(t *testing.T) {
	root := t.TempDir()
	if err := InstallROIHook(root); err == nil {
		t.Fatal("want error: not a git repo")
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallROIHook(root); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".git", "hooks", "pre-commit")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o100 == 0 {
		t.Fatal("hook not executable")
	}
}

func TestPreCommitHookGuards(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallROIHook(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".git", "hooks", "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"ROI.md", "PLAN.md", "beehive lint", "BEEHIVE_HONEYBEE"} {
		if !strings.Contains(s, want) {
			t.Fatalf("pre-commit hook missing %q:\n%s", want, s)
		}
	}
}

// TestPreCommitDepCycleGuardE2E drives the installed hook through a real commit,
// stubbing the beehive binary on PATH to stand in for `beehive lint`. It proves
// the wiring: a PLAN.md commit is rejected when lint fails (a cycle) and allowed
// when lint passes; the real cycle detection that lint runs is covered in
// internal/select and internal/links.
func TestPreCommitDepCycleGuardE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRun := func(env []string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := gitRun(nil, a...); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	if err := InstallROIHook(root); err != nil {
		t.Fatal(err)
	}

	// Stub beehive on PATH; mode "1" => lint fails (cycle), "0" => lint passes.
	bin := t.TempDir()
	writeStub := func(exit string) {
		sh := "#!/bin/sh\nexit " + exit + "\n"
		if err := os.WriteFile(filepath.Join(bin, "beehive"), []byte(sh), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pathEnv := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")

	if err := os.WriteFile(filepath.Join(root, "PLAN.md"), []byte("## a [TODO] <!-- attempts=0 deps=b -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := gitRun(nil, "add", "PLAN.md"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}

	// lint fails -> commit rejected, nothing recorded.
	writeStub("1")
	if out, err := gitRun([]string{pathEnv}, "commit", "-m", "cycle"); err == nil {
		t.Fatalf("commit must be rejected when lint fails:\n%s", out)
	}
	if _, err := gitRun(nil, "rev-parse", "--verify", "HEAD"); err == nil {
		t.Fatal("a commit landed despite lint failure")
	}

	// lint passes -> commit succeeds.
	writeStub("0")
	if out, err := gitRun([]string{pathEnv}, "commit", "-m", "ok"); err != nil {
		t.Fatalf("commit must succeed when lint passes: %v\n%s", err, out)
	}

	// ROI protection still holds for a honeybee identity.
	if err := os.WriteFile(filepath.Join(root, "ROI.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := gitRun(nil, "add", "ROI.md"); err != nil {
		t.Fatalf("add ROI: %v\n%s", err, out)
	}
	if out, err := gitRun([]string{pathEnv, "BEEHIVE_HONEYBEE=1"}, "commit", "-m", "roi"); err == nil {
		t.Fatalf("honeybee ROI.md commit must be rejected:\n%s", out)
	}
}
