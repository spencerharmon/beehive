package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

// `beehive worktree exec <branch> -- <cmd>...` and
// `beehive submodule worktree exec <submodule> <branch> -- <cmd>...` are the
// sanctioned way to run a NON-git, worktree-scoped command (a branch's build,
// test, or script) against one of the two worktrees a honeybee juggles — the
// HIVE/superrepo worktree (.worktrees/<branch>/) and a per-task SUBMODULE code
// worktree (.submodule-worktrees/<submodule>/<branch>/) — WITHOUT ever `cd`-ing
// into it or guessing a relative path. They are the non-git analogues of
// `beehive git` / `beehive submodule git`: each resolves the worktree by ABSOLUTE
// path (independent of the caller's cwd), streams the child's stdout/stderr,
// propagates the child's exit code, and preserves `--` so every flag after it
// belongs to the child command, never to beehive. A nonexistent worktree is
// refused with an actionable error naming the exact `beehive [submodule] worktree
// add` that would create it, rather than running the command in the wrong place.

// splitExecArgs separates the leading positional operands (branch, or submodule +
// branch) from the child command. A literal `--` is the boundary: everything
// after it is the child command verbatim (so a child flag is never eaten by
// beehive). With no `--`, the first nPos tokens are the operands and the rest is
// the child command. Returns the operands, the child argv, and an error when
// there are too few operands or no command.
func splitExecArgs(args []string, nPos int) (pos, child []string, err error) {
	dash := -1
	for i, a := range args {
		if a == "--" {
			dash = i
			break
		}
	}
	if dash >= 0 {
		pos = args[:dash]
		child = args[dash+1:]
	} else {
		if len(args) < nPos {
			pos = args
		} else {
			pos = args[:nPos]
			child = args[nPos:]
		}
	}
	if len(pos) < nPos {
		return nil, nil, fmt.Errorf("need %d operand(s) before the command", nPos)
	}
	if len(child) == 0 {
		return nil, nil, fmt.Errorf("no command given after `--`")
	}
	return pos, child, nil
}

// resolveExecWorktree confirms dir is an existing worktree directory, else
// returns an actionable error naming the exact `beehive [submodule] worktree add`
// that would create it.
func resolveExecWorktree(dir, addHint string) error {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("worktree %s does not exist — create it first with: %s", dir, addHint)
	}
	return nil
}

// execIn runs argv with its working directory set to dir, wiring the given
// stdio through. It returns the child's exit code (0 on success) WITHOUT calling
// os.Exit, so callers (and tests) decide what to do; the cobra wrapper turns a
// nonzero code into the process exit status. A failure to start the child (not a
// nonzero exit) is returned as an error, never swallowed.
func execIn(ctx context.Context, dir string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("no command given")
	}
	ex := newExecCmd(ctx, argv)
	ex.Dir = dir
	ex.Stdin, ex.Stdout, ex.Stderr = stdin, stdout, stderr
	if err := ex.Run(); err != nil {
		if code, ok := exitCode(err); ok {
			return code, nil
		}
		return 0, fmt.Errorf("exec %v in %s: %w", argv, dir, err)
	}
	return 0, nil
}

// runExecInCobra is the shared RunE body: resolve the worktree, run the command,
// and propagate a nonzero child exit code as the process exit status (mirroring
// runGitIn's pass-through contract).
func runExecInCobra(cmd *cobra.Command, dir, addHint string, child []string) error {
	if err := resolveExecWorktree(dir, addHint); err != nil {
		return err
	}
	code, err := execIn(cmd.Context(), dir, child, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// newExecCmd builds the child process command.
func newExecCmd(ctx context.Context, argv []string) *exec.Cmd {
	return exec.CommandContext(ctx, argv[0], argv[1:]...)
}

// exitCode returns the child's exit status when err is a process exit failure.
func exitCode(err error) (int, bool) {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), true
	}
	return 0, false
}

// worktreeExecCmd is `beehive worktree exec <branch> -- <cmd>...` →
// runs <cmd> with CWD = <root>/.worktrees/<branch>/.
func worktreeExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "exec <branch> -- <cmd> [args...]",
		Short:              "run a non-git command in the hive worktree .worktrees/<branch>/",
		Long:               "Run a NON-git, worktree-scoped command (a branch's build/test/script) in the HIVE (superrepo) worktree at .worktrees/<branch>/. Resolves the worktree by absolute path from the enclosing beehive repo root, so the caller's cwd is irrelevant. Streams stdout/stderr, propagates the child's exit code, and preserves `--` (everything after it is the child command). Refuses a nonexistent worktree, naming the `beehive worktree add <branch>` to create it. Non-git analogue of `beehive git`.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			pos, child, err := splitExecArgs(args, 1)
			if err != nil {
				return err
			}
			root, err := findRoot()
			if err != nil {
				return err
			}
			branch := pos[0]
			dir := filepath.Join(root, ".worktrees", branch)
			return runExecInCobra(cmd, dir, "beehive worktree add "+branch, child)
		},
	}
}

// submoduleWorktreeExecCmd is
// `beehive submodule worktree exec <submodule> <branch> -- <cmd>...` →
// runs <cmd> with CWD = <root>/.submodule-worktrees/<submodule>/<branch>/.
func submoduleWorktreeExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "exec <submodule> <branch> -- <cmd> [args...]",
		Short:              "run a non-git command in a submodule code worktree .submodule-worktrees/<submodule>/<branch>/",
		Long:               "Run a NON-git, worktree-scoped command (a branch's build/test/script) in a per-task SUBMODULE code worktree at .submodule-worktrees/<submodule>/<branch>/. Resolves the worktree by absolute path from the enclosing beehive repo root, so the caller's cwd is irrelevant. Streams stdout/stderr, propagates the child's exit code, and preserves `--` (everything after it is the child command). Refuses a nonexistent worktree, naming the `beehive submodule worktree add <submodule> <branch>` to create it. Non-git analogue of `beehive submodule git`.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			pos, child, err := splitExecArgs(args, 2)
			if err != nil {
				return err
			}
			root, err := findRoot()
			if err != nil {
				return err
			}
			sm, branch := pos[0], pos[1]
			dir := repo.SubmoduleWorktreePath(root, sm, branch)
			return runExecInCobra(cmd, dir, "beehive submodule worktree add "+sm+" "+branch, child)
		},
	}
}
