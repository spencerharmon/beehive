package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/submod"
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
	c.AddCommand(submoduleAddCmd(), submoduleLinkCmd(), submodulePlanCmd(),
		submoduleWorktreeCmd(), submoduleSyncCmd())
	return c
}

// submoduleWorktreeCmd manages worktrees of a submodule's target repo, where a
// honeybee's code edits for a task live (submodules/<sm>/worktrees/<branch>),
// kept separate from the beehive-layer worktrees.
func submoduleWorktreeCmd() *cobra.Command {
	c := &cobra.Command{Use: "worktree", Short: "manage submodule target-repo worktrees"}
	repoDir := func(root, sm string) string { return filepath.Join(root, "submodules", sm, "repo") }

	c.AddCommand(&cobra.Command{
		Use:   "add <submodule> <branch>",
		Short: "sync the tracked tip, then branch a worktree off it",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, a []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			if err := syncSubmodule(cmd.Context(), root, a[0]); err != nil {
				return err
			}
			g := git.New(repoDir(root, a[0]))
			if err := g.WorktreeAdd(cmd.Context(), filepath.Join("..", "worktrees", a[1]), a[1], "HEAD"); err != nil {
				return err
			}
			fmt.Println(filepath.Join("submodules", a[0], "worktrees", a[1]))
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "rm <submodule> <branch>",
		Short: "remove a submodule worktree and its branch",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, a []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			g := git.New(repoDir(root, a[0]))
			if err := g.WorktreeRemove(cmd.Context(), filepath.Join("..", "worktrees", a[1])); err != nil {
				return err
			}
			_, _ = g.Run(cmd.Context(), "branch", "-D", a[1])
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "list <submodule>",
		Short: "list a submodule's worktrees",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, a []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			wts, err := git.New(repoDir(root, a[0])).Worktrees(cmd.Context())
			if err != nil {
				return err
			}
			for _, w := range wts {
				br := w.Branch
				if br == "" {
					br = "(detached)"
				}
				fmt.Printf("%-12s %-40s %s\n", br, w.Path, short(w.HEAD))
			}
			return nil
		},
	})
	return c
}

// submoduleSyncCmd advances a submodule to the tip of its tracked branch.
func submoduleSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync <submodule>",
		Short: "fetch and fast-forward a submodule to its tracked branch tip",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, a []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			return syncSubmodule(cmd.Context(), root, a[0])
		},
	}
}

