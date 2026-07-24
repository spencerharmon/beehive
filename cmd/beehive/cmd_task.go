package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/checkpolicy"
	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spencerharmon/beehive/internal/submod"
	"github.com/spencerharmon/beehive/internal/swarm"
	"github.com/spf13/cobra"
)

func taskCmd() *cobra.Command {
	c := &cobra.Command{Use: "task", Short: "manage PLAN.md tasks"}
	c.AddCommand(taskHumanCmd())
	c.AddCommand(taskAddCmd())
	c.AddCommand(taskBlockCmd())
	c.AddCommand(taskCheckCmd())
	c.AddCommand(taskDeferCmd())
	c.AddCommand(taskReopenCmd())
	c.AddCommand(taskSetCheckCmd())
	c.AddCommand(taskRetargetDepCmd())
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

// taskAddCmd files a brand-new TODO task (with its design doc) into a
// submodule's PLAN.md through the primary-main convergence protocol — the
// first-class way a WORK pass that discovers a missing prerequisite creates the
// real task for it instead of faking a dangling dep or farming it out to a human.
// The target submodule may be THIS one or a linked one; the honeybee then points
// its own task at the new one with `beehive task block`.
func taskAddCmd() *cobra.Command {
	var body, bodyFile, doc, docFile, deps string
	var check, verifyAfterMerge, notBefore string
	var checkNone bool
	var weight int
	cmd := &cobra.Command{
		Use:   "add <submodule> <task-id>",
		Short: "file a new TODO task (with its design doc) in a submodule's PLAN.md",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			// Same convergence discipline as `task human`: this authors a PLAN.md
			// (+ doc) commit DIRECTLY on primary main, so merge the hive remote into
			// local main BEFORE authoring and publish after, or it manufactures the
			// fork ff-only pullMain cannot heal.
			rootGit := git.New(root)
			remote, _ := rootGit.Remote(cmd.Context())
			if err := rootGit.SyncMainFromRemote(cmd.Context(), remote); err != nil {
				return err
			}
			subName, err := taskSubmoduleName(args[0])
			if err != nil {
				return err
			}
			id := args[1]
			if !plan.ValidID(id) {
				return fmt.Errorf("invalid task id %q", id)
			}
			bodyText, err := textArg("--body", body, "--body-file", bodyFile)
			if err != nil {
				return err
			}
			docText, err := textArg("--doc", doc, "--doc-file", docFile)
			if err != nil {
				return fmt.Errorf("a design doc is required for every new task: %w", err)
			}
			var depList []string
			if strings.TrimSpace(deps) != "" {
				depList = strings.Split(deps, ",")
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
			t, err := plan.NewTask(id, depList, weight, linesOf(bodyText))
			if err != nil {
				return err
			}
			// Definition-of-done: attach the machine check(s) or the justified
			// absence. A live-effect task must carry a Check (or Verify-After-Merge)
			// or an explicit check=none with an honest reason in the body; the runner
			// gates DONE on it (docs/dod-verification-spec.md).
			if checkNone {
				if check != "" || verifyAfterMerge != "" {
					return fmt.Errorf("--check-none is mutually exclusive with --check/--verify-after-merge")
				}
				t.CheckNone = true
			}
			if check != "" {
				if err := t.SetCheck(check); err != nil {
					return err
				}
			}
			if verifyAfterMerge != "" {
				if err := t.SetVerifyAfterMerge(verifyAfterMerge); err != nil {
					return err
				}
			}
			if notBefore != "" {
				nb, err := parseUntil(notBefore, time.Now().UTC())
				if err != nil {
					return fmt.Errorf("--not-before: %w", err)
				}
				t.NotBefore = nb
			}
			if err := p.AddTask(t); err != nil {
				return err
			}
			docRel := filepath.Join("submodules", subName, "docs", "tasks", id+".md")
			docPath := filepath.Join(root, docRel)
			if err := os.MkdirAll(filepath.Dir(docPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(docPath, []byte(docText), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
				return err
			}
			msg := fmt.Sprintf("plan: file task %s in %s\n\nBeehive: %s %s", id, subName, id, docRel)
			if err := rootGit.CommitPaths(cmd.Context(), msg, planRel, docRel); err != nil && err != git.ErrNothing {
				return err
			}
			if err := rootGit.PublishPrimaryMain(cmd.Context(), remote); err != nil {
				return err
			}
			fmt.Printf("filed %s %s [TODO] (doc %s)\n", subName, id, docRel)
			return nil
		},
	}
	cmd.Flags().StringVar(&body, "body", "", "task card body / description")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "read task card body from file")
	cmd.Flags().StringVar(&doc, "doc", "", "design doc content (required)")
	cmd.Flags().StringVar(&docFile, "doc-file", "", "read design doc content from file (required)")
	cmd.Flags().StringVar(&deps, "deps", "", "comma-separated deps for the NEW task (bare id or <submodule>:<taskid>)")
	cmd.Flags().StringVar(&check, "check", "", "definition-of-done command (its exit 0 is the machine DoD the runner gates DONE on)")
	cmd.Flags().StringVar(&verifyAfterMerge, "verify-after-merge", "", "post-merge DoD command (effect exists only after merge; the runner auto-spawns a successor check task carrying it at DONE)")
	cmd.Flags().BoolVar(&checkNone, "check-none", false, "declare this task has NO machine-checkable DoD (justify in --body); mutually exclusive with --check")
	cmd.Flags().StringVar(&notBefore, "not-before", "", "hold the task out of selection until this time (RFC3339 or a duration like 30m/2h from now)")
	cmd.Flags().IntVar(&weight, "weight", 1, "selection weight (default 1)")
	return cmd
}

// taskBlockCmd adds a dependency to an existing TODO task and releases its claim,
// so a WORK pass that discovers it depends on a task it just filed can link that
// prerequisite and cleanly yield: the selector then holds this task until the dep
// is DONE. For a cross-submodule dep (`<submodule>:<taskid>`) it verifies the
// target task exists, rejects a dependency that would form a wait cycle, and
// registers the authorizing submodule link if it is missing — all committed and
// published to primary main together.
func taskBlockCmd() *cobra.Command {
	var on string
	cmd := &cobra.Command{
		Use:   "block <submodule> <task-id> --on <dep>",
		Short: "add a dependency to a TODO task (linking a cross-submodule prerequisite) and release its claim",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			rootGit := git.New(root)
			remote, _ := rootGit.Remote(cmd.Context())
			if err := rootGit.SyncMainFromRemote(cmd.Context(), remote); err != nil {
				return err
			}
			subName, err := taskSubmoduleName(args[0])
			if err != nil {
				return err
			}
			id := args[1]
			dep := strings.TrimSpace(on)
			if !plan.ValidDep(dep) {
				return fmt.Errorf("--on must be a valid dep: <taskid> or <submodule>:<taskid> (got %q)", dep)
			}
			if dep == "" {
				return fmt.Errorf("--on is required (the dependency to add)")
			}
			rp, err := repo.Open(root)
			if err != nil {
				return err
			}
			var linkRels []string
			if osm, _, isCross := strings.Cut(dep, ":"); isCross {
				g, err := selectt.LoadEdges(rp)
				if err != nil {
					return err
				}
				if _, exists := g.TaskStatus(dep); !exists {
					return fmt.Errorf("dependency task %q does not exist yet — file it first with `beehive task add %s <task-id> ...`", dep, osm)
				}
				node := subName + ":" + id
				edges := append(append([]links.Edge{}, g.Edges...), links.Edge{From: node, To: dep})
				if cyc := links.Cycle(edges); cyc != nil {
					return fmt.Errorf("adding dep %s to %s would form a wait cycle: %s", dep, node, strings.Join(cyc, " -> "))
				}
				if !g.LinkedTo(subName, osm) {
					if err := submod.LinkSubmodules(root, subName, osm); err != nil {
						return err
					}
					linkRels = append(linkRels,
						filepath.Join("submodules", subName, repo.LinksFile),
						filepath.Join("submodules", osm, repo.LinksFile))
				}
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
			t := p.Find(id)
			if t == nil {
				return fmt.Errorf("task %q not found in %s", id, planRel)
			}
			if t.Status != plan.StatusTODO {
				return fmt.Errorf("task block applies only to a TODO task; %s is %s", id, t.Status)
			}
			if _, err := t.AddDep(dep); err != nil {
				return err
			}
			// Release the claim so the runner reselects; the selector will hold the
			// task out of the ready set until the new dep is DONE.
			t.Session = ""
			t.Heartbeat = time.Time{}
			if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
				return err
			}
			msg := fmt.Sprintf("plan: block %s on %s\n\nBeehive: %s plan", id, dep, id)
			paths := append([]string{planRel}, linkRels...)
			if err := rootGit.CommitPaths(cmd.Context(), msg, paths...); err != nil && err != git.ErrNothing {
				return err
			}
			if err := rootGit.PublishPrimaryMain(cmd.Context(), remote); err != nil {
				return err
			}
			fmt.Printf("%s %s now depends on %s\n", subName, id, dep)
			return nil
		},
	}
	cmd.Flags().StringVar(&on, "on", "", "dependency to add: <taskid> (local) or <submodule>:<taskid> (cross-submodule)")
	return cmd
}

// textArg resolves a value provided either inline or via a file flag, rejecting
// both-set and neither-set. Unlike humanReason it preserves the content verbatim
// (multi-line task bodies / docs), only trimming a trailing newline.
func textArg(inlineName, inline, fileName, file string) (string, error) {
	if inline != "" && file != "" {
		return "", fmt.Errorf("use %s or %s, not both", inlineName, fileName)
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		inline = string(b)
	}
	if strings.TrimSpace(inline) == "" {
		return "", fmt.Errorf("%s or %s is required", inlineName, fileName)
	}
	return inline, nil
}

// linesOf splits body/doc text into the []string lines the plan layer stores,
// dropping a single trailing newline so a file that ends in "\n" does not add a
// spurious blank body line.
func linesOf(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// taskCheckCmd runs a task's definition-of-done command (`Check:`) ad hoc and
// reports its exit status — the same command the runner's handoff gate runs when
// the task enters DONE. It lets an author debug a check and lets a reviewer run
// the check without driving a whole pass (the review contract requires the
// reviewer to actually execute the check, not merely confirm a Check: line
// exists — docs/dod-verification-spec.md). Read-only: it never writes or commits.
// Exits non-zero when the check fails or the task declares check=none.
func taskCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check <submodule> <task-id>",
		Short: "run a task's Check: command ad hoc and report its exit status",
		Args:  cobra.ExactArgs(2),
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
				return err
			}
			t := p.Find(args[1])
			if t == nil {
				return fmt.Errorf("task %q not found in %s", args[1], planRel)
			}
			if t.CheckNone {
				return fmt.Errorf("task %s declares check=none — it has no machine-checkable definition of done to run", t.ID)
			}
			check := t.Check()
			if check == "" {
				return fmt.Errorf("task %s has no Check: command (and did not declare check=none)", t.ID)
			}
			// Run in the submodule's repo checkout, the natural cwd for a check that
			// inspects the change (build artifacts, rendered manifests, endpoints).
			runDir := filepath.Join(root, "submodules", subName, "repo")
			if _, statErr := os.Stat(runDir); statErr != nil {
				runDir = root
			}
			// Confine identically to the runner's gate: build the same check policy from
			// layered config, enforce the command allowlist, and run under the same
			// filesystem sandbox scoped to this submodule + its linked submodules.
			pol := checkpolicy.Default()
			if eff, cerr := config.Resolve(root, subName); cerr == nil {
				if len(eff.CheckAllowedCommands) > 0 {
					pol.Allowed = eff.CheckAllowedCommands
				}
				if eff.CheckSandbox != "" {
					pol.Sandbox = eff.CheckSandbox
				}
				if eff.CheckRequireSandbox != nil {
					pol.RequireSandbox = *eff.CheckRequireSandbox
				}
				pol.ReadPaths = eff.CheckReadPaths
			}
			if verr := pol.Validate(check); verr != nil {
				return fmt.Errorf("check for %s:%s is REJECTED by the check-command policy: %w", subName, t.ID, verr)
			}
			rp, rerr := repo.Open(root)
			if rerr != nil {
				return rerr
			}
			lk, _ := links.Load(filepath.Join(root, repo.LinksFile))
			sub := repo.Submodule{Name: subName, Path: filepath.Join(root, "submodules", subName)}
			rw, ro := swarm.CheckBinds(cmd.Context(), rp, lk, sub, runDir, root, pol.ReadPaths)
			pl, aerr := pol.Argv(check, runDir, rw, ro)
			if aerr != nil {
				return aerr
			}
			if pl.Note != "" {
				fmt.Fprintf(os.Stderr, "beehive: %s\n", pl.Note)
			}
			ex := exec.CommandContext(cmd.Context(), pl.Name, pl.Args...)
			ex.Dir = runDir
			ex.Stdout = os.Stdout
			ex.Stderr = os.Stderr
			sbx := "unconfined"
			if pl.Sandboxed {
				sbx = "sandboxed"
			}
			fmt.Fprintf(os.Stderr, "beehive: running check for %s:%s in %s (%s)\n  %s\n", subName, t.ID, runDir, sbx, check)
			if err := ex.Run(); err != nil {
				return fmt.Errorf("check FAILED for %s:%s: %w", subName, t.ID, err)
			}
			fmt.Fprintf(os.Stderr, "beehive: check PASSED for %s:%s\n", subName, t.ID)
			return nil
		},
	}
	return cmd
}

