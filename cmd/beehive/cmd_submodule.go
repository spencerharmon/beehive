package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
		submoduleWorktreeCmd(), submoduleSyncCmd(), submoduleRemoteCmd(),
		submodulePointerBumpCmd())
	return c
}

// submodulePointerBumpCmd bumps the superproject gitlink for a submodule to a
// specific commit, but ONLY after confirming that commit is durably pushed to
// the submodule's origin. This closes the chronic self-hosting defect where the
// gitlink was bumped to a worktree commit that never reached origin, leaving a
// dangling pointer on every other host (e.g. 62243addcc, 672eabd857): the bump
// is REFUSED with an error naming the unreachable sha rather than recording a
// pointer no peer can resolve. The caller stages/commits the bumped gitlink.
func submodulePointerBumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pointer-bump <submodule> <commit>",
		Short: "bump a submodule's gitlink to a commit confirmed pushed to its origin",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, a []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			return pointerBumpSubmodule(cmd.Context(), root, a[0], a[1])
		},
	}
}

// pointerBumpSubmodule resolves the submodule's tracked branch + origin, resolves
// commit to a full sha in the submodule checkout, and refuses the gitlink bump
// unless that sha is reachable from the origin's branch tip. On success it
// rewrites the gitlink index entry in the beehive layer (root repo).
func pointerBumpSubmodule(ctx context.Context, root, sm, commit string) error {
	rel := filepath.Join("submodules", sm, "repo")
	repoDir := filepath.Join(root, rel)
	if _, err := os.Stat(repoDir); err != nil {
		return fmt.Errorf("no repo at %s", rel)
	}
	rootGit := git.New(root)
	subGit := git.New(repoDir)
	sha, err := subGit.RevParse(ctx, commit)
	if err != nil {
		return fmt.Errorf("resolve %s in %s: %w", commit, rel, err)
	}
	branch, err := rootGit.Run(ctx, "config", "-f", ".gitmodules", "submodule."+rel+".branch")
	if err != nil || branch == "" {
		branch = "main"
	}
	remote, err := subGit.Remote(ctx)
	if err != nil {
		return err
	}
	pushed, err := subGit.RemoteContainsCommit(ctx, remote, branch, sha)
	if err != nil {
		return fmt.Errorf("confirm %s reachable on %s origin: %w", sha, sm, err)
	}
	if !pushed {
		return fmt.Errorf("refusing to bump %s gitlink to %s: commit is NOT pushed to the submodule origin (%s/%s) — it would dangle on every other host; push the %s branch first",
			sm, sha, remote, branch, branch)
	}
	if err := rootGit.BumpGitlink(ctx, rel, sha); err != nil {
		return err
	}
	fmt.Printf("%s gitlink bumped to %s (confirmed on %s/%s)\n", rel, sha, remote, branch)
	return nil
}

// submoduleRemoteCmd repoints a submodule's tracked remote URL through the shared
// submod.SetRemoteURL (the same body the frontend's "change remote" action uses),
// then commits the .gitmodules rewrite. The checkout's origin and the cached
// submodule url are updated by `git submodule sync` inside SetRemoteURL.
func submoduleRemoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remote <submodule> <url>",
		Short: "change a submodule's tracked remote URL (.gitmodules + submodule sync)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			// Convergence protocol (docs/main-convergence-protocol.md): this verb
			// authors a .gitmodules commit DIRECTLY on the primary main, so it must
			// merge the hive remote into local main BEFORE authoring, or it
			// manufactures the fork ff-only pullMain cannot heal. Mirrors
			// syncSubmodule's sync-before/publish-after call-site pattern exactly.
			rootGit := git.New(root)
			remote, _ := rootGit.Remote(cmd.Context())
			if err := rootGit.SyncMainFromRemote(cmd.Context(), remote); err != nil {
				return err
			}
			rel, err := submod.SetRemoteURL(cmd.Context(), root, args[0], args[1])
			if err != nil {
				return err
			}
			if err := rootGit.CommitPaths(cmd.Context(), "submodule remote: "+args[0]+" -> "+args[1], ".gitmodules"); err != nil && err != git.ErrNothing {
				return err
			}
			if err := rootGit.PublishPrimaryMain(cmd.Context(), remote); err != nil {
				return err
			}
			fmt.Printf("%s remote set to %s\n", rel, args[1])
			return nil
		},
	}
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
	// Convergence protocol (docs/main-convergence-protocol.md): this verb authors a
	// gitlink-bump commit DIRECTLY on the primary main, so it must first merge the
	// hive remote into local main. Committing on a stale base manufactures a fork
	// that ff-only pullMain cannot heal — the exact silent-loss bug this guards.
	remote, _ := rootGit.Remote(ctx)
	if err := rootGit.SyncMainFromRemote(ctx, remote); err != nil {
		return err
	}
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
	// Publish the bump to the hive remote so it is not stranded on local main
	// (the other half of the invariant: write on a fresh base, then push remote).
	if err := rootGit.PublishPrimaryMain(ctx, remote); err != nil {
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
	return c
}
