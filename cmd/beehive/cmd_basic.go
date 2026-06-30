package main

import (
	"fmt"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version",
		Run:   func(*cobra.Command, []string) { fmt.Println("beehive dev") },
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <path>",
		Short: "scaffold a beehive repo and install the git hooks (ROI-protect + submodule-sync)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if err := repo.Init(path); err != nil {
				return err
			}
			// Init always leaves path as a git repo on main. Lay down ALL hooks
			// idempotently, so a re-run upgrades stale hooks; .git/hooks is never
			// tracked by git.
			if err := config.InstallHooks(path); err != nil {
				return err
			}
			// Allow honeybee worktrees to publish to the checked-out main branch
			// via `git push . HEAD:main` (local, no-remote convergence).
			g := git.New(path)
			if _, err := g.Run(cmd.Context(), "config", "receive.denyCurrentBranch", "updateInstead"); err != nil {
				return err
			}
			fmt.Println("beehive repo at", path)
			return nil
		},
	}
}

func hookCmd() *cobra.Command {
	c := &cobra.Command{Use: "hook", Short: "git hook management"}
	c.AddCommand(&cobra.Command{
		Use:   "install <repo>",
		Short: "install (or re-install) all beehive git hooks: ROI-protect pre-commit + submodule-sync post-receive",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := config.InstallHooks(args[0]); err != nil {
				return err
			}
			fmt.Println("beehive git hooks installed (pre-commit + post-receive)")
			return nil
		},
	})
	return c
}