// taskDeferCmd records a convergence-wait self-defer on a TODO task: it sets
// not_before to --until, increments the bounded defer counter, and releases the
// claim so the selector holds the task until then. It is the sanctioned atomic
// form of the TODO->TODO self-defer transition ("did the work, the world has not
// converged, re-check after T") — bounded by plan.MaxDefers so a non-converging
// wait escalates to NEEDS-HUMAN rather than spinning forever. Authors the PLAN.md
// commit directly on primary main, so it syncs the hive remote before and
// publishes after, exactly like `task human`/`task block`.
func taskDeferCmd() *cobra.Command {
	var until, reason string
	cmd := &cobra.Command{
		Use:   "defer <submodule> <task-id> --until <time>",
		Short: "self-defer a TODO task until --until (convergence wait); releases its claim",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			rootGit := git.New(root)
			remote, _ := rootGit.Remote(cmd.Context())
			if err := rootGit.SyncMainFromRemote(cmd.Context(), remote); err != nil {
				return err
			}
			subName, err := taskSubmoduleName(args[0])
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			if strings.TrimSpace(until) == "" {
				return fmt.Errorf("--until is required (RFC3339 or a duration like 30m/2h from now)")
			}
			nb, err := parseUntil(until, now)
			if err != nil {
				return fmt.Errorf("--until: %w", err)
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
			if err := t.Defer(nb, now); err != nil {
				return err
			}
			// Release the claim so the runner reselects once not_before elapses.
			t.Session = ""
			t.Heartbeat = time.Time{}
			if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
				return err
			}
			msg := fmt.Sprintf("plan: defer %s until %s (defer %d/%d)\n\nBeehive: %s plan", t.ID, nb.Format(time.RFC3339), t.Defers, plan.MaxDefers, t.ID)
			if r := strings.Join(strings.Fields(reason), " "); r != "" {
				msg += "\nReason: " + r
			}
			if err := rootGit.CommitPaths(cmd.Context(), msg, planRel); err != nil && err != git.ErrNothing {
				return err
			}
			if err := rootGit.PublishPrimaryMain(cmd.Context(), remote); err != nil {
				return err
			}
			fmt.Printf("%s %s deferred until %s (defer %d/%d)\n", subName, t.ID, nb.Format(time.RFC3339), t.Defers, plan.MaxDefers)
			return nil
		},
	}
	cmd.Flags().StringVar(&until, "until", "", "when the task becomes selectable again (RFC3339 or a duration like 30m/2h from now)")
	cmd.Flags().StringVar(&reason, "reason", "", "why the task is being deferred (recorded in the commit)")
	return cmd
}

