package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spencerharmon/beehive/internal/audit"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

// auditCmd runs the deterministic session-metrics extraction and N-2 selection
// the session-audit-NNN series consumes. By default it is read-only and prints
// machine-readable TSV (the un-audited window, the per-task aggregate, and the
// delivered-only trend gauge). With --write it appends the window + trend to the
// append-only ledger under submodules/<sm>/docs/audit/, marking those sessions
// audited. It reads only the beehive layer (sessions/, PLAN.md, docs/audit/).
func auditCmd() *cobra.Command {
	var submodule string
	var write bool
	c := &cobra.Command{
		Use:   "audit",
		Short: "extract reproducible session metrics and select the next un-audited N-2 batch",
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
			// Submodule paths are already absolute (root-joined by Submodules()).
			sessDir := sm.SessionsDir()
			if _, err := os.Stat(sessDir); os.IsNotExist(err) {
				fmt.Printf("# no sessions for %s\n", sm.Name)
				return nil
			}
			sessions, census, perr := parseSessions(sessDir)
			if perr != nil {
				return perr
			}
			// Classify each stub's stream branch as CONFIRMED gone (resolves
			// nowhere) or not, so CorpusBroken can exempt permanently
			// un-finalizable stubs from its window/fraction math. Skipped
			// entirely when there are no stubs (no git call needed). Only the
			// coordination repo (root) is ever consulted here — never a
			// target's repo/ checkout.
			if census.StubCount() > 0 {
				g := git.New(root)
				remote, _ := g.Remote(cmd.Context())
				audit.ClassifyStubs(&census, func(branch string) string {
					return resolveSessionBranch(cmd.Context(), g, remote, branch)
				})
			}
			delivered, err := deliveredSet(sm)
			if err != nil {
				return err
			}
			auditDir := filepath.Join(sm.Path, "docs", "audit")
			led, err := audit.LoadLedger(auditDir)
			if err != nil {
				return err
			}
			pass := led.NextPass()
			window := audit.Window(sessions, led.Audited())
			// Aggregate over the FULL corpus so reruns/retries are true totals; the
			// trend is the delivered-only cost gauge at this pass.
			aggs := audit.Aggregate(sessions, delivered)
			trend := audit.ComputeTrend(aggs, pass)

			printCensus(census)
			printWindow(window)
			printAggregate(aggs)
			printTrend(trend)
			// Loud, byte-stable corpus-integrity alarm: an empty/sparse window over
			// a corpus of unfinalized stubs is a finalization defect, not a rest.
			// CorpusWarning yields "" (nothing printed) for a healthy corpus.
			if w := census.CorpusWarning(len(window) == 0); w != "" {
				fmt.Fprint(os.Stderr, w)
			}

			if write {
				led.AppendPass(window, trend)
				if err := led.Save(auditDir); err != nil {
					return err
				}
				fmt.Printf("# wrote pass %d: %d sessions, %d delivered tasks -> %s\n",
					pass, len(window), trend.DeliveredTasks, auditDir)
			}
			return nil
		},
	}
	c.Flags().StringVar(&submodule, "submodule", "", "submodule to audit (default: the only one)")
	c.Flags().BoolVar(&write, "write", false, "append this pass to the docs/audit ledger")
	return c
}

// resolveSessionBranch mirrors internal/swarm/sweep.go's private resolveRef
// closure EXACTLY (refs/heads/<branch> then refs/remotes/<remote>/<branch>,
// against the PRIMARY coordination repo g — never a target's repo/ checkout)
// so audit's GoneBranch classification and the finalize sweep never drift on
// what counts as a gone branch. Returns "" when branch resolves nowhere.
func resolveSessionBranch(ctx context.Context, g *git.Repo, remote, branch string) string {
	if _, err := g.RevParse(ctx, "refs/heads/"+branch); err == nil {
		return "refs/heads/" + branch
	}
	if remote != "" {
		if _, err := g.RevParse(ctx, "refs/remotes/"+remote+"/"+branch); err == nil {
			return "refs/remotes/" + remote + "/" + branch
		}
	}
	return ""
}

// parseSessions runs the corpus census over sessDir and returns the finalized
// (mineable) sessions plus the full census (finalized/stub/malformed counts).
// Genuinely malformed files are surfaced on stderr (never swallowed) but must not
// zero the pass. Unfinalized stubs are NOT errors — they are counted in the
// census and drive the corpus-broken warning downstream. A hard error is returned
// only when nothing at all is usable or recognisable (no sessions AND no stubs),
// or when the directory itself cannot be read.
func parseSessions(sessDir string) ([]audit.Session, audit.Census, error) {
	census, err := audit.ParseDirCensus(sessDir)
	if err != nil {
		return nil, audit.Census{}, err
	}
	if len(census.Errors) > 0 {
		joined := errors.Join(census.Errors...)
		fmt.Fprintf(os.Stderr, "beehive: audit: skipped %d malformed session(s):\n%v\n", len(census.Errors), joined)
		if census.Finalized() == 0 && census.StubCount() == 0 {
			// Nothing usable and nothing even recognisable as a stub — a real
			// failure, not a rest and not an unfinalized corpus.
			return nil, audit.Census{}, joined
		}
	}
	return census.Sessions, census, nil
}

