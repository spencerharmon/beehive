package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

// `beehive git` and `beehive submodule git` are the sanctioned way to run git
// against the two worktrees a honeybee juggles — the HIVE/superrepo worktree
// (PLAN.md, docs/, the beehive layer) and the per-task SUBMODULE code worktree —
// WITHOUT ever `cd`-ing between them or guessing a relative path. Each resolves an
// ABSOLUTE worktree path and runs `git -C <path> <args...>`, so the agent's cwd is
// irrelevant and it can never operate git on the wrong tree (the exact confusion
// this pair exists to remove). Path resolution order:
//
//  1. the explicit env var (BEEHIVE_WORKTREE / SUBMODULE_WORKTREE) if set;
//  2. the per-pass repo.WorktreeEnvFile the runner wrote at the hive root (the
//     reliable channel, since opencode's shell does not inherit the runner env);
//  3. a deterministic fallback — findRoot() for the hive; the sole
//     .submodule-worktrees/<sm>/<branch> dir for the submodule.
//
// Both are pure pass-throughs (DisableFlagParsing) so every git flag reaches git
// verbatim, and both propagate git's exit status.

// gitCmd is `beehive git <args...>` → `git -C $BEEHIVE_WORKTREE <args...>`.
func gitCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "git [git args...]",
		Short:              "run git in the beehive/superrepo worktree ($BEEHIVE_WORKTREE)",
		Long:               "Run git against the HIVE (superrepo) worktree — the beehive layer (PLAN.md, docs/). Resolves the worktree from $BEEHIVE_WORKTREE, else the per-pass " + repo.WorktreeEnvFile + " file, else the enclosing beehive repo root. Use this instead of a bare `git` or `cd`+git so you never operate on the wrong worktree.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveBeehiveWorktree()
			if err != nil {
				return err
			}
			return runGitIn(cmd, dir, args)
		},
	}
}

// gitSubmoduleCmd is `beehive submodule git <args...>` →
// `git -C $SUBMODULE_WORKTREE <args...>`.
func gitSubmoduleCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "git [git args...]",
		Short:              "run git in the current task's submodule code worktree ($SUBMODULE_WORKTREE)",
		Long:               "Run git against the per-task SUBMODULE code worktree (your editable checkout of the target repo). Resolves the worktree from $SUBMODULE_WORKTREE, else the per-pass " + repo.WorktreeEnvFile + " file, else the sole .submodule-worktrees/<sm>/<branch> dir. Use this instead of a bare `git` or `cd`+git so your git commands never hit the shared submodules/<sm>/repo checkout or the hive worktree by mistake.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveSubmoduleWorktree()
			if err != nil {
				return err
			}
			return runGitIn(cmd, dir, args)
		},
	}
}

// runGitIn execs `git -C dir args...` with the parent's stdio, so output streams
// live and stdin passes through. git's non-zero exit is propagated as the process
// exit status (a pass-through must mirror git, not wrap it).
func runGitIn(cmd *cobra.Command, dir string, args []string) error {
	full := append([]string{"-C", dir}, args...)
	ex := exec.CommandContext(cmd.Context(), "git", full...)
	ex.Stdin, ex.Stdout, ex.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := ex.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return fmt.Errorf("git -C %s: %w", dir, err)
	}
	return nil
}

// resolveBeehiveWorktree resolves the hive/superrepo worktree path.
func resolveBeehiveWorktree() (string, error) {
	if v := strings.TrimSpace(os.Getenv("BEEHIVE_WORKTREE")); v != "" {
		return v, nil
	}
	if v := passEnv("BEEHIVE_WORKTREE"); v != "" {
		return v, nil
	}
	// Fallback: the enclosing beehive repo root (the agent's cwd is the hive
	// worktree, so this is correct without any env).
	return findRoot()
}

// resolveSubmoduleWorktree resolves the current task's submodule code worktree.
func resolveSubmoduleWorktree() (string, error) {
	if v := strings.TrimSpace(os.Getenv("SUBMODULE_WORKTREE")); v != "" {
		return v, nil
	}
	if v := passEnv("SUBMODULE_WORKTREE"); v != "" {
		return v, nil
	}
	// Fallback: the sole .submodule-worktrees/<sm>/<branch> dir under the hive root
	// (a Work pass has exactly one). Ambiguity is refused rather than guessed.
	root, err := findRoot()
	if err != nil {
		return "", err
	}
	wt, err := soleSubmoduleWorktree(root)
	if err != nil {
		return "", err
	}
	return wt, nil
}

// passEnv reads one key from the per-pass repo.WorktreeEnvFile at the hive root.
// Returns "" if the file or key is absent (never an error — this is a fallback
// channel, not a hard requirement).
func passEnv(key string) string {
	root, err := findRoot()
	if err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(root, repo.WorktreeEnvFile))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// soleSubmoduleWorktree returns the single .submodule-worktrees/<sm>/<branch> dir
// under root, erroring when there are zero or many (the caller must then set
// $SUBMODULE_WORKTREE explicitly).
func soleSubmoduleWorktree(root string) (string, error) {
	base := filepath.Join(root, repo.SubmoduleWorktreesDirName)
	sms, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("no submodule worktree found (set $SUBMODULE_WORKTREE): %w", err)
	}
	var found []string
	for _, sm := range sms {
		if !sm.IsDir() {
			continue
		}
		branches, err := os.ReadDir(filepath.Join(base, sm.Name()))
		if err != nil {
			continue
		}
		for _, br := range branches {
			if br.IsDir() {
				found = append(found, filepath.Join(base, sm.Name(), br.Name()))
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("no submodule code worktree under %s; set $SUBMODULE_WORKTREE", base)
	default:
		return "", fmt.Errorf("multiple submodule code worktrees under %s (%s); set $SUBMODULE_WORKTREE to pick one",
			base, strings.Join(found, ", "))
	}
}