// parseUntil resolves a not_before / defer target given either an absolute
// RFC3339 timestamp or a relative Go duration (e.g. "30m", "2h") added to now.
// The result is always UTC.
func parseUntil(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(d).UTC(), nil
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%q is neither an RFC3339 time nor a duration (e.g. 30m, 2h)", s)
}

// mutatePlanTask runs the sanctioned operator-directed PLAN.md mutation protocol
// (identical to task defer/block/human): sync primary main from the hive remote,
// parse the submodule's plan, apply `mut` (which returns the commit subject),
// write, commit the plan path, and publish primary main. It keeps every operator
// task verb converging through the same non-racing path.
func mutatePlanTask(cmd *cobra.Command, subArg string, mut func(*plan.Plan) (string, error)) error {
	root, err := findRoot()
	if err != nil {
		return err
	}
	rootGit := git.New(root)
	remote, _ := rootGit.Remote(cmd.Context())
	if err := rootGit.SyncMainFromRemote(cmd.Context(), remote); err != nil {
		return err
	}
	subName, err := taskSubmoduleName(subArg)
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
	subject, err := mut(p)
	if err != nil {
		return err
	}
	if err := os.WriteFile(planPath, []byte(p.String()), 0o644); err != nil {
		return err
	}
	if err := rootGit.CommitPaths(cmd.Context(), subject, planRel); err != nil && err != git.ErrNothing {
		return err
	}
	return rootGit.PublishPrimaryMain(cmd.Context(), remote)
}

