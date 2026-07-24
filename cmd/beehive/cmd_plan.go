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
	c.AddCommand(planValidateCmd())
	c.AddCommand(planLintCmd())
	return c
}

// planLintCmd reports definition-of-done and dependency hygiene for a submodule's
// PLAN.md: open (non-DONE) tasks that declared neither a Check nor check=none;
// dangling local dependencies (a dep naming no task in the plan — the class that
// wedged the jellyfin repin task); and self-defer counters at or past the
// MaxDefers bound. It is the deterministic replacement for a "periodic efficacy-
// evaluation pass": coverage and rot are a command away, not a swarm session.
// Read-only. Exits non-zero when any issue is found so a hook / CI can gate on it.
// See docs/dod-verification-spec.md.
func planLintCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "lint <submodule>",
		Short: "report missing DoD checks, dangling deps, and defer-cap breaches in a PLAN.md",
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
			b, err := os.ReadFile(filepath.Join(root, planRel))
			if err != nil {
				return err
			}
			p, err := plan.Parse(string(b))
			if err != nil {
				return fmt.Errorf("plan lint: %s does NOT parse: %w", planRel, err)
			}
			var issues []string
			// Dangling local deps — always an error (any writer).
			for id, deps := range p.DanglingDeps() {
				issues = append(issues, fmt.Sprintf("task %s: dangling dependency %s (names no task in this plan)", id, strings.Join(deps, ",")))
			}
			// Defer-cap breaches — a non-converging wait that should have escalated.
			for _, t := range p.Tasks {
				if t.Defers > plan.MaxDefers {
					issues = append(issues, fmt.Sprintf("task %s: deferred %d times (max %d) — escalate to NEEDS-HUMAN, do not defer again", t.ID, t.Defers, plan.MaxDefers))
				}
			}
			// Missing DoD declaration on open tasks. Grandfather DONE history (do not
			// retro-gate). This is the backlog the migration surfaces as coverage climbs;
			// a warning by default, an error only with --strict.
			var missing []string
			for _, t := range p.Tasks {
				if t.Status == plan.Done {
					continue
				}
				if !t.CheckDeclared() {
					missing = append(missing, t.ID)
				}
			}
			if len(missing) > 0 {
				msg := fmt.Sprintf("%d open task(s) declare neither a Check: nor check=none: %s", len(missing), strings.Join(missing, ", "))
				if strict {
					issues = append(issues, msg)
				} else {
					fmt.Fprintf(os.Stderr, "beehive: WARNING %s\n", msg)
				}
			}
			if len(issues) > 0 {
				for _, is := range issues {
					fmt.Fprintf(os.Stderr, "beehive: %s: %s\n", planRel, is)
				}
				return fmt.Errorf("plan lint: %d issue(s) in %s", len(issues), planRel)
			}
			fmt.Printf("beehive: %s DoD/dep hygiene OK\n", planRel)
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "treat open tasks missing a DoD declaration as errors (default: warn)")
	return cmd
}

// planValidateCmd parses a submodule's PLAN.md and reports whether it is
// well-formed, exiting non-zero when it is not. It exists because a work/review
// pass frequently needs to confirm "PLAN.md still parses" after editing it, and
// until now had no sanctioned affordance for that — the recurring audit found
// passes flailing through `beehive plan check/lint/validate` (none of which
// existed) and falling back to ad-hoc `go test internal/plan.Parse` runs. This
// is read-only: it never writes or commits. Beyond a bare Parse it round-trips
// the plan (Parse → String → Parse) and checks the re-render is stable and the
// task set is preserved, catching a plan that parses but would not survive a
// runner rewrite.
func planValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <submodule>",
		Short: "parse a submodule's PLAN.md and report whether it is well-formed",
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
			b, err := os.ReadFile(filepath.Join(root, planRel))
			if err != nil {
				return err
			}
			p, err := plan.Parse(string(b))
			if err != nil {
				return fmt.Errorf("plan validate: %s does NOT parse: %w", planRel, err)
			}
			// Round-trip: a plan that parses but re-renders to something that no
			// longer parses (or drops/duplicates tasks) would silently corrupt on
			// the next runner rewrite. Catch it here.
			p2, err := plan.Parse(p.String())
			if err != nil {
				return fmt.Errorf("plan validate: %s parses but does NOT round-trip: %w", planRel, err)
			}
			if len(p2.Tasks) != len(p.Tasks) {
				return fmt.Errorf("plan validate: %s round-trip changed task count %d -> %d", planRel, len(p.Tasks), len(p2.Tasks))
			}
			seen := map[string]bool{}
			for _, t := range p.Tasks {
				if seen[t.ID] {
					return fmt.Errorf("plan validate: %s has duplicate task id %q", planRel, t.ID)
				}
				seen[t.ID] = true
			}
			fmt.Printf("plan validate: %s OK — %d tasks parse and round-trip\n", planRel, len(p.Tasks))
			return nil
		},
	}
	return cmd
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
			// Convergence protocol (docs/main-convergence-protocol.md): plan archive
			// authors a commit DIRECTLY on the primary main, so it must merge the hive
			// remote into local main BEFORE authoring (and publish after), or it
			// manufactures the fork ff-only pullMain cannot heal. Mirrors
			// syncSubmodule's sync-before/publish-after call-site pattern exactly.
			rootGit := git.New(root)
			remote, _ := rootGit.Remote(cmd.Context())
			if err := rootGit.SyncMainFromRemote(cmd.Context(), remote); err != nil {
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
			if err := rootGit.PublishPrimaryMain(cmd.Context(), remote); err != nil {
				return err
			}
			fmt.Printf("plan archive: leaned %d DONE task(s) in %s: %s\n", len(archived), subName, strings.Join(ids, ", "))
			return nil
		},
	}
	return cmd
}
