package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditCommandEndToEnd is the hive-edit-command CLI acceptance: `beehive
// edit` runs the worktree -> write -> publish -> cleanup sequence as one call
// and lands the change on main with no dangling worktree/branch.
func TestEditCommandEndToEnd(t *testing.T) {
	root := setupBeehive(t)
	roiFile := "submodules/beehive/ROI.md"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(roiFile)), []byte("# ROI\n\noriginal intent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDo(t, root, "add", "-A")
	gitDo(t, root, "commit", "-qm", "seed roi")
	gitDo(t, root, "branch", "-M", "main")
	gitDo(t, root, "config", "receive.denyCurrentBranch", "updateInstead")

	cmd := editCmd()
	cmd.SetIn(strings.NewReader("# ROI\n\noriginal intent\nnew operator intent\n"))
	cmd.SetArgs([]string{roiFile, "--message", "operator: add new intent"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("edit: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(roiFile)))
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	if !strings.Contains(string(got), "new operator intent") {
		t.Fatalf("main missing the published edit: %q", got)
	}

	// No dangling worktree/branch.
	out := gitOut(t, root, "worktree", "list", "--porcelain")
	if strings.Contains(out, "edit-cli-") {
		t.Fatalf("dangling worktree left behind:\n%s", out)
	}
	refs := gitOut(t, root, "for-each-ref", "--format=%(refname)", "refs/heads/edit-cli-*")
	if strings.TrimSpace(refs) != "" {
		t.Fatalf("dangling branch left behind: %q", refs)
	}
	entries, derr := os.ReadDir(filepath.Join(root, ".worktrees"))
	if derr == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "edit-cli-") {
				t.Fatalf("dangling worktree dir left behind: %s", e.Name())
			}
		}
	}

	logOut := gitOut(t, root, "log", "-1", "--format=%s", "--", roiFile)
	if strings.TrimSpace(logOut) != "operator: add new intent" {
		t.Fatalf("commit message = %q", logOut)
	}
}

// TestEditCommandRejectsNonEditableFile confirms `beehive edit` refuses to
// touch a file outside the editable coordination-file set (e.g. PLAN.md,
// honeybee-owned).
func TestEditCommandRejectsNonEditableFile(t *testing.T) {
	setupBeehive(t)
	cmd := editCmd()
	cmd.SetIn(strings.NewReader("whatever"))
	cmd.SetArgs([]string{"submodules/beehive/PLAN.md"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want an error editing PLAN.md, got nil")
	}
}
