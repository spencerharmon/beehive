package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
)

// task add files a real new task (with its doc) through the convergence protocol:
// it heals a fork, publishes to origin, the task lands TODO in the target plan,
// and its design doc is written.
func TestTaskAddHealsForkAndPublishes(t *testing.T) {
	root, origin := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md", todoPlan)
	commitPush(t, root, "seed flux plan")
	peer := seedRemoteAhead(t, origin)

	inDir(t, root, func() {
		c := taskAddCmd()
		c.SetArgs([]string{"flux", "base-job",
			"--body", "Land the build-and-publish-image base job.",
			"--doc", "# base-job\nDesign for the base job.\n",
			"--weight", "2"})
		if err := c.Execute(); err != nil {
			t.Fatalf("task add: %v", err)
		}
	})
	assertHealedAndPushed(t, root, origin, peer, "plan: file task base-job in flux")

	b, err := os.ReadFile(filepath.Join(root, "submodules/flux/PLAN.md"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("reparse flux plan: %v", err)
	}
	nt := p.Find("base-job")
	if nt == nil {
		t.Fatal("base-job not filed into flux PLAN.md")
	}
	if nt.Status != plan.StatusTODO || nt.Weight != 2 {
		t.Fatalf("filed task wrong shape: %+v", nt)
	}
	if _, err := os.Stat(filepath.Join(root, "submodules/flux/docs/tasks/base-job.md")); err != nil {
		t.Fatalf("design doc not written: %v", err)
	}
}

// task add requires a design doc (every plan item ships one).
func TestTaskAddRequiresDoc(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md", todoPlan)
	commitPush(t, root, "seed flux plan")
	inDir(t, root, func() {
		c := taskAddCmd()
		c.SetArgs([]string{"flux", "base-job", "--body", "x"})
		if err := c.Execute(); err == nil {
			t.Fatal("task add without a doc must error")
		}
	})
}

// task block links a cross-submodule prerequisite: it registers the authorizing
// link if missing, adds the dep, releases the claim, and publishes.
func TestTaskBlockCrossSubmoduleLinksAndPublishes(t *testing.T) {
	root, origin := newHive(t)
	writeFileMW(t, root, "submodules/gostream/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## image-verify [TODO] <!-- attempts=0 deps= session=s1 heartbeat=2026-07-01T00:00:00Z -->\nverify the image.\n")
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## base-job [TODO] <!-- attempts=0 deps= -->\nland the base job.\n")
	commitPush(t, root, "seed plans")
	peer := seedRemoteAhead(t, origin)

	inDir(t, root, func() {
		c := taskBlockCmd()
		c.SetArgs([]string{"gostream", "image-verify", "--on", "flux:base-job"})
		if err := c.Execute(); err != nil {
			t.Fatalf("task block: %v", err)
		}
	})
	assertHealedAndPushed(t, root, origin, peer, "plan: block image-verify on flux:base-job")

	b, _ := os.ReadFile(filepath.Join(root, "submodules/gostream/PLAN.md"))
	p, _ := plan.Parse(string(b))
	tk := p.Find("image-verify")
	if tk == nil || len(tk.Deps) != 1 || tk.Deps[0] != "flux:base-job" {
		t.Fatalf("dep not added: %+v", tk)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatalf("claim not released on block: %+v", tk)
	}
	// Authorizing link registered on both sides.
	for _, sm := range []string{"gostream", "flux"} {
		lb, err := os.ReadFile(filepath.Join(root, "submodules", sm, "SUBMODULE-LINKS.yaml"))
		if err != nil {
			t.Fatalf("links file for %s: %v", sm, err)
		}
		if !strings.Contains(string(lb), "gostream") || !strings.Contains(string(lb), "flux") {
			t.Fatalf("%s link not registered both ways: %s", sm, lb)
		}
	}
}

// task block rejects a cross-submodule dep whose target task does not exist —
// never link to a dangling id.
func TestTaskBlockRejectsMissingTarget(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/gostream/PLAN.md",
		"## image-verify [TODO] <!-- attempts=0 deps= -->\nverify.\n")
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"## other [TODO] <!-- attempts=0 deps= -->\nx.\n")
	commitPush(t, root, "seed")
	inDir(t, root, func() {
		c := taskBlockCmd()
		c.SetArgs([]string{"gostream", "image-verify", "--on", "flux:does-not-exist"})
		if err := c.Execute(); err == nil {
			t.Fatal("task block on a non-existent dep task must error")
		}
	})
}

