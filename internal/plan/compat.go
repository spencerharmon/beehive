package plan

import "time"

// Short status aliases for swarm/select/claim consumers.
const (
	TODO        = StatusTODO
	NeedsReview = StatusReview
	NeedsArb    = StatusArb
	Done        = StatusDone
	NeedsHuman  = StatusHuman
)

// Find returns the task with id, nil if absent (alias of Task).
func (p *Plan) Find(id string) *Task { return p.Task(id) }

// ROIStamp returns the recorded ROI reconcile sha.
func (p *Plan) ROIStamp() string { return p.ROI }

// Candidates returns the highest-priority tier of selectable tasks, skipping any
// task already actively claimed (a fresh session+heartbeat): a bee never selects
// work another bee is currently holding. A stale claim (expired heartbeat) does
// not protect a task — it is reclaimable, so it remains a candidate by its
// status, and the selecting bee's claim overwrites the dead owner's stamp.
//
// Priority: arbitration > review > main (TODO with deps satisfied).
func (p *Plan) Candidates(now time.Time, ttl time.Duration) []Task {
	var arb, rev, main []Task
	for _, t := range p.Tasks {
		if t.Active(now, ttl) {
			continue // someone is on it; leave it alone
		}
		switch t.Status {
		case StatusArb:
			arb = append(arb, *t)
		case StatusReview:
			rev = append(rev, *t)
		case StatusTODO:
			if p.Selectable(t) {
				main = append(main, *t)
			}
		}
	}
	for _, tier := range [][]Task{arb, rev, main} {
		if len(tier) > 0 {
			return tier
		}
	}
	return nil
}
