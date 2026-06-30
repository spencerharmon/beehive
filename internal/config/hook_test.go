package config

import (
	"bytes"
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

// TestInstallHooks asserts the reproducible install lays down BOTH hooks at the
// canonical content and mode 0755, and errors (writing nothing) when the target
// is not a git repo.
func TestInstallHooks(t *testing.T) {
	root := t.TempDir()
	if err := InstallHooks(root); err == nil {
		t.Fatal("want error: not a git repo")
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks")); err == nil {
		t.Fatal("hooks dir created despite not-a-git-repo error")
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(root); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"pre-commit":   preCommitHook,
		"post-receive": postReceiveHook,
	} {
		p := filepath.Join(root, ".git", "hooks", name)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode = %o, want 0755", name, fi.Mode().Perm())
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != want {
			t.Fatalf("%s content not canonical:\n--- got ---\n%s\n--- want ---\n%s", name, b, want)
		}
	}
	// The post-receive hook carries the submodule-sync + orphan-skip semantics:
	// it iterates only .gitmodules-declared paths (never a blind update) so an
	// orphan gitlink cannot fatal it.
	for _, want := range []string{
		"-f .gitmodules", "--get-regexp", "submodule update --init --force", "exit 0",
	} {
		if !strings.Contains(postReceiveHook, want) {
			t.Fatalf("post-receive hook missing %q:\n%s", want, postReceiveHook)
		}
	}
}

// TestInstallHooksIdempotent proves a re-install is byte-identical (no
// duplication) and that it UPGRADES a stale, non-executable hand-edited hook to
// the canonical content at 0755.
func TestInstallHooksIdempotent(t *testing.T) {
	root := t.TempDir()
	hooksDir := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed a STALE post-receive: wrong content, non-executable (0644).
	stale := filepath.Join(hooksDir, "post-receive")
	if err := os.WriteFile(stale, []byte("#!/bin/sh\n# stale hand-edited\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallHooks(root); err != nil {
		t.Fatal(err)
	}
	first := map[string][]byte{}
	for _, n := range []string{"pre-commit", "post-receive"} {
		b, err := os.ReadFile(filepath.Join(hooksDir, n))
		if err != nil {
			t.Fatal(err)
		}
		first[n] = b
	}
	if string(first["post-receive"]) != postReceiveHook {
		t.Fatalf("stale post-receive not upgraded to canonical:\n%s", first["post-receive"])
	}
	if fi, _ := os.Stat(stale); fi.Mode().Perm() != 0o755 {
		t.Fatalf("upgraded post-receive mode = %o, want 0755 (stale 0644 must be re-chmod'd)", fi.Mode().Perm())
	}

	// Second run: byte-identical, no duplication.
	if err := InstallHooks(root); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"pre-commit", "post-receive"} {
		b, err := os.ReadFile(filepath.Join(hooksDir, n))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, first[n]) {
			t.Fatalf("%s changed on re-install (not idempotent)", n)
		}
	}
	ents, err := os.ReadDir(hooksDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 {
		t.Fatalf("hooks dir has %d entries, want exactly 2 (no duplication): %v", len(ents), ents)
	}
}

// gitTestEnv returns the process env with the repo-locating GIT_* vars removed so
// a child git resolves the repo from cwd (the post-receive hook is normally
// invoked with GIT_DIR set; this simulates a clean invocation against the test
// repo). GIT_ALLOW_PROTOCOL (set via t.Setenv for the offline submodule clone) is
// preserved.
func gitTestEnv() []string {
	drop := map[string]bool{
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true,
		"GIT_QUARANTINE_PATH": true, "GIT_OBJECT_DIRECTORY": true,
		"GIT_COMMON_DIR": true, "GIT_PREFIX": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if drop[k] {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// TestPostReceiveSkipsOrphanGitlink drives the installed post-receive hook in a
// repo that holds a REAL declared submodule plus an ORPHAN gitlink (a 160000
// entry with no .gitmodules URL, mirroring a leaked honeybee worktree checkout
// under submodules/*/worktrees/*). The hook must sync the real submodule and SKIP
// the orphan, exiting 0 -- whereas a blind `git submodule update --init --force`
// fatals on the orphan, proving the per-.gitmodules-path iteration is load-bearing.
func TestPostReceiveSkipsOrphanGitlink(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_ALLOW_PROTOCOL", "file") // permit the local-path submodule clone offline

	runGit := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitTestEnv()
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	mustGit := func(dir string, args ...string) {
		t.Helper()
		if out, err := runGit(dir, args...); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	// A real local submodule source.
	src := t.TempDir()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		mustGit(src, a...)
	}
	if err := os.WriteFile(filepath.Join(src, "s.txt"), []byte("sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(src, "add", "s.txt")
	mustGit(src, "commit", "-qm", "sub init")

	// Superproject with the real submodule declared in .gitmodules.
	root := t.TempDir()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		mustGit(root, a...)
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(root, "add", "f.txt")
	mustGit(root, "commit", "-qm", "init")
	mustGit(root, "submodule", "add", src, "submodules/real/repo")

	// Inject an ORPHAN gitlink: a 160000 index entry with no .gitmodules URL.
	headSHA, err := runGit(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, headSHA)
	}
	mustGit(root, "update-index", "--add", "--cacheinfo",
		"160000,"+strings.TrimSpace(headSHA)+",submodules/orphan/worktrees/bee-x")
	mustGit(root, "commit", "-qm", "real submodule + orphan gitlink")

	// Install and run the hook exactly as git would (cwd in the work tree, no
	// inherited GIT_DIR). It must exit 0 despite the orphan.
	if err := InstallPostReceiveHook(root); err != nil {
		t.Fatal(err)
	}
	hookCmd := exec.Command(filepath.Join(root, ".git", "hooks", "post-receive"))
	hookCmd.Dir = root
	hookCmd.Env = gitTestEnv()
	if out, err := hookCmd.CombinedOutput(); err != nil {
		t.Fatalf("post-receive must SKIP the orphan gitlink and exit 0, got: %v\n%s", err, out)
	}
	// It synced the real submodule (its tracked file is checked out).
	if _, err := os.Stat(filepath.Join(root, "submodules", "real", "repo", "s.txt")); err != nil {
		t.Fatalf("post-receive did not sync the real submodule: %v", err)
	}

	// Contrast: a blind update (no per-path filter) fatals on the orphan, proving
	// the hook's skip is load-bearing, not cosmetic.
	if out, err := runGit(root, "submodule", "update", "--init", "--force"); err == nil {
		t.Fatalf("expected a blind `submodule update --init --force` to fatal on the orphan gitlink, but it succeeded:\n%s", out)
	}
}
