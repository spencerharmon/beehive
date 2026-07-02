package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

// planCmd groups deterministic PLAN.md maintenance verbs. Reconcile/bootstrap own
// the task SET; these verbs only reshape how existing tasks are stored.
func planCmd() *cobra.Command {
	c := &cobra.Command{Use: "plan", Short: "maintain a submodule's PLAN.md"}
	c.AddCommand(planArchiveCmd())
	return c
}

// planArchiveCmd moves the post-hoc Impl/Review/Reconciled/Arbitration narrative
// out of every DONE task in a submodule's PLAN.md into
// submodules/<sm>/docs/plan-archive/<id>.md, leaving the lean task card (the
// header + description + Files:/Doc:/Accept:). This keeps PLAN.md proportional to
// OPEN work — it is the ~130 KB file every honeybee re-reads each session — while
// the parsed task set/statuses/deps/weights/claims are provably preserved. OPEN
// tasks and all claim metadata are never touched; a re-run is a no-op. The change
// is committed (scoped), so it publishes via the normal merge-to-main path rather
// than an in-place rewrite of a live tree racing the runner's heartbeat.
func planArchiveCmd() *cobra.Command {
	var submodule string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "move DONE-task Impl/Review narrative out of PLAN.md into docs/plan-archive/<id>.md",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			rp, err := repo.Open(root)
			if err != nil {
				return err
			}
			sm, err := resolveSubmodule(rp, submodule)
			if err != nil {
				return err
			}
			res, err := archivePlan(sm, dryRun)
			if err != nil {
				return err
			}
			if len(res.Archived) == 0 {
				fmt.Printf("# %s: nothing to archive (PLAN.md already lean)\n", sm.Name)
				return nil
			}
			fmt.Printf("# %s: archived %d DONE narrative(s); PLAN.md %d -> %d bytes\n",
				sm.Name, len(res.Archived), res.BytesFrom, res.BytesTo)
			for _, id := range res.Archived {
				fmt.Printf("#   %s -> submodules/%s/docs/plan-archive/%s.md\n", id, sm.Name, id)
			}
			if dryRun {
				fmt.Println("# dry-run: no files written, no commit")
				return nil
			}
			msg := fmt.Sprintf("plan: archive %d DONE narrative(s) from %s PLAN.md", len(res.Archived), sm.Name)
			if err := git.New(root).CommitPaths(cmd.Context(), msg, res.Paths...); err != nil && err != git.ErrNothing {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&submodule, "submodule", "", "submodule to archive (default: the only one)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be archived without writing or committing")
	return cmd
}

// archiveResult summarizes one plan-archive pass.
type archiveResult struct {
	Archived  []string // task ids whose narrative was moved (sorted)
	Paths     []string // repo-relative paths written, for a scoped commit
	BytesFrom int      // PLAN.md size before
	BytesTo   int      // PLAN.md size after (== BytesFrom on a no-op)
}

// archivePlan performs the archive as pure file I/O (no git), so the CLI can
// commit the returned Paths and tests can exercise it hermetically. It leans a
// COPY and verifies the parser round-trips (plan.SameMeta) BEFORE writing, so a
// behavior-changing archive aborts without leaving a half-written tree. On dryRun
// it computes the result and writes nothing. A plan with no fat DONE task returns
// an empty Archived and touches no file (a genuine no-op, which is what makes a
// re-run idempotent).
func archivePlan(sm repo.Submodule, dryRun bool) (archiveResult, error) {
	var res archiveResult
	planAbs := sm.PlanPath()
	raw, err := os.ReadFile(planAbs)
	if err != nil {
		return res, err
	}
	res.BytesFrom, res.BytesTo = len(raw), len(raw)

	orig, err := plan.Parse(string(raw))
	if err != nil {
		return res, err
	}
	leaned, err := plan.Parse(string(raw))
	if err != nil {
		return res, err
	}
	narratives := leaned.LeanDone()
	if len(narratives) == 0 {
		return res, nil // nothing to archive: no writes, no-op
	}

	leanedText := leaned.String()
	reparsed, err := plan.Parse(leanedText)
	if err != nil {
		return res, fmt.Errorf("leaned plan no longer parses: %w", err)
	}
	if err := plan.SameMeta(orig, reparsed); err != nil {
		return res, fmt.Errorf("archive would change parsed tasks: %w", err)
	}
	res.BytesTo = len(leanedText)

	ids := make([]string, 0, len(narratives))
	for id := range narratives {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	archiveDirAbs := filepath.Join(sm.Path, "docs", "plan-archive")
	for _, id := range ids {
		res.Archived = append(res.Archived, id)
		res.Paths = append(res.Paths, filepath.Join("submodules", sm.Name, "docs", "plan-archive", id+".md"))
		if dryRun {
			continue
		}
		if err := os.MkdirAll(archiveDirAbs, 0o755); err != nil {
			return res, err
		}
		if err := writeArchive(filepath.Join(archiveDirAbs, id+".md"), id, narratives[id]); err != nil {
			return res, err
		}
	}
	res.Paths = append(res.Paths, filepath.Join("submodules", sm.Name, repo.PlanFile))
	if !dryRun {
		if err := os.WriteFile(planAbs, []byte(leanedText), 0o644); err != nil {
			return res, err
		}
	}
	return res, nil
}

// writeArchive persists a DONE task's narrative to its plan-archive doc. A fresh
// doc gets a self-describing header; if the doc already exists (a task re-opened
// and re-closed, accreting new narrative) the new prose is appended below a rule
// so nothing is lost. This never runs on a steady-state re-archive because
// LeanDone only yields narrative still present in the plan body.
func writeArchive(path, id string, narrative []string) error {
	body := strings.Join(narrative, "\n") + "\n"
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		doc := strings.TrimRight(string(existing), "\n") + "\n\n---\n\n" + body
		return os.WriteFile(path, []byte(doc), 0o644)
	case os.IsNotExist(err):
		var b strings.Builder
		b.WriteString("# " + id + " — archived PLAN.md narrative\n\n")
		b.WriteString("<!-- Beehive-plan-archive: " + id + " -->\n\n")
		b.WriteString("Moved out of PLAN.md by `beehive plan archive` to keep the plan proportional\n")
		b.WriteString("to open work. The task's change doc is the authoritative record; this file\n")
		b.WriteString("preserves the plan-embedded Impl/Review/Reconciled/Arbitration prose verbatim.\n\n")
		b.WriteString(body)
		return os.WriteFile(path, []byte(b.String()), 0o644)
	default:
		return err
	}
}
