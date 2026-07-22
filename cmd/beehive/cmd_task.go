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
	var reason, reasonFile, category string
	cmd := &cobra.Command{
		Use:     "human <submodule> <task-id>",
		Aliases: []string{"needs-human", "request-human"},
		Short:   "move a task to NEEDS-HUMAN with a category + concrete reason",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			// Convergence protocol (docs/main-convergence-protocol.md): task human
			// authors a PLAN.md commit DIRECTLY on the primary main, so it must merge
			// the hive remote into local main BEFORE authoring (and publish after), or
			// it manufactures the fork ff-only pullMain cannot heal. Mirrors
			// syncSubmodule's sync-before/publish-after call-site pattern exactly.
			rootGit := git.New(root)
			remote, _ := rootGit.Remote(cmd.Context())
			if err := rootGit.SyncMainFromRemote(cmd.Context(), remote); err != nil {
				return err
			}
			cat, err := humanCategory(category)
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
			if err := t.RequestHuman(cat, reasonText, time.Now().UTC()); err != nil {
				return err
			}
			if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
				return err
			}
			msg := fmt.Sprintf("plan: request human for %s (%s)\n\nBeehive: %s plan\nCategory: %s\nReason: %s", args[1], cat, args[1], cat, t.HumanReason())
			if err := git.New(root).CommitPaths(cmd.Context(), msg, planRel); err != nil && err != git.ErrNothing {
				return err
			}
			if err := rootGit.PublishPrimaryMain(cmd.Context(), remote); err != nil {
				return err
			}
			fmt.Printf("%s %s -> %s\n", subName, args[1], plan.StatusHuman)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "concrete reason operator input is required")
	cmd.Flags().StringVar(&reasonFile, "reason-file", "", "read concrete reason from file")
	cmd.Flags().StringVar(&category, "category", "", "escalation category (required): secret | external-permission | contradiction | architecture")
	return cmd
}

// humanCategory validates the --category flag against the four legitimate
// escalation categories. It is required: an empty or unknown value is an error,
// so an escalation can never be filed unclassified.
func humanCategory(category string) (plan.Category, error) {
	if strings.TrimSpace(category) == "" {
		return "", fmt.Errorf("--category is required, one of %v", plan.Categories())
	}
	c := plan.Category(strings.TrimSpace(category))
	if !c.Valid() {
		return "", fmt.Errorf("unknown --category %q, one of %v", category, plan.Categories())
	}
	return c, nil
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
