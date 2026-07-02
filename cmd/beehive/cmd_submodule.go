package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// archiveStoreRel is the submodule-relative store for archived PLAN.md closure
// narratives. It matches the `Archived:` pointer left on each leaned card and the
// `Doc: docs/...` convention (submodule-relative, forward-slashed).
const archiveStoreRel = "docs/plan-archive"

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
	c.AddCommand(submodulePlanArchiveCmd())
	return c
}

// submodulePlanArchiveCmd moves each DONE task's Impl/Review/Reconciled/
// Arbitration closure prose out of PLAN.md into docs/plan-archive/<id>.md,
// leaving the lean task card (header + description + Files/Doc/Accept + an
// `Archived:` pointer). It keeps PLAN.md proportional to OPEN work — every
// honeybee re-reads the whole file each session — without losing the DONE record.
// Behavior-preserving (plan.Parse round-trips the same tasks/statuses/deps/
// weights/claims) and idempotent (a re-run over an already-lean plan is a no-op).
//
// Run it from a beehive-repo worktree and let the commit publish/merge to main —
// never against the live main checkout under a running runner, whose heartbeat
// restamps PLAN.md (an in-place rewrite of the live tree would race it).
func submodulePlanArchiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "archive <submodule>",
		Short: "move DONE tasks' closure prose out of PLAN.md into docs/plan-archive/, leaving lean cards",
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
			archived := p.ArchiveDone(archiveStoreRel)
			if len(archived) == 0 {
				fmt.Printf("%s: nothing to archive (PLAN.md already lean)\n", subName)
				return nil
			}
			archiveDirRel := filepath.Join("submodules", subName, "docs", "plan-archive")
			if err := os.MkdirAll(filepath.Join(root, archiveDirRel), 0o755); err != nil {
				return err
			}
			paths := []string{planRel}
			for _, a := range archived {
				rel := filepath.Join(archiveDirRel, a.ID+".md")
				if err := writeArchiveDoc(filepath.Join(root, rel), a.ID, a.Narrative); err != nil {
					return err
				}
				paths = append(paths, rel)
			}
			if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
				return err
			}
			msg := fmt.Sprintf("plan: archive %d DONE narrative(s) for %s", len(archived), subName)
			if err := git.New(root).CommitPaths(cmd.Context(), msg, paths...); err != nil && err != git.ErrNothing {
				return err
			}
			fmt.Printf("%s: archived %d DONE task(s) -> %s/\n", subName, len(archived), archiveDirRel)
			return nil
		},
	}
}

// archiveDocContent renders the archive file for one task: a titled preamble
// plus the moved narrative verbatim. Deterministic (no timestamps) so re-runs and
// tests are reproducible.
func archiveDocContent(id, narrative string) string {
	return "# " + id + " — archived PLAN.md closure narrative\n\n" +
		"Moved out of PLAN.md by `beehive submodule plan archive` to keep the plan proportional to\n" +
		"open work. The task's change doc under docs/ remains the authoritative record; this file\n" +
		"preserves the plan-embedded Impl/Review/Reconciled/Arbitration prose verbatim.\n\n" +
		narrative + "\n"
}

// writeArchiveDoc writes an archived narrative to path. If a prior archive for
// this id already exists (a DONE task that was re-opened then re-closed), the new
// section is appended under a divider so no closure record is ever lost.
func writeArchiveDoc(path, id, narrative string) error {
	content := archiveDocContent(id, narrative)
	if existing, err := os.ReadFile(path); err == nil && len(existing) > 0 {
		content = strings.TrimRight(string(existing), "\n") + "\n\n<!-- re-archived after re-open -->\n\n" + content
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
