package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

func worktreeCmd() *cobra.Command {
	c := &cobra.Command{Use: "worktree", Short: "manage per-branch honeybee worktrees"}
	run := func(cmd *cobra.Command, op, sm, br string) error {
		root, err := findRoot()
		if err != nil {
			return err
		}
		script := filepath.Join(root, "scripts", "worktree.sh")
		ex := exec.CommandContext(cmd.Context(), "sh", script, op, sm, br)
		ex.Dir = root
		ex.Stdout, ex.Stderr = os.Stdout, os.Stderr
		return ex.Run()
	}
	c.AddCommand(&cobra.Command{
		Use: "add <submodule> <branch>", Short: "create a worktree off the synced tip",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, a []string) error { return run(cmd, "add", a[0], a[1]) },
	}, &cobra.Command{
		Use: "rm <submodule> <branch>", Short: "remove a worktree",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, a []string) error { return run(cmd, "rm", a[0], a[1]) },
	})
	return c
}

func honeybeeCmd() *cobra.Command {
	c := &cobra.Command{Use: "honeybee", Short: "honeybee process control"}
	c.AddCommand(&cobra.Command{
		Use:   "start <path>",
		Short: "start a honeybee on a beehive repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ex := exec.CommandContext(cmd.Context(), "honeybee")
			ex.Dir = args[0]
			ex.Stdin, ex.Stdout, ex.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := ex.Run(); err != nil {
				return fmt.Errorf("honeybee: %w", err)
			}
			return nil
		},
	})
	return c
}
