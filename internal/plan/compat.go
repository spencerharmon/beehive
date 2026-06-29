package plan

import "time"

// Short status aliases for swarm/select/claim consumers.
const (
	TODO        = StatusTODO
	InProgress  = StatusInProgress
	NeedsReview = StatusReview
	NeedsArb    = StatusArb
	Done        = StatusDone
	NeedsHuman  = StatusHuman
)

// Find returns the task with id, nil if absent (alias of Task).
func (p *Plan) Find(id string) *Task { return p.Task(id) }

// ROIStamp returns the recorded ROI reconcile sha.
func (p *Plan) ROIStamp() string { return p.ROI }

// priorityTiers orders selectable types: GC stale IN-PROGRESS > arbitration >
// review > main (TODO). Candidates returns the highest non-empty tier's tasks.
func (p *Plan) Candidates(now time.Time, ttl time.Duration) []Task {
	var gc, arb, rev, main []Task
	for _, t := range p.Tasks {
		switch {
		case t.Stale(now, ttl):
			gc = append(gc, *t)
		case t.Status == StatusArb:
			arb = append(arb, *t)
		case t.Status == StatusReview:
			rev = append(rev, *t)
		case t.Status == StatusTODO && p.Selectable(t):
			main = append(main, *t)
		}
	}
	for _, tier := range [][]Task{gc, arb, rev, main} {
		if len(tier) > 0 {
			return tier
		}
	}
	return nil
}
