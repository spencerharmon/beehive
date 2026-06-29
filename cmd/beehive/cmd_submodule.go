package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

// findRoot ascends from cwd until AGENTS.md is found.
func findRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, repo.AgentsFile)); err == nil {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return "", fmt.Errorf("not inside a beehive repo (no %s found)", repo.AgentsFile)
		}
		d = p
	}
}

func submoduleCmd() *cobra.Command {
	c := &cobra.Command{Use: "submodule", Short: "manage beehive submodules"}
	c.AddCommand(submoduleAddCmd(), submoduleLinkCmd(), submodulePlanCmd())
	return c
}

func submoduleAddCmd() *cobra.Command {
	var name, branch string
	c := &cobra.Command{
		Use:   "add <repo-url>",
		Short: "add a target repo as a tracked submodule (dormant until ROI.md exists)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			url := args[0]
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(url), ".git")
			}
			subdir := filepath.Join(root, "submodules", name)
			if err := os.MkdirAll(filepath.Join(subdir, "worktrees"), 0o755); err != nil {
				return err
			}
			g := git.New(root)
			rel := filepath.Join("submodules", name, "repo")
			if _, err := g.Run(cmd.Context(), "submodule", "add", "-b", branch, url, rel); err != nil {
				return err
			}
			fmt.Printf("added submodule %s tracking %s (dormant; author ROI.md to activate)\n", name, branch)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "submodule name (default: repo basename)")
	c.Flags().StringVar(&branch, "branch", "main", "tracked branch tip")
	return c
}

func submoduleLinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link <submodule-a> <submodule-b>",
		Short: "link two submodules so each plan may depend on the other",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			a, b := args[0], args[1]
			for _, sm := range []string{a, b} {
				p := filepath.Join(root, "submodules", sm, repo.LinksFile)
				l, err := links.Load(p)
				if err != nil {
					return err
				}
				l.LinkSubmodules(a, b)
				if err := l.Save(p); err != nil {
					return err
				}
			}
			fmt.Printf("linked %s <-> %s\n", a, b)
			return nil
		},
	}
}

func submodulePlanCmd() *cobra.Command {
	c := &cobra.Command{Use: "plan", Short: "submodule plan operations"}
	c.AddCommand(&cobra.Command{
		Use:   "rollback <submodule> <commit>",
		Short: "restore a submodule's PLAN.md to an earlier commit",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			planRel := filepath.Join("submodules", args[0], repo.PlanFile)
			g := git.New(root)
			if err := plan.Rollback(cmd.Context(), g, planRel, args[1]); err != nil {
				return err
			}
			fmt.Printf("rolled %s back to %s (staged; commit to apply)\n", planRel, args[1])
			return nil
		},
	})
	return c
}