// taskReopenCmd returns a terminal task (a false-DONE, a stuck NEEDS-REVIEW, an
// escalated NEEDS-HUMAN) to TODO so the swarm re-drives it — the sanctioned way to
// reopen a task whose recorded DONE does not match reality (the class the DoD
// contract exists to catch). It clears the stale claim/attempts/stamps and records
// the operator's reason. It syncs the hive remote before and publishes after,
// exactly like `task defer`/`task block`.
func taskReopenCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "reopen <submodule> <task-id> --reason <why>",
		Short: "return a terminal task (e.g. a false-DONE) to TODO so the swarm re-drives it",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required (why is this task being reopened?)")
			}
			return mutatePlanTask(cmd, args[0], func(p *plan.Plan) (string, error) {
				if err := p.Reopen(args[1], reason); err != nil {
					return "", err
				}
				fmt.Printf("reopened %s to TODO\n", args[1])
				return fmt.Sprintf("plan: reopen %s to TODO\n\nReason: %s\nBeehive: %s plan", args[1], strings.Join(strings.Fields(reason), " "), args[1]), nil
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the task is being reopened (recorded in the commit + a body note)")
	return cmd
}

// taskSetCheckCmd attaches or replaces the definition-of-done on an EXISTING task
// (unlike `task add --check`, which only applies at creation) — used to backfill a
// real check onto a task that lacked one, or to correct a weak/wrong check. Exactly
// one of --check / --verify-after-merge / --check-none.
func taskSetCheckCmd() *cobra.Command {
	var check, vam string
	var checkNone bool
	cmd := &cobra.Command{
		Use:   "set-check <submodule> <task-id> (--check <cmd> | --verify-after-merge <cmd> | --check-none)",
		Short: "attach or replace a task's definition-of-done Check: (backfill/correct a DoD)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			if strings.TrimSpace(check) != "" {
				n++
			}
			if strings.TrimSpace(vam) != "" {
				n++
			}
			if checkNone {
				n++
			}
			if n != 1 {
				return fmt.Errorf("exactly one of --check, --verify-after-merge, --check-none is required")
			}
			return mutatePlanTask(cmd, args[0], func(p *plan.Plan) (string, error) {
				t := p.Find(args[1])
				if t == nil {
					return "", fmt.Errorf("task %q not found", args[1])
				}
				var subject string
				switch {
				case checkNone:
					t.SetCheckNone()
					subject = "set check=none on " + t.ID
				case strings.TrimSpace(vam) != "":
					if err := t.ReplaceVerifyAfterMerge(vam); err != nil {
						return "", err
					}
					subject = "set Verify-After-Merge on " + t.ID
				default:
					if err := t.ReplaceCheck(check); err != nil {
						return "", err
					}
					subject = "set Check on " + t.ID
				}
				fmt.Printf("%s: %s\n", t.ID, subject)
				return "plan: " + subject + "\n\nBeehive: " + t.ID + " plan", nil
			})
		},
	}
	cmd.Flags().StringVar(&check, "check", "", "definition-of-done command (its exit 0 IS done; the gate enforces it)")
	cmd.Flags().StringVar(&vam, "verify-after-merge", "", "post-merge DoD command (effect exists only after merge; the runner auto-spawns a successor check task carrying it at DONE)")
	cmd.Flags().BoolVar(&checkNone, "check-none", false, "declare this task has NO machine-checkable DoD (justify in the body)")
	return cmd
}

// taskRetargetDepCmd fixes a wrong/dangling dependency on a task — e.g. a
// cross-submodule dep naming a task id that does not exist (which the selector
// holds forever). Replaces --from with --to.
func taskRetargetDepCmd() *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "retarget-dep <submodule> <task-id> --from <dep> --to <dep>",
		Short: "replace a wrong/dangling dependency on a task with the correct one",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
				return fmt.Errorf("both --from and --to are required")
			}
			return mutatePlanTask(cmd, args[0], func(p *plan.Plan) (string, error) {
				t := p.Find(args[1])
				if t == nil {
					return "", fmt.Errorf("task %q not found", args[1])
				}
				if err := t.RetargetDep(from, to); err != nil {
					return "", err
				}
				fmt.Printf("%s: dep %s -> %s\n", t.ID, from, to)
				return fmt.Sprintf("plan: retarget %s dep %s -> %s\n\nBeehive: %s plan", t.ID, strings.TrimSpace(from), strings.TrimSpace(to), t.ID), nil
			})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the current (wrong/dangling) dependency to remove")
	cmd.Flags().StringVar(&to, "to", "", "the correct dependency to add in its place")
	return cmd
}
