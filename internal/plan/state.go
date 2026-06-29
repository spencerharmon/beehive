package plan

import (
	"fmt"
	"strings"
	"time"
)

// transitions enumerates the legal status edges. NEEDS-HUMAN is terminal and
// only reachable via Reject overflow, never a direct transition.
var transitions = map[Status]map[Status]bool{
	StatusTODO:       {StatusInProgress: true},
	StatusInProgress: {StatusReview: true, StatusTODO: true},
	StatusReview:     {StatusDone: true, StatusArb: true},
	StatusArb:        {StatusTODO: true, StatusDone: true},
}

// CanTransition reports whether from->to is legal.
func CanTransition(from, to Status) bool { return transitions[from][to] }

// Transition moves a task to a new status, enforcing the machine. It clears the
// heartbeat on any terminal/non-in-progress state and stamps it on IN-PROGRESS.
func (t *Task) Transition(to Status, now time.Time) error {
	if !CanTransition(t.Status, to) {
		return fmt.Errorf("plan: illegal transition %s -> %s for %s", t.Status, to, t.ID)
	}
	t.Status = to
	if to == StatusInProgress {
		t.Heartbeat = now
	} else {
		t.Heartbeat = time.Time{}
	}
	return nil
}

// Heartbeat re-stamps an IN-PROGRESS task; error otherwise.
func (t *Task) HeartbeatNow(now time.Time) error {
	if t.Status != StatusInProgress {
		return fmt.Errorf("plan: heartbeat on non-in-progress task %s (%s)", t.ID, t.Status)
	}
	t.Heartbeat = now
	return nil
}

// Stale reports whether an IN-PROGRESS task's heartbeat is older than ttl: a GC
// candidate. Non-in-progress tasks are never stale.
func (t *Task) Stale(now time.Time, ttl time.Duration) bool {
	if t.Status != StatusInProgress || t.Heartbeat.IsZero() {
		return false
	}
	return now.Sub(t.Heartbeat) > ttl
}

// Reject records a rejection: bumps attempts, and once attempts exceed limit the
// task goes NEEDS-HUMAN (no longer auto-recycled). Otherwise it returns to TODO.
// Valid from NEEDS-REVIEW or NEEDS-ARBITRATION.
func (t *Task) Reject(limit int, now time.Time) error {
	if t.Status != StatusReview && t.Status != StatusArb {
		return fmt.Errorf("plan: reject on non-reviewable task %s (%s)", t.ID, t.Status)
	}
	t.Attempts++
	t.Heartbeat = time.Time{}
	if t.Attempts > limit {
		t.Status = StatusHuman
	} else {
		t.Status = StatusTODO
	}
	return nil
}

// Selectable reports whether a task can be auto-selected: not terminal, not
// NEEDS-HUMAN, and all LOCAL deps DONE in p. A dep id containing ":" names a
// task in another submodule (<submodule>:<taskid>); the plan layer stays
// links-free and defers those to the selector, which owns the combined
// cross-submodule graph (link authorization + DONE status + cycle exclusion).
func (p *Plan) Selectable(t *Task) bool {
	if t.Status == StatusDone || t.Status == StatusHuman {
		return false
	}
	for _, d := range t.Deps {
		if strings.Contains(d, ":") {
			continue // cross-submodule; resolved by the selector, not here
		}
		dep := p.Task(d)
		if dep == nil || dep.Status != StatusDone {
			return false
		}
	}
	return true
}
