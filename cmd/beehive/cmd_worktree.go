package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spf13/cobra"
)

// worktreeCmd manages worktrees of the top-level beehive repo. Each honeybee
// runs in one of these; this is the operator-facing view and manual control.
func worktreeCmd() *cobra.Command {
	c := &cobra.Command{Use: "worktree", Short: "manage top-level beehive repo worktrees"}

	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list beehive repo worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			wts, err := git.New(root).Worktrees(cmd.Context())
			if err != nil {
				return err
			}
			for _, w := range wts {
				br := w.Branch
				if br == "" {
					br = "(detached)"
				}
				fmt.Printf("%-10s %-40s %s\n", br, w.Path, short(w.HEAD))
			}
			return nil
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "add <branch>",
		Short: "create a beehive worktree off main under .worktrees/",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, a []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			wt := filepath.Join(root, ".worktrees", a[0])
			if err := git.New(root).WorktreeAdd(cmd.Context(), wt, a[0], "main"); err != nil {
				return err
			}
			fmt.Println(wt)
			return nil
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "rm <branch>",
		Short: "remove a beehive worktree and its branch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, a []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			g := git.New(root)
			if err := g.WorktreeRemove(cmd.Context(), filepath.Join(root, ".worktrees", a[0])); err != nil {
				return err
			}
			_, _ = g.Run(cmd.Context(), "branch", "-D", a[0])
			return nil
		},
	})
	return c
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func honeybeeCmd() *cobra.Command {
	c := &cobra.Command{Use: "honeybee", Short: "honeybee process control"}
	debug := false
	start := &cobra.Command{
		Use:   "start <path>",
		Short: "start a honeybee on a beehive repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hbArgs := []string{}
			if debug {
				hbArgs = append(hbArgs, "--debug")
			}
			ex := exec.CommandContext(cmd.Context(), "honeybee", hbArgs...)
			ex.Dir = args[0]
			ex.Stdin, ex.Stdout, ex.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := ex.Run(); err != nil {
				return fmt.Errorf("honeybee: %w", err)
			}
			return nil
		},
	}
	start.Flags().BoolVar(&debug, "debug", false, "stream session turns to stderr")
	c.AddCommand(start)
	return c
}
