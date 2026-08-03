package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

// execTestCmd is a bare cobra command carrying a background context for driving a
// verb's RunE directly in tests.
func execTestCmd() *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	return c
}

// splitExecArgs separates leading operands from the child command, honoring an
// explicit `--` boundary and preserving child flags after it.
func TestSplitExecArgs(t *testing.T) {
	// hive form: 1 operand, `--` boundary preserves child flags.
	pos, child, err := splitExecArgs([]string{"br", "--", "go", "test", "-v", "./..."}, 1)
	if err != nil || len(pos) != 1 || pos[0] != "br" {
		t.Fatalf("pos: got %v err %v", pos, err)
	}
	if strings.Join(child, " ") != "go test -v ./..." {
		t.Fatalf("child not preserved: %v", child)
	}

	// submodule form: 2 operands before `--`.
	pos, child, err = splitExecArgs([]string{"sm", "br", "--", "make", "-j2"}, 2)
	if err != nil || len(pos) != 2 || pos[0] != "sm" || pos[1] != "br" {
		t.Fatalf("pos: got %v err %v", pos, err)
	}
	if strings.Join(child, " ") != "make -j2" {
		t.Fatalf("child: %v", child)
	}

	// no `--`: first nPos tokens are operands, the rest is the command.
	pos, child, err = splitExecArgs([]string{"br", "ls", "-la"}, 1)
	if err != nil || pos[0] != "br" || strings.Join(child, " ") != "ls -la" {
		t.Fatalf("no-dash: pos %v child %v err %v", pos, child, err)
	}

	// too few operands errors.
	if _, _, err := splitExecArgs([]string{"--", "ls"}, 1); err == nil {
		t.Fatal("missing operand must error")
	}
	// no command errors.
	if _, _, err := splitExecArgs([]string{"br", "--"}, 1); err == nil {
		t.Fatal("missing command must error")
	}
}

// execIn resolves the correct working directory, streams output, and propagates
// the child's exit code (0 on success, nonzero from a failing child).
func TestExecInDirAndExitCode(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "here")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// CWD is dir: `cat here` succeeds and streams the file content.
	var out bytes.Buffer
	code, err := execIn(context.Background(), dir, []string{"cat", "here"}, nil, &out, &out)
	if err != nil {
		t.Fatalf("execIn: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code: got %d want 0", code)
	}
	if strings.TrimSpace(out.String()) != "x" {
		t.Fatalf("wrong dir/output: %q", out.String())
	}

	// A failing child propagates its nonzero exit code (not os.Exit here).
	code, err = execIn(context.Background(), dir, []string{"sh", "-c", "exit 7"}, nil, &out, &out)
	if err != nil {
		t.Fatalf("execIn failing child errored instead of returning code: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code: got %d want 7", code)
	}
}

// resolveExecWorktree accepts an existing dir and rejects a missing one with the
// corrective `worktree add` in the message.
func TestResolveExecWorktree(t *testing.T) {
	root, _ := newHive(t)

	// hive worktree present.
	hiveWT := filepath.Join(root, ".worktrees", "bee-x")
	if err := os.MkdirAll(hiveWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := resolveExecWorktree(hiveWT, "beehive worktree add bee-x"); err != nil {
		t.Fatalf("existing worktree rejected: %v", err)
	}

	// missing hive worktree: actionable error names `beehive worktree add`.
	missing := filepath.Join(root, ".worktrees", "bee-missing")
	err := resolveExecWorktree(missing, "beehive worktree add bee-missing")
	if err == nil || !strings.Contains(err.Error(), "beehive worktree add bee-missing") {
		t.Fatalf("missing worktree must error with corrective add: %v", err)
	}

	// submodule worktree present.
	smWT := repo.SubmoduleWorktreePath(root, "flux", "bee-a")
	if err := os.MkdirAll(smWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := resolveExecWorktree(smWT, "beehive submodule worktree add flux bee-a"); err != nil {
		t.Fatalf("existing submodule worktree rejected: %v", err)
	}

	// missing submodule worktree: actionable error names the submodule add.
	smMissing := repo.SubmoduleWorktreePath(root, "flux", "bee-none")
	err = resolveExecWorktree(smMissing, "beehive submodule worktree add flux bee-none")
	if err == nil || !strings.Contains(err.Error(), "beehive submodule worktree add flux bee-none") {
		t.Fatalf("missing submodule worktree must error with corrective add: %v", err)
	}
}

// End-to-end through the cobra commands: hive + submodule variants resolve the
// correct dir by absolute path regardless of caller cwd.
func TestWorktreeExecCmdResolvesDir(t *testing.T) {
	root, _ := newHive(t)

	hiveWT := filepath.Join(root, ".worktrees", "bee-h")
	if err := os.MkdirAll(hiveWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hiveWT, "tag"), []byte("hive"), 0o644); err != nil {
		t.Fatal(err)
	}
	smWT := repo.SubmoduleWorktreePath(root, "flux", "bee-s")
	if err := os.MkdirAll(smWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(smWT, "tag"), []byte("sub"), 0o644); err != nil {
		t.Fatal(err)
	}

	// From an unrelated cwd, the commands still resolve by absolute path.
	inDir(t, root, func() {
		if err := worktreeExecCmd().RunE(execTestCmd(), []string{"bee-h", "--", "test", "-f", "tag"}); err != nil {
			t.Fatalf("hive exec resolved wrong dir: %v", err)
		}
		if err := submoduleWorktreeExecCmd().RunE(execTestCmd(), []string{"flux", "bee-s", "--", "test", "-f", "tag"}); err != nil {
			t.Fatalf("submodule exec resolved wrong dir: %v", err)
		}
		// Missing worktree errors with the corrective add rather than running.
		err := worktreeExecCmd().RunE(execTestCmd(), []string{"bee-none", "--", "true"})
		if err == nil || !strings.Contains(err.Error(), "beehive worktree add bee-none") {
			t.Fatalf("missing hive worktree: %v", err)
		}
	})
}
