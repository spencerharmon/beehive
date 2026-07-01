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

// planArchiveCmd leans a submodule's PLAN.md: it moves each DONE task's
// Impl/Review/Reconciled/Arbitration narrative into docs/plan-archive/<id>.md,
// leaving the lean task card. It is a deterministic parser-based transform (never
// an in-place sed), so it is safe to run like any other task through the normal
// worktree->publish path; --commit records the change.
func planArchiveCmd() *cobra.Command {
	var doCommit bool
	cmd := &cobra.Command{
		Use:   "archive <submodule>",
		Short: "move DONE tasks' audit narrative from PLAN.md into docs/plan-archive/<id>.md",
		Long: "Archive keeps PLAN.md proportional to open work: it moves every DONE task's\n" +
			"post-hoc Impl/Review/Reconciled/Arbitration narrative out of PLAN.md into\n" +
			"docs/plan-archive/<id>.md, leaving the lean task card (header + description +\n" +
			"Files/Doc/Accept) plus a one-line pointer. OPEN tasks and all claim metadata are\n" +
			"untouched and the parsed task set is preserved; re-running is a no-op.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			sub, err := taskSubmoduleName(args[0])
			if err != nil {
				return err
			}
			ids, changed, before, after, err := archivePlan(root, sub)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				fmt.Printf("%s: PLAN.md already lean (nothing to archive)\n", sub)
				return nil
			}
			fmt.Printf("%s: archived %d DONE task(s) [%s]; PLAN.md %d -> %d bytes\n",
				sub, len(ids), strings.Join(ids, ", "), before, after)
			if !doCommit {
				fmt.Println("review the changes and commit, or re-run with --commit")
				return nil
			}
			msg := fmt.Sprintf("plan: archive %d DONE task narrative(s) in %s\n\nBeehive: plan-archive plan", len(ids), sub)
			if err := git.New(root).CommitPaths(cmd.Context(), msg, changed...); err != nil && err != git.ErrNothing {
				return err
			}
			fmt.Println("committed")
			return nil
		},
	}
	cmd.Flags().BoolVar(&doCommit, "commit", false, "commit the leaned PLAN.md and archive docs")
	return cmd
}

// archivePlan leans submodules/<sub>/PLAN.md under root: it writes each DONE task's
// narrative to submodules/<sub>/docs/plan-archive/<id>.md and rewrites the lean
// PLAN.md, returning the archived task ids, the changed repo-relative paths, and
// the before/after PLAN.md byte sizes. Pure file I/O; the caller commits. A no-op
// (already lean) returns no ids and leaves every file untouched.
func archivePlan(root, sub string) (ids, changed []string, before, after int, err error) {
	subDir := filepath.Join("submodules", sub)
	planRel := filepath.Join(subDir, repo.PlanFile)
	planPath := filepath.Join(root, planRel)
	b, err := os.ReadFile(planPath)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return nil, nil, 0, 0, err
	}
	archived := p.Archive()
	if len(archived) == 0 {
		return nil, nil, len(b), len(b), nil
	}
	if err := os.MkdirAll(filepath.Join(root, subDir, plan.ArchiveDir), 0o755); err != nil {
		return nil, nil, 0, 0, err
	}
	changed = []string{planRel}
	for _, a := range archived {
		rel := filepath.Join(subDir, plan.ArchivePath(a.ID))
		if err := os.WriteFile(filepath.Join(root, rel), []byte(a.Doc()), 0o644); err != nil {
			return nil, nil, 0, 0, err
		}
		ids = append(ids, a.ID)
		changed = append(changed, rel)
	}
	out := p.String()
	if err := os.WriteFile(planPath, []byte(out), 0o644); err != nil {
		return nil, nil, 0, 0, err
	}
	return ids, changed, len(b), len(out), nil
}
