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

// NotBeforeReached reports whether the task's optional wall-clock `not_before`
// gate has passed at now: true when no gate is set (zero NotBefore) or when
// now is at/after it. A future gate holds a TODO task out of the ready set the
// same way an unmet dep does; deps are gated independently of this. Recovery
// tiers (review/arbitration) are never gated by not_before.
func (t *Task) NotBeforeReached(now time.Time) bool {
	if t.NotBefore.IsZero() {
		return true
	}
	return !now.Before(t.NotBefore)
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

// Strand demotes a task from NEEDS-REVIEW back to a workable status because its
// implementer commit could not be durably pushed to the submodule's origin —
// not even after reconciling a divergent dead orphan left by a prior GC'd
// attempt (see git.Repo.PushBranchReconciled) — so the runner must never leave
// it NEEDS-REVIEW pointing at a commit no reviewer can reach (session-audit-003
// F-LIVE). Mirrors Reject's attempts/limit escalation (past limit -> NEEDS-HUMAN,
// otherwise TODO for a fresh honeybee to redo the work and retry the push) but
// carries the caller's own concrete reason instead of a generic rejection
// message. Valid only from NEEDS-REVIEW. Releases the active claim.
func (t *Task) Strand(reason string, limit int, now time.Time) error {
	if t.Status != StatusReview {
		return fmt.Errorf("plan: strand on non-NEEDS-REVIEW task %s (%s)", t.ID, t.Status)
	}
	reason = oneLine(reason)
	if reason == "" {
		return fmt.Errorf("plan: strand for %s requires a reason", t.ID)
	}
	t.Attempts++
	t.Session = ""
	t.Heartbeat = time.Time{}
	if t.Attempts > limit {
		t.Status = StatusHuman
		t.setHumanReason(reason)
	} else {
		t.Status = StatusTODO
		t.appendNote("Stranded (runner): " + reason)
	}
	return nil
}

// BounceUnreachable moves a NEEDS-REVIEW task straight to NEEDS-ARBITRATION,
// deterministically and without ever spawning a review session, when its
// implementer branch/commit is reachable nowhere this host can see (session-
// audit-003 F-LIVE: a review pass against a phantom commit can only spelunk git
// internals and idle-time-out, forever). reason is appended as a review note in
// the same free-form "Review (verdict, who): ..." convention a rejecting
// reviewer writes, so the Arbitration pass that picks this up next reads
// exactly why it was bounced instead of judged. Valid only from NEEDS-REVIEW.
// Releases the active claim.
func (t *Task) BounceUnreachable(reason string) error {
	if t.Status != StatusReview {
		return fmt.Errorf("plan: bounce-unreachable on non-NEEDS-REVIEW task %s (%s)", t.ID, t.Status)
	}
	reason = oneLine(reason)
	if reason == "" {
		return fmt.Errorf("plan: bounce-unreachable for %s requires a reason", t.ID)
	}
	t.Status = StatusArb
	t.Session = ""
	t.Heartbeat = time.Time{}
	t.appendNote("Review (bounced, runner): " + reason)
	return nil
}

// FinalizeAlreadyMerged moves a NEEDS-REVIEW task straight to DONE,
// deterministically and without ever spawning a review session, when its
// recorded submodule pointer commit is discovered to already be an ancestor of
// the submodule's tracked main — the SYMMETRIC counterpart to
// BounceUnreachable (session-audit-005 F-1). A review's two effects are not
// atomic: it (1) merges bee-<taskid> into the submodule's tracked main and
// pushes (durable, irreversible), then (2) commits the hive-layer bookkeeping
// (gitlink bump + this transition). Interrupted between them, the code is
// merged-and-done at origin while the task still reads NEEDS-REVIEW at the
// pre-merge pointer; dispatching a whole second review to re-discover an
// already-landed merge wastes a full session on bookkeeping. Ancestry-of-main
// can only follow a PRIOR approved-review merge (submodule main advances only
// via approved reviews), so auto-finalizing is safe: it completes interrupted
// bookkeeping, it never approves anything itself. note is appended as a body
// line in the same free-form convention BounceUnreachable/Strand use. Valid
// from NEEDS-REVIEW or NEEDS-ARBITRATION. Releases the active claim.
func (t *Task) FinalizeAlreadyMerged(note string, now time.Time) error {
	if t.Status != StatusReview && t.Status != StatusArb {
		return fmt.Errorf("plan: finalize-already-merged on non-reviewable task %s (%s)", t.ID, t.Status)
	}
	note = oneLine(note)
	if note == "" {
		return fmt.Errorf("plan: finalize-already-merged for %s requires a note", t.ID)
	}
	t.Status = StatusDone
	t.Session = ""
	t.Heartbeat = time.Time{}
	t.appendNote("Review (runner-finalized): " + note)
	return nil
}

// SetReviewCommit durably records sha as the submodule commit this task's
// completed Work pass handed to review (its NEEDS-REVIEW gitlink tip). It is a
// pure metadata setter — it never changes status or the claim — written by the
// runner right after a Work pass lands NEEDS-REVIEW so a later pass can still
// recognize the work as already-merged-into-main after the disposable
// bee-<taskid> branch is reclaimed/reused. Overwrites any prior value (each
// fresh review landing supersedes the last); a blank sha is ignored so a
// resolution failure never erases a good record.
func (t *Task) SetReviewCommit(sha string) {
	if sha == "" {
		return
	}
	t.ReviewCommit = sha
}

// RecoverLostWork resets a NEEDS-REVIEW or NEEDS-ARBITRATION task back to TODO
// (or, past limit, escalates to NEEDS-HUMAN like Reject/Strand) when its
// implementer commit is confirmed truly unrecoverable — reachable NOWHERE
// (branch absent both locally and on the submodule remote after a
// prune-fetch), not reflected in the tracked submodule pointer, and with no
// change doc on disk. A work pass can flip a task NEEDS-REVIEW/NEEDS-ARBITRATION
// after authoring in its worktree but before the runner publishes; if that
// publish never lands (crash, killed at cap, failed push), the task strands at
// a phantom commit forever without this. Mirrors Reject/Strand's
// attempts/limit escalation so a task that keeps losing its work does not
// auto-recycle indefinitely. Valid from NEEDS-REVIEW or NEEDS-ARBITRATION.
// Releases the active claim.
func (t *Task) RecoverLostWork(reason string, limit int, now time.Time) error {
	if t.Status != StatusReview && t.Status != StatusArb {
		return fmt.Errorf("plan: recover-lost-work on non-reviewable task %s (%s)", t.ID, t.Status)
	}
	reason = oneLine(reason)
	if reason == "" {
		return fmt.Errorf("plan: recover-lost-work for %s requires a reason", t.ID)
	}
	t.Attempts++
	t.Session = ""
	t.Heartbeat = time.Time{}
	if t.Attempts > limit {
		t.Status = StatusHuman
		t.setHumanReason(reason)
	} else {
		t.Status = StatusTODO
		t.appendNote("Recovered (runner, lost work): " + reason)
	}
	return nil
}

// appendNote appends line as a new task body line, inserting a blank-line
// separator first when the body is non-empty and does not already end on a
// blank line — the same spacing setHumanReason uses when it first adds its
// field, so a freshly appended note never runs onto the previous line.
func (t *Task) appendNote(line string) {
	if len(t.Body) > 0 && strings.TrimSpace(t.Body[len(t.Body)-1]) != "" {
		t.Body = append(t.Body, "")
	}
	t.Body = append(t.Body, line)
}

// RequestHuman moves a non-DONE task to NEEDS-HUMAN with an explicit category +
// reason and releases its active claim. This is the first-class honeybee-
// initiated escalation path when a bee hits a concrete blocker requiring operator
// input. The category MUST be one of the four legitimate Category values (secret,
// external-permission, contradiction, architecture) — an invalid/empty category
// is rejected so a bee cannot escalate an unclassified blocker (or farm ordinary
// in-authority work out to a human by leaving it uncategorized). The reason must
// be non-empty and should lead with the category's operator-facing ask.
func (t *Task) RequestHuman(category Category, reason string, now time.Time) error {
	if !category.Valid() {
		return fmt.Errorf("plan: human request for %s requires a category, one of %v", t.ID, Categories())
	}
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
	t.HumanCategory = category
	t.setHumanReason(reason)
	return nil
}

// EscalationReady reports whether a NEEDS-HUMAN task is a well-formed honeybee-
// initiated escalation: a non-empty reason AND a valid category. The runner's
// work/review/arbitration completion checks use this to refuse to terminate a
// pass on a malformed escalation (blank reason or missing/invalid category), so a
// honeybee is forced to classify its blocker before the pass can end. Runner-
// forced overflow escalations (Reject/Strand/RecoverLostWork) set NEEDS-HUMAN
// directly and never flow through these checks, so their lack of a category does
// not strand a pass.
func (t *Task) EscalationReady() bool {
	return t.HumanReason() != "" && t.HumanCategory.Valid()
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
	t.HumanCategory = ""
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
