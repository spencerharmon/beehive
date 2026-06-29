package main

import (
	"fmt"

	"github.com/spencerharmon/beehive/internal/config"
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
		Short: "scaffold a beehive repo and install the ROI-protect hook",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := args[0]
			if err := repo.Init(path); err != nil {
				return err
			}
			// Hook install is best-effort: only if path is already a git repo.
			_ = config.InstallROIHook(path)
			fmt.Println("beehive repo at", path)
			return nil
		},
	}
}

func hookCmd() *cobra.Command {
	c := &cobra.Command{Use: "hook", Short: "git hook management"}
	c.AddCommand(&cobra.Command{
		Use:   "install <repo>",
		Short: "install the ROI-protect pre-commit hook",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := config.InstallROIHook(args[0]); err != nil {
				return err
			}
			fmt.Println("ROI-protect hook installed")
			return nil
		},
	})
	return c
}
