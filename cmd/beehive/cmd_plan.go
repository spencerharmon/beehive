package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

func planCmd() *cobra.Command {
	c := &cobra.Command{Use: "plan", Short: "manage PLAN.md"}
	c.AddCommand(planArchiveCmd())
	return c
}

// planArchiveCmd leans a submodule's PLAN.md: it moves each DONE task's post-hoc
// Impl/Review/Reconciled/Arbitration narrative out of the task card into
// submodules/<sm>/docs/plan-archive/<id>.md, leaving the terse card (description +
// Files/Doc/Accept) on the plan. Every honeybee re-reads PLAN.md each pass, so
// shedding the audit-history prose that piles up as tasks complete is a direct
// token cut. OPEN tasks and all claim metadata (session/heartbeat) are never
// touched, and the change is published as one reviewable commit — never left as an
// unpublished in-place edit that could race the runner's heartbeat restamp.
func planArchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive <submodule>",
		Short: "move DONE-task Impl/Review narrative out of PLAN.md into docs/plan-archive/<id>.md",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
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
			archived := p.ArchiveDone()
			if len(archived) == 0 {
				fmt.Println("plan archive: nothing to archive (all DONE cards already lean)")
				return nil
			}

			archiveDirRel := filepath.Join("submodules", subName, "docs", "plan-archive")
			if err := os.MkdirAll(filepath.Join(root, archiveDirRel), 0o755); err != nil {
				return err
			}
			paths := make([]string, 0, len(archived)+1)
			ids := make([]string, 0, len(archived))
			for _, a := range archived {
				rel := filepath.Join(archiveDirRel, a.ID+".md")
				if err := os.WriteFile(filepath.Join(root, rel), []byte(plan.RenderArchive(a)), 0o644); err != nil {
					return err
				}
				paths = append(paths, rel)
				ids = append(ids, a.ID)
			}
			// Write the leaned plan last: the parser round-trips it (same task set /
			// statuses / deps / weights / claims), only the DONE prose shrank.
			if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
				return err
			}
			paths = append(paths, planRel)

			msg := fmt.Sprintf("plan: archive DONE narrative for %s (%s)\n\nBeehive: plan-lean-task-card %s",
				subName, strings.Join(ids, ", "), archiveDirRel)
			if err := git.New(root).CommitPaths(cmd.Context(), msg, paths...); err != nil && err != git.ErrNothing {
				return err
			}
			fmt.Printf("plan archive: leaned %d DONE task(s) in %s: %s\n", len(archived), subName, strings.Join(ids, ", "))
			return nil
		},
	}
	return cmd
}