// syncSubmodule tracks the tip of a submodule's configured branch (nonstandard:
// the beehive pointer follows a branch, not a pinned commit). It fetches, takes
// the remote tip verbatim (auto-clobber: upstream may be force-pushed), and
// advances the beehive pointer with an auto-commit when it moved.
func syncSubmodule(ctx context.Context, root, sm string) error {
	rel := filepath.Join("submodules", sm, "repo")
	repoDir := filepath.Join(root, rel)
	if _, err := os.Stat(repoDir); err != nil {
		return fmt.Errorf("no repo at %s", rel)
	}
	rootGit := git.New(root)
	branch, err := rootGit.Run(ctx, "config", "-f", ".gitmodules", "submodule."+rel+".branch")
	if err != nil || branch == "" {
		branch = "main"
	}
	g := git.New(repoDir)
	if _, err := g.Run(ctx, "fetch", "origin", branch, "--prune"); err != nil {
		return err
	}
	if _, err := g.Run(ctx, "checkout", branch); err != nil {
		return err
	}
	if err := g.HardReset(ctx, "origin/"+branch); err != nil {
		return err
	}
	if err := rootGit.CommitPaths(ctx, "submodule sync: "+sm+" -> "+branch+" tip\n\nBeehive: submodule-sync "+sm, rel); err != nil && err != git.ErrNothing {
		return err
	}
	head, _ := g.Run(ctx, "rev-parse", "--short", "HEAD")
	fmt.Printf("%s on %s at %s\n", rel, branch, head)
	return nil
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
			added, err := submod.Add(cmd.Context(), root, args[0], name, branch)
			if err != nil {
				return err
			}
			fmt.Printf("added submodule %s tracking %s (dormant; author ROI.md to activate)\n", added, branch)
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
			if err := submod.LinkSubmodules(root, args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("linked %s <-> %s\n", args[0], args[1])
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
	c.AddCommand(&cobra.Command{
		Use:   "archive <submodule>",
		Short: "move DONE tasks' Impl/Review narrative out of PLAN.md into docs/plan-archive/<id>.md, leaving lean cards",
		Long: "Keep PLAN.md proportional to OPEN work. Every DONE task's post-hoc\n" +
			"Impl/Review/Reconciled/Arbitration narrative is moved into\n" +
			"submodules/<sm>/docs/plan-archive/<id>.md, leaving the lean card (header +\n" +
			"description + Files/Doc/Accept) plus a one-line pointer. OPEN tasks and all\n" +
			"claim metadata are untouched; a re-run is a no-op. Run it in a beehive-layer\n" +
			"worktree so the leaned PLAN.md publishes via the normal path — never edit the\n" +
			"live tree in place (it would race the runner's heartbeat).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			subName, err := taskSubmoduleName(args[0])
			if err != nil {
				return err
			}
			n, err := archivePlan(cmd.Context(), root, subName)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Printf("%s: PLAN.md already lean (nothing to archive)\n", subName)
			} else {
				fmt.Printf("%s: archived %d DONE task narrative(s) to submodules/%s/%s/\n",
					subName, n, subName, plan.ArchiveDir)
			}
			return nil
		},
	})
	return c
}

// archivePlan leans a submodule's PLAN.md: it moves every DONE task's post-hoc
// narrative into docs/plan-archive/<id>.md, rewrites PLAN.md with the lean cards,
// and commits the two together (the worktree->publish path). It never touches
// ROI.md, an OPEN task, or any claim metadata. Returns the number of tasks
// archived; 0 means the plan was already lean (a no-op, nothing committed).
func archivePlan(ctx context.Context, root, subName string) (int, error) {
	planRel := filepath.Join("submodules", subName, repo.PlanFile)
	planPath := filepath.Join(root, planRel)
	b, err := os.ReadFile(planPath)
	if err != nil {
		return 0, err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return 0, err
	}
	archived := p.ArchiveDone()
	if len(archived) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(archived))
	for id := range archived {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	paths := []string{planRel}
	for _, id := range ids {
		rel := filepath.Join("submodules", subName, plan.ArchivePath(id))
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return 0, err
		}
		if err := writeArchive(abs, id, archived[id]); err != nil {
			return 0, err
		}
		paths = append(paths, rel)
	}
	if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
		return 0, err
	}
	msg := fmt.Sprintf("plan: archive %d DONE narrative(s) for %s\n\nBeehive: %s plan-archive",
		len(ids), subName, subName)
	if err := git.New(root).CommitPaths(ctx, msg, paths...); err != nil && err != git.ErrNothing {
		return 0, err
	}
	return len(ids), nil
}

// writeArchive creates, or appends to, a task's archive file. Appending (rather
// than overwriting) preserves a prior epoch's narrative when a task was reopened
// and re-completed; because ArchiveDone only returns tasks that still carry
// narrative, a plain re-run appends nothing.
func writeArchive(abs, id string, narrative []string) error {
	prev, err := os.ReadFile(abs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var out strings.Builder
	if len(prev) == 0 {
		out.WriteString("# " + id + " (archived plan narrative)\n\n")
	} else {
		out.Write(prev)
		if !strings.HasSuffix(string(prev), "\n") {
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}
	out.WriteString(strings.Join(narrative, "\n"))
	out.WriteString("\n")
	return os.WriteFile(abs, []byte(out.String()), 0o644)
}
