package audit

import (
	"sort"

	"github.com/spencerharmon/beehive/internal/plan"
)

// TaskAgg is the per-task rollup across every session sharing a TaskID. Reruns is
// the session count (links-graph-enforcement -> 3, reconcile -> 2 in the current
// corpus); Retries is Reruns-1 (the wasted re-attempts). Turns and Bytes sum the
// task's sessions. Delivered is true only when the task reached DONE on main.
type TaskAgg struct {
	TaskID    string
	Sessions  []string // session IDs, epoch-sorted
	Reruns    int      // == len(Sessions)
	Retries   int      // Reruns - 1
	Turns     int      // summed across sessions
	Bytes     int64    // summed across sessions
	Delivered bool
}

// Aggregate groups sessions by TaskID and marks each task delivered per the
// delivered set (taskid -> reached DONE). Sessions are assumed epoch-sorted (as
// ParseDir returns them); the result is sorted by TaskID for determinism.
func Aggregate(sessions []Session, delivered map[string]bool) []TaskAgg {
	idx := map[string]*TaskAgg{}
	var order []string
	for _, s := range sessions {
		a := idx[s.TaskID]
		if a == nil {
			a = &TaskAgg{TaskID: s.TaskID, Delivered: delivered[s.TaskID]}
			idx[s.TaskID] = a
			order = append(order, s.TaskID)
		}
		a.Sessions = append(a.Sessions, s.ID)
		a.Turns += s.Turns
		a.Bytes += s.Bytes
	}
	out := make([]TaskAgg, 0, len(order))
	for _, id := range order {
		a := idx[id]
		a.Reruns = len(a.Sessions)
		a.Retries = a.Reruns - 1
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

// Trend is the tracked metric for one audit pass: the cost carried by DELIVERED
// tasks only, so successive passes can show whether the swarm is getting cheaper
// per shipped task. Totals are stored (not averages) to keep the ledger free of
// rounding; per-task averages are derived on read.
type Trend struct {
	Pass           int
	DeliveredTasks int
	Turns          int   // total turns across delivered tasks
	Bytes          int64 // total bytes across delivered tasks
	Retries        int   // total retries across delivered tasks
}

// ComputeTrend sums the delivered-only cost for a pass.
func ComputeTrend(aggs []TaskAgg, pass int) Trend {
	t := Trend{Pass: pass}
	for _, a := range aggs {
		if !a.Delivered {
			continue
		}
		t.DeliveredTasks++
		t.Turns += a.Turns
		t.Bytes += a.Bytes
		t.Retries += a.Retries
	}
	return t
}

// TurnsPerTask, BytesPerTask, RetriesPerTask are the derived per-delivered-task
// averages (zero when no delivered task, never a divide-by-zero panic).
func (t Trend) TurnsPerTask() float64   { return ratio(t.Turns, t.DeliveredTasks) }
func (t Trend) BytesPerTask() float64   { return ratio(int(t.Bytes), t.DeliveredTasks) }
func (t Trend) RetriesPerTask() float64 { return ratio(t.Retries, t.DeliveredTasks) }

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// DeliveredFromPlan collects the taskids that reached DONE on main, the only
// definition of "delivered". It reads the parsed PLAN.md (beehive layer); the
// caller passes plan.Parse(planBytes).
func DeliveredFromPlan(p *plan.Plan) map[string]bool {
	out := map[string]bool{}
	for _, t := range p.Tasks {
		if t.Status == plan.StatusDone {
			out[t.ID] = true
		}
	}
	return out
}
