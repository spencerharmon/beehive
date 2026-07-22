package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spencerharmon/beehive/internal/submod"
	"github.com/spf13/cobra"
)

func taskCmd() *cobra.Command {
	c := &cobra.Command{Use: "task", Short: "manage PLAN.md tasks"}
	c.AddCommand(taskHumanCmd())
	c.AddCommand(taskAddCmd())
	c.AddCommand(taskBlockCmd())
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
