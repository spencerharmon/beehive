package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

// planCmd groups deterministic PLAN.md maintenance that is not a status change.
func planCmd() *cobra.Command {
	c := &cobra.Command{Use: "plan", Short: "maintain PLAN.md"}
	c.AddCommand(planArchiveCmd())
	return c
}

// planArchiveCmd offloads the post-completion Impl/Review narrative of DONE tasks
// out of PLAN.md into docs/plan-archive/<id>.md, leaving the lean task card.
// PLAN.md is re-read in full by every honeybee each pass (it holds the live claim
// metadata), and the bulk is closed-task audit prose — not task input — so leaning
// it directly cuts per-pass tokens. The transform (internal/plan.Archive) only
// edits DONE-task bodies: OPEN tasks and ALL claim metadata (session/heartbeat)
// are left byte-for-byte identical and Parse still round-trips, so the change is
// safe to publish. Run it as a maintenance/reconcile step through the normal
// worktree->publish flow, not by hand-editing a live tree an active pass shares.
func planArchiveCmd() *cobra.Command {
	var submodule string
	var commit bool
	cmd := &cobra.Command{
		Use:   "archive [submodule]",
		Short: "offload DONE-task Impl/Review narrative to docs/plan-archive/, leaving lean task cards",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			rp, err := repo.Open(root)
			if err != nil {
				return err
			}
			name := submodule
			if len(args) == 1 {
				name = args[0]
			}
			sm, err := resolveSubmodule(rp, name)
			if err != nil {
				return err
			}
			planPath := sm.PlanPath()
			b, err := os.ReadFile(planPath)
			if err != nil {
				return err
			}
			p, err := plan.Parse(string(b))
			if err != nil {
				return err
			}
			docs := p.Archive()
			if len(docs) == 0 {
				fmt.Printf("%s: PLAN.md already lean; nothing to archive\n", sm.Name)
				return nil
			}
			// Persist each offloaded narrative doc (Path is relative to the
			// submodule dir, alongside PLAN.md), collecting repo-root-relative
			// pathspecs for a scoped commit.
			paths := make([]string, 0, len(docs)+1)
			for _, d := range docs {
				abs := filepath.Join(sm.Path, filepath.FromSlash(d.Path))
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(abs, []byte(d.Content), 0o644); err != nil {
					return err
				}
				rel, err := filepath.Rel(root, abs)
				if err != nil {
					return err
				}
				paths = append(paths, filepath.ToSlash(rel))
			}
			// Rewrite the leaned PLAN.md.
			leaned := p.String()
			if err := os.WriteFile(planPath, []byte(leaned), 0o644); err != nil {
				return err
			}
			planRel, err := filepath.Rel(root, planPath)
			if err != nil {
				return err
			}
			paths = append([]string{filepath.ToSlash(planRel)}, paths...)

			saved := len(b) - len(leaned)
			if commit {
				msg := fmt.Sprintf("plan: archive %d DONE task narrative(s) for %s (-%d bytes)",
					len(docs), sm.Name, saved)
				if err := git.New(root).CommitPaths(cmd.Context(), msg, paths...); err != nil && err != git.ErrNothing {
					return err
				}
			}
			fmt.Printf("%s: archived %d DONE narrative(s), PLAN.md %d -> %d bytes (-%d)\n",
				sm.Name, len(docs), len(b), len(leaned), saved)
			for _, d := range docs {
				fmt.Printf("  %s\n", d.Path)
			}
			if !commit {
				fmt.Println("(--commit=false: files written, not committed)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&submodule, "submodule", "", "submodule whose PLAN.md to archive (default: the only one)")
	cmd.Flags().BoolVar(&commit, "commit", true, "commit the leaned PLAN.md + archive docs (scoped)")
	return cmd
}