// resolveSubmodule picks the named submodule, or the sole submodule when name is
// empty, erroring if the choice is ambiguous or unknown.
func resolveSubmodule(rp *repo.Repo, name string) (repo.Submodule, error) {
	subs, err := rp.Submodules()
	if err != nil {
		return repo.Submodule{}, err
	}
	if len(subs) == 0 {
		return repo.Submodule{}, fmt.Errorf("no submodules in repo")
	}
	if name == "" {
		if len(subs) != 1 {
			names := make([]string, len(subs))
			for i, s := range subs {
				names[i] = s.Name
			}
			return repo.Submodule{}, fmt.Errorf("--submodule required (have %s)", strings.Join(names, ", "))
		}
		return subs[0], nil
	}
	for _, s := range subs {
		if s.Name == name {
			return s, nil
		}
	}
	return repo.Submodule{}, fmt.Errorf("unknown submodule %q", name)
}

// deliveredSet reads PLAN.md and returns the DONE taskids. A missing PLAN.md
// (un-bootstrapped submodule) means nothing delivered yet.
func deliveredSet(sm repo.Submodule) (map[string]bool, error) {
	b, err := os.ReadFile(sm.PlanPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return nil, err
	}
	return audit.DeliveredFromPlan(p), nil
}

func printCensus(c audit.Census) {
	// The corpus census makes an empty audit window self-explaining:
	// empty-because-audited (a rest) versus empty-because-unfinalized (a defect).
	fmt.Println("# corpus census (finalized=mineable, stub=unfinalized, malformed=broken)")
	fmt.Println(strings.Join([]string{"total", "finalized", "stub", "malformed", "mineable_fraction"}, "\t"))
	fmt.Println(strings.Join([]string{
		strconv.Itoa(c.Total()), strconv.Itoa(c.Finalized()), strconv.Itoa(c.StubCount()),
		strconv.Itoa(c.ErrorCount()), strconv.FormatFloat(c.MineableFraction(), 'f', 3, 64),
	}, "\t"))
	if c.StubCount() > 0 {
		// gone is the ClassifyStubs verdict (see resolveSessionBranch): true
		// means the stub's stream branch is CONFIRMED to resolve nowhere, so
		// it is permanently un-finalizable, not a live or growing defect.
		// Every stub stays listed here — reporting only, nothing pruned — even
		// though CorpusBroken (below) exempts gone stubs from its math.
		fmt.Println("# unfinalized stubs (sid, branch, gone)")
		fmt.Println(strings.Join([]string{"sid", "branch", "gone"}, "\t"))
		for _, s := range c.Stubs {
			fmt.Println(strings.Join([]string{s.SID, s.Branch, strconv.FormatBool(s.GoneBranch)}, "\t"))
		}
	}
}

func printWindow(w []audit.Session) {
	fmt.Println("# window (N-2, un-audited)")
	fmt.Println(strings.Join([]string{
		"session_id", "epoch", "kind", "taskid", "bytes", "turns",
		"aborted", "lost_race", "completion_miss", "reconcile_loop",
	}, "\t"))
	for _, s := range w {
		h := s.Heuristics
		fmt.Println(strings.Join([]string{
			s.ID, strconv.FormatInt(s.Epoch, 10), s.Kind, s.TaskID,
			strconv.FormatInt(s.Bytes, 10), strconv.Itoa(s.Turns),
			strconv.FormatBool(h.Aborted), strconv.FormatBool(h.LostRace),
			strconv.FormatBool(h.CompletionMiss), strconv.FormatBool(h.ReconcileLoop),
		}, "\t"))
	}
}

func printAggregate(aggs []audit.TaskAgg) {
	fmt.Println("# per-task aggregate (full corpus)")
	fmt.Println(strings.Join([]string{"taskid", "reruns", "retries", "turns", "bytes", "delivered"}, "\t"))
	for _, a := range aggs {
		fmt.Println(strings.Join([]string{
			a.TaskID, strconv.Itoa(a.Reruns), strconv.Itoa(a.Retries),
			strconv.Itoa(a.Turns), strconv.FormatInt(a.Bytes, 10), strconv.FormatBool(a.Delivered),
		}, "\t"))
	}
}

func printTrend(t audit.Trend) {
	fmt.Println("# trend (delivered-only cost gauge)")
	fmt.Println(strings.Join([]string{
		"pass", "delivered_tasks", "turns", "bytes", "retries",
		"turns_per_task", "bytes_per_task", "retries_per_task",
	}, "\t"))
	fmt.Println(strings.Join([]string{
		strconv.Itoa(t.Pass), strconv.Itoa(t.DeliveredTasks),
		strconv.Itoa(t.Turns), strconv.FormatInt(t.Bytes, 10), strconv.Itoa(t.Retries),
		strconv.FormatFloat(t.TurnsPerTask(), 'f', 1, 64),
		strconv.FormatFloat(t.BytesPerTask(), 'f', 1, 64),
		strconv.FormatFloat(t.RetriesPerTask(), 'f', 2, 64),
	}, "\t"))
}
