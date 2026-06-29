package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/prompts"
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

// TestInitWritesSlimAgents: Init writes the slim repo-local AGENTS.md (a repo
// marker + local rules), NOT the full protocol. The full protocol ships in the
// binary as the runtime system prompt so it never freezes in an init'd repo.
func TestInitWritesSlimAgents(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, AgentsFile))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if got != prompts.RepoAgents {
		t.Fatal("on-disk AGENTS.md should be the slim repo template")
	}
	if got == prompts.Agents {
		t.Fatal("on-disk AGENTS.md must NOT be the full binary protocol")
	}
	if strings.Contains(got, "## Protocol") {
		t.Fatalf("slim AGENTS.md leaked the full protocol:\n%s", got)
	}
	if !strings.Contains(got, "local rules") {
		t.Fatalf("slim AGENTS.md missing local-rules marker:\n%s", got)
	}
}
