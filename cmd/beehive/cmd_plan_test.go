package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
)

func gitDo(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return string(out)
}

const cmdFatPlan = `<!-- Beehive-ROI: deadbeef -->
# Plan

## alpha [DONE] <!-- attempts=0 deps= weight=32 -->
Add Foo. FOUNDATION.
Files: internal/foo/foo.go.
Doc: docs/tasks/alpha.md
Accept: foo works.
Impl (bee-alpha, commit c21a4f0): closed spec gaps; tests green.
Review (approved, beehive-123): verified vs task + ROI. Merged.

## beta [TODO] <!-- attempts=0 deps=alpha session=beehive-999 heartbeat=2026-07-03T00:00:00Z -->
Do beta.
Files: internal/beta/beta.go.
Doc: docs/tasks/beta.md
Accept: beta works.
`

// setupBeehive makes a git repo rooted at a temp dir with AGENTS.md and a leaned-
// candidate PLAN.md under submodules/beehive/, committed, and chdirs into it.
func setupBeehive(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitDo(t, root, "init", "-q")
	gitDo(t, root, "config", "user.email", "t@t")
	gitDo(t, root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatal(err)
	}
	planDir := filepath.Join(root, "submodules", "beehive")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "PLAN.md"), []byte(cmdFatPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDo(t, root, "add", "-A")
	gitDo(t, root, "commit", "-qm", "init")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return root
}

func TestPlanArchiveCommand(t *testing.T) {
	root := setupBeehive(t)
	planPath := filepath.Join(root, "submodules", "beehive", "PLAN.md")

	cmd := planCmd()
	cmd.SetArgs([]string{"archive", "beehive"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// The DONE task's narrative moved to docs/plan-archive/alpha.md.
	arch := filepath.Join(root, "submodules", "beehive", "docs", "plan-archive", "alpha.md")
	ab, err := os.ReadFile(arch)
	if err != nil {
		t.Fatalf("archive file: %v", err)
	}
	if !strings.Contains(string(ab), "Impl (bee-alpha") || !strings.Contains(string(ab), "Review (approved") {
		t.Fatalf("archive file missing narrative:\n%s", ab)
	}

	// PLAN.md is leaned: parses to the SAME task set/statuses/deps/claims, no
	// narrative left on alpha, beta (OPEN, claimed) fully intact.
	lb, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(lb), "Impl (bee-alpha") || strings.Contains(string(lb), "Review (approved") {
		t.Fatalf("PLAN.md still carries archived narrative:\n%s", lb)
	}
	p, err := plan.Parse(string(lb))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tasks) != 2 || p.Task("alpha").Status != plan.Done || p.Task("beta").Status != plan.TODO {
		t.Fatalf("leaned plan task set wrong: %+v", p.Tasks)
	}
	beta := p.Task("beta")
	if beta.Session != "beehive-999" || len(beta.Deps) != 1 || beta.Deps[0] != "alpha" {
		t.Fatalf("beta claim/deps altered: %+v", beta)
	}
	if !strings.Contains(strings.Join(beta.Body, "\n"), "Do beta.") {
		t.Fatalf("beta body altered: %q", beta.Body)
	}

	// The change was published as a commit touching PLAN.md + the archive file.
	logOut := gitOut(t, root, "log", "-1", "--name-only", "--pretty=format:%s")
	if !strings.Contains(logOut, "archive DONE narrative") {
		t.Fatalf("no archive commit: %s", logOut)
	}
	if !strings.Contains(logOut, "PLAN.md") || !strings.Contains(logOut, filepath.Join("plan-archive", "alpha.md")) {
		t.Fatalf("archive commit missing expected paths:\n%s", logOut)
	}

	// Idempotent: re-running archives nothing, makes no new commit, PLAN.md stable.
	headBefore := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	planBefore := mustRead(t, planPath)
	cmd2 := planCmd()
	cmd2.SetArgs([]string{"archive", "beehive"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("second archive: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("idempotent re-run created a commit: %s -> %s", headBefore, got)
	}
	if got := mustRead(t, planPath); got != planBefore {
		t.Fatalf("idempotent re-run changed PLAN.md")
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPlanValidateCommand(t *testing.T) {
	setupBeehive(t)

	// A well-formed PLAN.md validates.
	cmd := planCmd()
	cmd.SetArgs([]string{"validate", "beehive"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate well-formed plan: %v", err)
	}

	// A malformed header (bad heartbeat) fails validation with a non-nil error.
	bad := "<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## alpha [TODO] <!-- attempts=0 deps= heartbeat=not-a-timestamp -->\nBody.\n"
	if err := os.WriteFile(filepath.Join("submodules", "beehive", "PLAN.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = planCmd()
	cmd.SetArgs([]string{"validate", "beehive"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatalf("validate malformed plan: got nil error, want a parse failure")
	}
}

// plan lint fails on a dangling local dep and on a defer-cap breach, warns (does
// not fail) on open tasks missing a DoD declaration by default, and errors on
// them with --strict.
func TestPlanLint(t *testing.T) {
	root, _ := newHive(t)
	// Clean plan: a task with a Check and one with check=none — no issues.
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## a [TODO] <!-- attempts=0 deps= -->\nx\nCheck: true\n\n## b [DONE] <!-- attempts=0 deps= check=none -->\nx\n")
	commitPush(t, root, "seed clean plan")
	inDir(t, root, func() {
		if err := runPlanLint(t, "flux", false); err != nil {
			t.Fatalf("clean plan must lint OK: %v", err)
		}
	})

	// Dangling dep + defer-cap breach → hard errors regardless of --strict.
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## a [TODO] <!-- attempts=0 deps=ghost -->\nx\nCheck: true\n\n## b [TODO] <!-- attempts=0 deps= defers=99 -->\nx\nCheck: true\n")
	commitPush(t, root, "seed broken plan")
	inDir(t, root, func() {
		if err := runPlanLint(t, "flux", false); err == nil {
			t.Fatal("dangling dep + defer-cap breach must fail lint")
		}
	})

	// Open task missing a DoD declaration: warn by default, error with --strict.
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## a [TODO] <!-- attempts=0 deps= -->\nno check declared\n")
	commitPush(t, root, "seed undeclared plan")
	inDir(t, root, func() {
		if err := runPlanLint(t, "flux", false); err != nil {
			t.Fatalf("missing DoD must only WARN by default: %v", err)
		}
		if err := runPlanLint(t, "flux", true); err == nil {
			t.Fatal("missing DoD must ERROR with --strict")
		}
	})
}

func runPlanLint(t *testing.T, sm string, strict bool) error {
	t.Helper()
	c := planLintCmd()
	args := []string{sm}
	if strict {
		args = append(args, "--strict")
	}
	c.SetArgs(args)
	return c.Execute()
}
