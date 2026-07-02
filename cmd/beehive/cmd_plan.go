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
	"github.com/spf13/cobra"
)

// planCmd groups PLAN.md maintenance verbs. Today: `archive`, which leans a
// submodule's PLAN.md by moving completed tasks' Impl/Review narrative into
// docs/plan-archive/, keeping only the parseable task card on-plan.
func planCmd() *cobra.Command {
	c := &cobra.Command{Use: "plan", Short: "manage a submodule's PLAN.md"}
	c.AddCommand(planArchiveCmd())
	return c
}

// planArchiveCmd lifts DONE-task Impl/Review/Reconciled/Arbitration prose out of
// PLAN.md into docs/plan-archive/<id>.md, leaving the lean task card (header +
// description + Files/Doc/Accept). Every honeybee re-reads PLAN.md each session
// (it carries the live claim metadata), so trimming closed-task audit history
// keeps the re-read proportional to OPEN work. The parse is behaviour-preserving:
// the task set / statuses / deps / weights / claim metadata are unchanged; only
// DONE-narrative bytes move. Idempotent — a re-run on an already-lean plan is a
// no-op.
func planArchiveCmd() *cobra.Command {
	var submodule string
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "move DONE-task Impl/Review prose out of PLAN.md into docs/plan-archive/",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			res, err := archivePlan(cmd.Context(), root, submodule)
			if err != nil {
				return err
			}
			fmt.Print(res.summary())
			return nil
		},
	}
	cmd.Flags().StringVar(&submodule, "submodule", "", "submodule whose PLAN.md to lean (default: the only one)")
	return cmd
}

// archiveResult reports what an archivePlan run did, for the CLI summary and for
// tests to assert against.
type archiveResult struct {
	Submodule   string
	IDs         []string // archived task ids, in plan order
	BeforeBytes int
	AfterBytes  int
}

func (r archiveResult) summary() string {
	if len(r.IDs) == 0 {
		return fmt.Sprintf("# %s: nothing to archive (PLAN.md %d bytes)\n", r.Submodule, r.BeforeBytes)
	}
	return fmt.Sprintf("# %s: archived %d DONE card(s) [%s]; PLAN.md %d -> %d bytes\n",
		r.Submodule, len(r.IDs), strings.Join(r.IDs, ", "), r.BeforeBytes, r.AfterBytes)
}

// archivePlan is the testable core of `beehive plan archive`: it reads the
// submodule's PLAN.md, moves each DONE card's narrative to
// docs/plan-archive/<id>.md, writes the leaned PLAN.md, and commits the changed
// paths (PLAN.md + the archive docs) with a Beehive stamp. When nothing is DONE-
// with-narrative it makes no writes and no commit, so a re-run is a no-op.
func archivePlan(ctx context.Context, root, submodule string) (archiveResult, error) {
	rp, err := repo.Open(root)
	if err != nil {
		return archiveResult{}, err
	}
	sm, err := resolveSubmodule(rp, submodule)
	if err != nil {
		return archiveResult{}, err
	}
	res := archiveResult{Submodule: sm.Name}
	planRel := filepath.Join("submodules", sm.Name, repo.PlanFile)
	planPath := filepath.Join(root, planRel)
	before, err := os.ReadFile(planPath)
	if err != nil {
		return res, err
	}
	res.BeforeBytes = len(before)
	p, err := plan.Parse(string(before))
	if err != nil {
		return res, err
	}
	archived := p.Archive()
	if len(archived) == 0 {
		res.AfterBytes = len(before)
		return res, nil // nothing to do; leave the tree untouched
	}
	archiveRel := filepath.Join("submodules", sm.Name, "docs", "plan-archive")
	if err := os.MkdirAll(filepath.Join(root, archiveRel), 0o755); err != nil {
		return res, err
	}
	paths := []string{planRel}
	for _, a := range archived {
		rel := filepath.Join(archiveRel, a.ID+".md")
		if err := writeArchiveDoc(filepath.Join(root, rel), a); err != nil {
			return res, err
		}
		paths = append(paths, rel)
		res.IDs = append(res.IDs, a.ID)
	}
	after := p.String()
	if err := os.WriteFile(planPath, []byte(after), 0o644); err != nil {
		return res, err
	}
	res.AfterBytes = len(after)
	msg := fmt.Sprintf("plan: archive %d DONE card(s): %s\n\nBeehive: plan-archive plan\nArchived: %s",
		len(res.IDs), strings.Join(res.IDs, ", "), strings.Join(res.IDs, ", "))
	if err := git.New(root).CommitPaths(ctx, msg, paths...); err != nil && err != git.ErrNothing {
		return res, err
	}
	return res, nil
}

const archivePreamble = "Impl/Review/Reconciled/Arbitration prose lifted out of the lean PLAN.md task\n" +
	"card by `beehive plan archive`. The authoritative record is the task's change\n" +
	"doc under docs/; this file preserves the plan-embedded narrative.\n"

// writeArchiveDoc writes (creating) or appends to a task's plan-archive doc. It
// appends when the file already exists so a task that is re-opened and re-DONE
// accumulates its narrative rather than clobbering earlier history. The content
// is deterministic (no timestamps) so archiving the same input reproduces the
// same bytes.
func writeArchiveDoc(path string, a plan.Archived) error {
	block := a.Header + "\n\n" + strings.Join(a.Narrative, "\n") + "\n"
	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		fresh := "# " + a.ID + ": archived PLAN.md narrative\n\n" + archivePreamble + "\n" + block
		return os.WriteFile(path, []byte(fresh), 0o644)
	}
	content := string(existing)
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + block
	return os.WriteFile(path, []byte(content), 0o644)
}
