package plan

import (
	"fmt"
	"strings"
	"time"
)

// transitions enumerates the legal status edges. There is no IN-PROGRESS state:
// a task is implemented in place at TODO (marked active by a session+heartbeat),
// then moves straight to NEEDS-REVIEW. NEEDS-HUMAN is terminal and reachable via
// Reject overflow or explicit human-request escalation with a concrete reason.
var transitions = map[Status]map[Status]bool{
	StatusTODO:   {StatusReview: true},
	StatusReview: {StatusDone: true, StatusArb: true},
	StatusArb:    {StatusTODO: true, StatusDone: true},
}

// CanTransition reports whether from->to is legal.
func CanTransition(from, to Status) bool { return transitions[from][to] }

// Transition moves a task to a new status, enforcing the machine. A status change
// is a work-phase change, so it releases the active claim (clears session +
// heartbeat); the next phase is claimed afresh by whichever bee picks it up.
func (t *Task) Transition(to Status, now time.Time) error {
	if !CanTransition(t.Status, to) {
		return fmt.Errorf("plan: illegal transition %s -> %s for %s", t.Status, to, t.ID)
	}
	t.Status = to
	t.Session = ""
	t.Heartbeat = time.Time{}
	return nil
}

// Claim stamps the task as actively worked by session at now (the unified claim
// token used for every status). It does not change the status.
func (t *Task) Claim(session string, now time.Time) {
	t.Session = session
	t.Heartbeat = now
}

// Release clears the active claim (session + heartbeat) without changing status,
// e.g. when a honeybee hands a task back after losing the race or being GC'd.
func (t *Task) Release() {
	t.Session = ""
	t.Heartbeat = time.Time{}
}

// HeartbeatNow re-stamps the active claim's heartbeat to keep it fresh.
func (t *Task) HeartbeatNow(now time.Time) { t.Heartbeat = now }

// Active reports whether the task is being worked right now: it carries a session
// id and a heartbeat fresh within ttl. Independent of status.
func (t *Task) Active(now time.Time, ttl time.Duration) bool {
	if t.Session == "" || t.Heartbeat.IsZero() {
		return false
	}
	return now.Sub(t.Heartbeat) <= ttl
}

// Stale reports whether the task holds a claim whose heartbeat has expired: a GC
// candidate whose owner died. An unclaimed task is never stale.
func (t *Task) Stale(now time.Time, ttl time.Duration) bool {
	if t.Session == "" || t.Heartbeat.IsZero() {
		return false
	}
	return now.Sub(t.Heartbeat) > ttl
}

// Reject records a rejection: bumps attempts, and once attempts exceed limit the
// task goes NEEDS-HUMAN (no longer auto-recycled). Otherwise it returns to TODO.
// Valid from NEEDS-REVIEW or NEEDS-ARBITRATION. Releases the active claim.
func (t *Task) Reject(limit int, now time.Time) error {
	if t.Status != StatusReview && t.Status != StatusArb {
		return fmt.Errorf("plan: reject on non-reviewable task %s (%s)", t.ID, t.Status)
	}
	t.Attempts++
	t.Session = ""
	t.Heartbeat = time.Time{}
	if t.Attempts > limit {
		t.Status = StatusHuman
		if t.HumanReason() == "" {
			t.setHumanReason(fmt.Sprintf("rejected %d times; exceeded reject_limit=%d", t.Attempts, limit))
		}
	} else {
		t.Status = StatusTODO
	}
	return nil
}

// RequestHuman moves a non-DONE task to NEEDS-HUMAN with an explicit reason and
// releases its active claim. This is the first-class escalation path when a bee
// hits a concrete blocker requiring operator input (credentials, calibration,
// public-contract decision, contradictory spec, missing upstream API, etc.).
func (t *Task) RequestHuman(reason string, now time.Time) error {
	reason = oneLine(reason)
	if reason == "" {
		return fmt.Errorf("plan: human request for %s requires a reason", t.ID)
	}
	if t.Status == StatusDone {
		return fmt.Errorf("plan: cannot request human for DONE task %s", t.ID)
	}
	t.Status = StatusHuman
	t.Session = ""
	t.Heartbeat = time.Time{}
	t.setHumanReason(reason)
	return nil
}

// Resolve reopens a NEEDS-HUMAN task to TODO once the operator has cleared the
// blocker: it moves the task back into the selectable pool, drops the resolved
// Human-needed reason line, and releases any (now stale) claim. It is the inverse
// of RequestHuman and the ONLY edge out of the terminal NEEDS-HUMAN state — an
// explicit operator action, never an automatic transition (NEEDS-HUMAN stays
// terminal for the state machine and the selector, so a bee can never silently
// un-escalate its own blocker). It errors on a non-NEEDS-HUMAN task so a UI/CLI
// mistake can't reset an in-flight task's status or claim.
func (t *Task) Resolve(now time.Time) error {
	if t.Status != StatusHuman {
		return fmt.Errorf("plan: resolve on non-NEEDS-HUMAN task %s (%s)", t.ID, t.Status)
	}
	t.Status = StatusTODO
	t.Session = ""
	t.Heartbeat = time.Time{}
	t.clearHumanReason()
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
