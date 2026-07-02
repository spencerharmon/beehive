package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return string(out)
}

const archiveCmdPlan = `<!-- Beehive-ROI: abc123 -->
# Plan

## done-fat [DONE] <!-- attempts=0 deps= weight=32 -->
Add Fetch, Pull, Push to git.go.
Files: internal/git/git.go
Doc: docs/tasks/done-fat.md
Accept: ctx-aware wrappers with tests.
Impl (bee-done-fat, commit c21a4f0, pushed origin): Fetch/Push pre-existed; added --prune
and a new Pull running pull --ff-only. Tests against a temp bare origin; go test green.
Review (approved, beehive-1): verified vs task + ROI; re-ran under CGO_ENABLED=0, vet clean,
static build OK. MERGED fast-forward; pointer bumped. No dependents to unlock.

## open-claimed [TODO] <!-- attempts=0 deps= weight=128 session=bee-live heartbeat=2026-06-29T10:00:00Z -->
An in-flight task under an active claim; must not be touched.
Files: internal/swarm/swarm.go
Doc: docs/tasks/open-claimed.md
Accept: measurable.
`

// setupArchiveRepo makes a throwaway beehive repo (AGENTS.md + one submodule with
// a fat PLAN.md) as a git repo with an initial commit, and returns its root.
func setupArchiveRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# agents\n")
	smDir := filepath.Join(root, "submodules", "beehive")
	if err := os.MkdirAll(smDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(smDir, "PLAN.md"), archiveCmdPlan)
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.email", "t@t")
	runGit(t, root, "config", "user.name", "t")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-qm", "init")
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// chdir switches to dir for the duration of the test (go1.22-compatible; no t.Chdir).
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func TestPlanArchiveVerb(t *testing.T) {
	root := setupArchiveRepo(t)
	chdir(t, root)

	planPath := filepath.Join(root, "submodules", "beehive", "PLAN.md")
	before, _ := os.ReadFile(planPath)

	cmd := planCmd()
	cmd.SetArgs([]string{"archive"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// PLAN.md leaned: narrative gone, card + pointer retained, claim preserved.
	after, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) >= len(before) {
		t.Fatalf("PLAN.md did not shrink: %d -> %d", len(before), len(after))
	}
	as := string(after)
	if strings.Contains(as, "Impl (bee-done-fat") || strings.Contains(as, "Review (approved") {
		t.Fatalf("narrative still inline:\n%s", as)
	}
	for _, keep := range []string{
		"Files: internal/git/git.go",
		"Accept: ctx-aware wrappers with tests.",
		"Archived-details: docs/plan-archive/done-fat.md",
		// OPEN task and its claim metadata untouched.
		"session=bee-live heartbeat=2026-06-29T10:00:00Z",
		"An in-flight task under an active claim; must not be touched.",
	} {
		if !strings.Contains(as, keep) {
			t.Fatalf("leaned PLAN.md missing %q:\n%s", keep, as)
		}
	}

	// Archive doc written with the narrative.
	arch, err := os.ReadFile(filepath.Join(root, "submodules", "beehive", "docs", "plan-archive", "done-fat.md"))
	if err != nil {
		t.Fatalf("archive doc not written: %v", err)
	}
	if !strings.Contains(string(arch), "Impl (bee-done-fat") || !strings.Contains(string(arch), "MERGED fast-forward") {
		t.Fatalf("archive doc missing narrative:\n%s", arch)
	}

	// A scoped commit was made, and the tree is clean for those paths.
	if subj := runGit(t, root, "log", "-1", "--format=%s"); !strings.Contains(subj, "plan: archive 1 DONE task narrative(s)") {
		t.Fatalf("commit subject = %q", subj)
	}
	if st := runGit(t, root, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Fatalf("worktree not clean after archive:\n%s", st)
	}

	// Idempotent: re-run archives nothing and makes no new commit.
	countBefore := strings.Count(runGit(t, root, "log", "--oneline"), "\n")
	cmd2 := planCmd()
	cmd2.SetArgs([]string{"archive"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("second archive: %v", err)
	}
	countAfter := strings.Count(runGit(t, root, "log", "--oneline"), "\n")
	if countAfter != countBefore {
		t.Fatalf("idempotent re-run made a commit: %d -> %d log lines", countBefore, countAfter)
	}
	reAfter, _ := os.ReadFile(planPath)
	if string(reAfter) != as {
		t.Fatal("idempotent re-run changed PLAN.md bytes")
	}
}