// task add --check attaches a machine definition of done that round-trips into
// PLAN.md as a Check: body field.
func TestTaskAddWithCheck(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md", todoPlan)
	commitPush(t, root, "seed flux plan")
	inDir(t, root, func() {
		c := taskAddCmd()
		c.SetArgs([]string{"flux", "dod-job",
			"--body", "Ship the thing.",
			"--doc", "# dod-job\ndesign\n",
			"--check", "curl -sf https://x/health"})
		if err := c.Execute(); err != nil {
			t.Fatalf("task add --check: %v", err)
		}
	})
	p := reparsePlan(t, root, "submodules/flux/PLAN.md")
	nt := p.Find("dod-job")
	if nt == nil || nt.Check() != "curl -sf https://x/health" {
		t.Fatalf("check not attached: %+v", nt)
	}
	if !nt.CheckDeclared() {
		t.Fatal("task must be CheckDeclared")
	}
}

// task add --check-none records a justified absence; --check-none with --check is
// rejected.
func TestTaskAddCheckNone(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md", todoPlan)
	commitPush(t, root, "seed flux plan")
	inDir(t, root, func() {
		c := taskAddCmd()
		c.SetArgs([]string{"flux", "doc-only", "--body", "pure doc edit, no DoD",
			"--doc", "# d\nx\n", "--check-none"})
		if err := c.Execute(); err != nil {
			t.Fatalf("task add --check-none: %v", err)
		}
	})
	p := reparsePlan(t, root, "submodules/flux/PLAN.md")
	if nt := p.Find("doc-only"); nt == nil || !nt.CheckNone {
		t.Fatalf("check=none not recorded: %+v", nt)
	}
	inDir(t, root, func() {
		c := taskAddCmd()
		c.SetArgs([]string{"flux", "contradiction", "--body", "x", "--doc", "# d\nx\n",
			"--check", "true", "--check-none"})
		if err := c.Execute(); err == nil {
			t.Fatal("--check with --check-none must be rejected")
		}
	})
}

// task defer sets not_before in the future, increments the defer counter, and
// releases the claim.
func TestTaskDefer(t *testing.T) {
	root, origin := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## conv [TODO] <!-- attempts=0 deps= session=s1 heartbeat=2026-07-01T00:00:00Z -->\nwait for convergence.\n")
	commitPush(t, root, "seed flux plan")
	peer := seedRemoteAhead(t, origin)
	inDir(t, root, func() {
		c := taskDeferCmd()
		c.SetArgs([]string{"flux", "conv", "--until", "30m", "--reason", "gitops not reconciled yet"})
		if err := c.Execute(); err != nil {
			t.Fatalf("task defer: %v", err)
		}
	})
	assertHealedAndPushed(t, root, origin, peer, "plan: defer conv")
	nt := reparsePlan(t, root, "submodules/flux/PLAN.md").Find("conv")
	if nt == nil || nt.NotBefore.IsZero() || !nt.NotBefore.After(time.Now()) {
		t.Fatalf("not_before not set in the future: %+v", nt)
	}
	if nt.Defers != 1 {
		t.Fatalf("defer counter = %d, want 1", nt.Defers)
	}
	if nt.Session != "" || !nt.Heartbeat.IsZero() {
		t.Fatalf("claim not released: %+v", nt)
	}
}

// task check runs a task's Check: command and reflects its exit status.
func TestTaskCheckRunsCommand(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## ok [TODO] <!-- attempts=0 deps= -->\nx\nCheck: true\n\n## bad [TODO] <!-- attempts=0 deps= -->\nx\nCheck: false\n\n## none [TODO] <!-- attempts=0 deps= check=none -->\nx\n")
	commitPush(t, root, "seed flux plan")
	inDir(t, root, func() {
		if err := runTaskCheck(t, "flux", "ok"); err != nil {
			t.Fatalf("check ok must pass: %v", err)
		}
		if err := runTaskCheck(t, "flux", "bad"); err == nil {
			t.Fatal("check bad must fail")
		}
		if err := runTaskCheck(t, "flux", "none"); err == nil {
			t.Fatal("check on a check=none task must error (nothing to run)")
		}
	})
}

func runTaskCheck(t *testing.T, sm, id string) error {
	t.Helper()
	c := taskCheckCmd()
	c.SetArgs([]string{sm, id})
	return c.Execute()
}

func reparsePlan(t *testing.T, root, rel string) *plan.Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("reparse %s: %v", rel, err)
	}
	return p
}
