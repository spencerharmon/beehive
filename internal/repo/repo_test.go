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

// TestRulesFileConstant pins the beehive-owned per-submodule RULES.md overlay path
// (submodule-rules-md): it is exactly "RULES.md", sits alongside the other layout
// constants, and is distinct from AGENTS.md (the overlay it is additive to).
func TestRulesFileConstant(t *testing.T) {
	if RulesFile != "RULES.md" {
		t.Fatalf("RulesFile = %q, want %q", RulesFile, "RULES.md")
	}
	if RulesFile == AgentsFile {
		t.Fatal("RulesFile must be distinct from AgentsFile (RULES.md is additive to AGENTS.md, not a rename)")
	}
}

// TestOptionalFilesSet pins the declared optional-file set the explorer renders
// discoverable view/edit links from (optional-file-links). It must be exactly the
// five per-submodule optional files, keyed off the shared name constants (not
// stray literals), and must NOT include PLAN.md (honeybee-owned, bootstrap-
// produced, with its own view) so a create link is never offered for it.
func TestOptionalFilesSet(t *testing.T) {
	want := []string{InfraFile, RulesFile, Artifacts, AgentsFile, ROIFile}
	if len(OptionalFiles) != len(want) {
		t.Fatalf("OptionalFiles = %v, want %v", OptionalFiles, want)
	}
	got := map[string]bool{}
	for _, f := range OptionalFiles {
		if f == "" {
			t.Fatal("OptionalFiles contains an empty entry")
		}
		if got[f] {
			t.Fatalf("OptionalFiles has a duplicate entry %q", f)
		}
		got[f] = true
	}
	for _, f := range want {
		if !got[f] {
			t.Errorf("OptionalFiles missing %q", f)
		}
	}
	if got[PlanFile] {
		t.Error("OptionalFiles must NOT include PLAN.md (honeybee-owned, not a discoverable optional file)")
	}
}

// TestRootInstructionFilesSet pins the declared repo-ROOT instruction-file set the
// frontend renders discoverable view/edit/create links from (root-instruction-
// file-links). It must be exactly the four root instruction files keyed off the
// shared name constants, carry the correct per-file Managed flag (AGENTS/HONEYBEE/
// BOOTSTRAP managed, LOCALS site-authored) for instruction-update-drift, and must
// NOT include any per-submodule file (ROI/PLAN/RULES) — those ride OptionalFiles.
func TestRootInstructionFilesSet(t *testing.T) {
	want := map[string]bool{ // file -> managed
		AgentsFile:    true,
		HoneybeeFile:  true,
		BootstrapFile: true,
		LocalsFile:    false,
	}
	if len(RootInstructionFiles) != len(want) {
		t.Fatalf("RootInstructionFiles = %v, want %d members", RootInstructionFiles, len(want))
	}
	seen := map[string]bool{}
	for _, f := range RootInstructionFiles {
		if f.File == "" {
			t.Fatal("RootInstructionFiles contains an empty entry")
		}
		if seen[f.File] {
			t.Fatalf("RootInstructionFiles has a duplicate entry %q", f.File)
		}
		seen[f.File] = true
		mg, ok := want[f.File]
		if !ok {
			t.Errorf("RootInstructionFiles has unexpected member %q", f.File)
			continue
		}
		if f.Managed != mg {
			t.Errorf("RootInstructionFiles[%q].Managed = %v, want %v", f.File, f.Managed, mg)
		}
	}
	for f := range want {
		if !seen[f] {
			t.Errorf("RootInstructionFiles missing %q", f)
		}
	}
	// The site-authored LOCALS.md is the ONLY unmanaged member: it is never
	// shipped/refreshed by the binary, so instruction-update-drift skips it.
	if seen[LocalsFile] && want[LocalsFile] {
		t.Error("LOCALS.md must be site-authored (Managed=false), never managed")
	}
	// Root-vs-submodule guard: these are root files, not the per-submodule
	// optional set (which owns ROI/PLAN/RULES). A root member must not leak in as
	// a per-submodule optional and vice versa for the ownership-sensitive ones.
	for _, f := range []string{ROIFile, PlanFile, RulesFile} {
		if seen[f] {
			t.Errorf("RootInstructionFiles must NOT include per-submodule file %q", f)
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
