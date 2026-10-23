package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/fsatomic"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// RecordCycle is the measurement-only handoff behind task cycle. Checks run on
// already tracked code: new code still goes through independent review. Nothing
// is published here; the ordinary runner publish path carries both artifacts.
func (r *Runner) RecordCycle(ctx context.Context, subName, taskID, checkoutDir string) error {
	if subName == "" || filepath.Base(subName) != subName || subName == "." || subName == ".." {
		return fmt.Errorf("invalid submodule %q", subName)
	}
	root := r.Repo.Root
	g := git.New(root)
	branch, err := g.CurrentBranch(ctx)
	if err != nil {
		return err
	}
	if r.Session == "" || branch != r.Session || branch == "main" || branch == "HEAD" {
		return fmt.Errorf("cycle requires the owning private hive session branch")
	}
	sub := repo.Submodule{Name: subName, Path: filepath.Join(root, "submodules", subName)}
	before, err := os.ReadFile(sub.PlanPath())
	if err != nil {
		return err
	}
	p, err := plan.Parse(string(before))
	if err != nil {
		return err
	}
	t := p.Find(taskID)
	if t == nil {
		return fmt.Errorf("task %q not found", taskID)
	}
	// Validate without mutating the real task until the check succeeds.
	trial := *t
	if err := trial.CompleteCycle(r.Session, r.now()); err != nil {
		return err
	}
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: sub, Task: *t}
	if hint := checkFrameworkGate(subName, root, t); hint != "" {
		return fmt.Errorf("%s", hint)
	}
	graph, err := selectt.LoadEdges(r.Repo)
	if err != nil {
		return err
	}
	for _, dep := range t.Deps {
		if strings.Contains(dep, ":") {
			if !graph.CrossDepSatisfied(subName, dep) {
				return fmt.Errorf("cycle blocked by dependency %s", dep)
			}
		} else if d := p.Find(dep); d == nil || d.Status != plan.Done {
			return fmt.Errorf("cycle blocked by dependency %s", dep)
		}
	}
	code := git.New(checkoutDir)
	sha, err := code.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	tracked, err := g.Run(ctx, "rev-parse", "HEAD:submodules/"+subName+"/repo")
	if err != nil || tracked != sha {
		return fmt.Errorf("cycle requires code at the tracked gitlink; new code requires review (tracked %s, checkout %s): %v", tracked, sha, err)
	}
	if hint, err := r.gateCleanCheckout(ctx, checkoutDir); err != nil || hint != "" {
		return fmt.Errorf("cycle requires a clean code checkout: %s: %v", hint, err)
	}
	outcome, err := r.runCheckIn(ctx, sel, t.Check(), checkoutDir)
	if err != nil || outcome.exitErr {
		return fmt.Errorf("cycle check FAILED (no successful cycle recorded): %v\n%s", err, outcome.out)
	}
	if hint, err := r.gateCleanCheckout(ctx, checkoutDir); err != nil || hint != "" {
		return fmt.Errorf("cycle check dirtied code; commit changes for review: %s: %v", hint, err)
	}
	afterSHA, err := code.Run(ctx, "rev-parse", "HEAD")
	if err != nil || afterSHA != sha {
		return fmt.Errorf("code HEAD changed during cycle check")
	}
	// Heartbeats may run during a long measurement. Serialize the final commit
	// with them and preserve unrelated plan updates, but refuse changed task intent.
	unlock, err := g.LockWorktree(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	after, err := os.ReadFile(sub.PlanPath())
	if err != nil {
		return err
	}
	fresh, err := plan.Parse(string(after))
	if err != nil {
		return err
	}
	ft := fresh.Find(taskID)
	if ft == nil {
		return fmt.Errorf("task removed during cycle check")
	}
	old := *t
	old.Heartbeat = ft.Heartbeat
	if old.Card() != ft.Card() {
		return fmt.Errorf("plan changed during cycle check; retry with the current claim")
	}
	p, t = fresh, ft
	if err := t.CompleteCycle(r.Session, r.now()); err != nil {
		return err
	}
	t.Commits, t.CommitsSet = nil, true
	docRel := filepath.Join("submodules", subName, "docs", "bee-"+taskID+"-"+taskID+".md")
	docPath := filepath.Join(root, docRel)
	doc, err := os.ReadFile(docPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := strings.TrimRight(plan.SetDocCommitsHeader(string(doc), nil), "\n")
	text += fmt.Sprintf("\n\n## Successful cycle %d\n%s\n\nCheck: `%s`\n\nCode: `%s`; exit 0; next run: `%s`.\n\n",
		t.Cycles, cycleMarker(t, sha), t.Check(), sha, t.NotBefore.Format(time.RFC3339))
	// Indented output cannot inject a receipt/header even if the check prints one.
	text += "    " + strings.ReplaceAll(strings.TrimRight(outcome.out, "\n"), "\n", "\n    ") + "\n"
	if err := os.MkdirAll(filepath.Dir(docPath), 0o755); err != nil {
		return err
	}
	if err := fsatomic.WriteFile(docPath, []byte(text), 0o644); err != nil {
		return err
	}
	if err := plan.WriteFile(sub.PlanPath(), p); err != nil {
		return err
	}
	return g.CommitPaths(ctx, fmt.Sprintf("plan: record cycle %d for %s\n\nBeehive: %s %s", t.Cycles, taskID, taskID, docRel),
		filepath.Join("submodules", subName, repo.PlanFile), docRel)
}

func cycleMarker(t *plan.Task, sha string) string {
	return fmt.Sprintf("<!-- Beehive-Cycle: %d %s %s %s %x -->", t.Cycles, t.CycleSession,
		t.LastCycle.UTC().Format(time.RFC3339), sha, sha256.Sum256([]byte(t.Check())))
}

func cycleReceiptSHA(doc string, t *plan.Task) string {
	for _, line := range strings.Split(doc, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 8 || fields[1] != "Beehive-Cycle:" {
			continue
		}
		sha := fields[5]
		if b, err := hex.DecodeString(sha); err == nil && len(b) == 20 && line == cycleMarker(t, sha) {
			return sha
		}
	}
	return ""
}

// cycleRecorded checks committed evidence, not an old on-disk doc or merely a
// future timestamp. It also works if publication takes longer than the interval.
func (r *Runner) cycleRecorded(sel *selectt.Selection, t *plan.Task) (bool, error) {
	return r.cycleRecordedAt(sel, t, "HEAD")
}

func (r *Runner) cycleRecordedAt(sel *selectt.Selection, t *plan.Task, ref string) (bool, error) {
	if t.Status != plan.TODO || t.Recurrence != 24*time.Hour || t.Cycles != sel.Task.Cycles+1 ||
		t.CycleSession != r.Session || r.Session == "" || t.Session != "" || !t.Heartbeat.IsZero() ||
		!t.LastCycle.After(sel.Task.LastCycle) || t.LastCycle.After(r.now()) ||
		!t.NotBefore.Equal(t.LastCycle.Add(t.Recurrence)) || t.Defers != sel.Task.Defers {
		return false, nil
	}
	g := git.New(r.Repo.Root)
	ctx := context.Background()
	text, err := g.Run(ctx, "show", ref+":submodules/"+sel.Submodule.Name+"/PLAN.md")
	if err != nil {
		return false, nil
	}
	p, err := plan.Parse(text)
	if err != nil {
		return false, err
	}
	ct := p.Find(t.ID)
	if ct == nil || ct.Card() != t.Card() || !ct.CommitsSet || len(ct.Commits) != 0 {
		return false, nil
	}
	doc, err := g.Run(ctx, "show", ref+":submodules/"+sel.Submodule.Name+"/docs/bee-"+t.ID+"-"+t.ID+".md")
	if err != nil {
		return false, nil
	}
	sha := cycleReceiptSHA(doc, ct)
	if ref == "HEAD" {
		tracked, err := g.Run(ctx, "rev-parse", "HEAD:submodules/"+sel.Submodule.Name+"/repo")
		if err != nil {
			return false, err
		}
		return sha != "" && sha == tracked, nil
	}
	return sha != "", nil
}
