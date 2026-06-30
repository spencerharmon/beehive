package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spencerharmon/beehive/internal/audit"
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
			sessions, perr := audit.ParseDir(sessDir)
			if perr != nil {
				// ParseDir is per-file resilient: a single unparsable/odd
				// transcript name must not zero an entire pass. Surface the
				// failures (never swallow them) but proceed with the sessions
				// that did parse; only a directory that yields nothing usable is
				// a hard error.
				fmt.Fprintf(os.Stderr, "beehive: audit: skipped unparsable session(s):\n%v\n", perr)
				if len(sessions) == 0 {
					return perr
				}
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

			printWindow(window)
			printAggregate(aggs)
			printTrend(trend)

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
