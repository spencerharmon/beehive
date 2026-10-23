package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spencerharmon/beehive/internal/checkpolicy"
	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/swarm"
	"github.com/spf13/cobra"
)

func taskRecurCmd() *cobra.Command {
	var every time.Duration
	cmd := &cobra.Command{
		Use:   "recur <submodule> <task-id> --every 24h",
		Short: "enable daily measurement cycles on an existing TODO without resetting its history",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutatePlanTask(cmd, args[0], func(p *plan.Plan) (string, error) {
				t := p.Find(args[1])
				if t == nil {
					return "", fmt.Errorf("task %q not found", args[1])
				}
				if err := t.EnableRecurrence(every); err != nil {
					return "", err
				}
				return fmt.Sprintf("plan: enable 24h recurrence on %s\n\nBeehive: %s plan", t.ID, t.ID), nil
			})
		},
	}
	cmd.Flags().DurationVar(&every, "every", 0, "recurrence period (required; only 24h is supported)")
	_ = cmd.MarkFlagRequired("every")
	return cmd
}

func taskCycleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cycle <submodule> <task-id>",
		Short: "run a standing task's Check and commit a successful daily cycle locally",
		Long: "Run the registered Check on a clean code worktree at the tracked tip. " +
			"Requires the owning session's non-primary hive worktree. On success, append measured " +
			"output to the standard change doc and commit it with PLAN.md, leaving the task TODO " +
			"until its next daily cycle. Does not push; the runner publishes the hive branch. " +
			"Code changes require normal review, not a measurement cycle.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			subName, err := taskSubmoduleName(args[0])
			if err != nil {
				return err
			}
			root, err := resolveBeehiveWorktree()
			if err != nil {
				return err
			}
			g := git.New(root)
			branch, err := g.CurrentBranch(cmd.Context())
			if err != nil {
				return err
			}
			if branch == "" || branch == "HEAD" || branch == "main" {
				return fmt.Errorf("task cycle requires a non-primary hive session branch (got %q)", branch)
			}
			b, err := os.ReadFile(filepath.Join(root, "submodules", subName, repo.PlanFile))
			if err != nil {
				return err
			}
			p, err := plan.Parse(string(b))
			if err != nil {
				return err
			}
			t := p.Find(args[1])
			if t == nil {
				return fmt.Errorf("task %q not found", args[1])
			}
			if t.Session != branch {
				return fmt.Errorf("task cycle requires a matching task claim: %s:%s session=%q, hive branch=%q", subName, t.ID, t.Session, branch)
			}
			checkoutDir, err := resolveSubmoduleWorktree()
			if err != nil {
				return err
			}
			rp, err := repo.Open(root)
			if err != nil {
				return err
			}
			// Match taskCheckCmd's layered policy, including its default fallback.
			pol := checkpolicy.Default()
			if eff, cerr := config.Resolve(root, subName); cerr == nil {
				if len(eff.CheckDeniedCommands) > 0 {
					pol.Denied = eff.CheckDeniedCommands
				}
				if eff.CheckSandbox != "" {
					pol.Sandbox = eff.CheckSandbox
				}
				if eff.CheckRequireSandbox != nil {
					pol.RequireSandbox = *eff.CheckRequireSandbox
				}
				pol.ReadPaths = eff.CheckReadPaths
			}
			lk, err := links.Load(filepath.Join(root, repo.LinksFile))
			if err != nil {
				return err
			}
			r := swarm.Runner{Repo: rp, Git: g, Session: branch, CheckPolicy: &pol, Links: lk}
			if err := r.RecordCycle(cmd.Context(), subName, t.ID, checkoutDir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s: successful cycle committed to the local hive branch; next cycle in 24h\n", subName, t.ID)
			return nil
		},
	}
}
