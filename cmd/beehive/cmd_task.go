package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

func taskCmd() *cobra.Command {
	c := &cobra.Command{Use: "task", Short: "manage PLAN.md tasks"}
	c.AddCommand(taskHumanCmd())
	return c
}

func taskHumanCmd() *cobra.Command {
	var reason, reasonFile string
	cmd := &cobra.Command{
		Use:     "human <submodule> <task-id>",
		Aliases: []string{"needs-human", "request-human"},
		Short:   "move a task to NEEDS-HUMAN with a concrete reason",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			reasonText, err := humanReason(reason, reasonFile)
			if err != nil {
				return err
			}
			subName, err := taskSubmoduleName(args[0])
			if err != nil {
				return err
			}
			planRel := filepath.Join("submodules", subName, repo.PlanFile)
			planPath := filepath.Join(root, planRel)
			b, err := os.ReadFile(planPath)
			if err != nil {
				return err
			}
			p, err := plan.Parse(string(b))
			if err != nil {
				return err
			}
			t := p.Find(args[1])
			if t == nil {
				return fmt.Errorf("task %q not found in %s", args[1], planRel)
			}
			if err := t.RequestHuman(reasonText, time.Now().UTC()); err != nil {
				return err
			}
			if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
				return err
			}
			msg := fmt.Sprintf("plan: request human for %s\n\nBeehive: %s plan\nReason: %s", args[1], args[1], t.HumanReason())
			if err := git.New(root).CommitPaths(cmd.Context(), msg, planRel); err != nil && err != git.ErrNothing {
				return err
			}
			fmt.Printf("%s %s -> %s\n", subName, args[1], plan.StatusHuman)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "concrete reason operator input is required")
	cmd.Flags().StringVar(&reasonFile, "reason-file", "", "read concrete reason from file")
	return cmd
}

func taskSubmoduleName(s string) (string, error) {
	s = filepath.Clean(s)
	if strings.HasPrefix(s, "submodules"+string(filepath.Separator)) {
		s = strings.TrimPrefix(s, "submodules"+string(filepath.Separator))
	}
	if filepath.IsAbs(s) || s == "." || s == ".." || s == "submodules" || strings.Contains(s, string(filepath.Separator)) {
		return "", fmt.Errorf("submodule must be a name under submodules/: %q", s)
	}
	return s, nil
}

func humanReason(reason, reasonFile string) (string, error) {
	if reason != "" && reasonFile != "" {
		return "", fmt.Errorf("use --reason or --reason-file, not both")
	}
	if reasonFile != "" {
		b, err := os.ReadFile(reasonFile)
		if err != nil {
			return "", err
		}
		reason = string(b)
	}
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		return "", fmt.Errorf("--reason or --reason-file is required")
	}
	return reason, nil
}
