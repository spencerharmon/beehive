package plan

import (
	"fmt"
	"regexp"
	"slices"
)

// A DONE task's PLAN.md body grows an unbounded tail of post-hoc audit prose
// (Impl / Review / Reconciled / Arbitration / Merged sections) that every
// honeybee re-reads each session even though it is never task input. The lean
// "task card" an agent actually acts on is the header (parsed metadata) plus the
// leading body: the description and the Files:/Doc:/Accept: block. This file
// separates the two so the narrative can be moved to docs/plan-archive/<id>.md,
// keeping PLAN.md proportional to OPEN work without touching the parse.

// archiveSectionRe matches the first line of an archivable narrative section in a
// task body. The recognized sections open with a keyword immediately followed by
// ": " or " (" (e.g. `Impl:`, `Impl (bee-x ...):`, `Review (approved ...):`,
// `Reconciled (ROI ...):`, `Arbitration (BINDING ...):`, `Merged: ...`). The
// tight suffix deliberately does NOT match prose that merely starts with the
// word — e.g. the description phrase "Impl/Review prose ..." or a continuation
// "Impl note in this PLAN body" — so only real section headers start the split.
var archiveSectionRe = regexp.MustCompile(`^(Impl|Review|Reconciled|Arbitration|Merged)(:| \()`)

// splitCard divides a task body into the retained lean card and the archivable
// narrative. The split point is the first line that opens a narrative section
// (archiveSectionRe); everything before it — the description plus the
// Files:/Doc:/Accept: block — is the card, and everything from it to the end is
// the narrative. A body with no narrative section returns (body, nil), which is
// what makes archiving idempotent: an already-lean card has nothing to move.
func splitCard(body []string) (card, narrative []string) {
	for i, line := range body {
		if archiveSectionRe.MatchString(line) {
			return body[:i], body[i:]
		}
	}
	return body, nil
}

// Narrative returns the archivable narrative lines in this task's body without
// modifying it, or nil when the body is already lean (no narrative section).
func (t *Task) Narrative() []string {
	_, narrative := splitCard(t.Body)
	return narrative
}

// LeanDone moves the archivable narrative out of every DONE task, returning the
// removed lines keyed by task id. Only DONE tasks that actually carry narrative
// appear in the result; their in-memory Body is reduced to the lean card. OPEN
// tasks (any non-DONE status) are never touched, and NO task's claim metadata
// (status, deps, weight, attempts, session, heartbeat) is ever changed — only
// closed-task body prose moves. Idempotent: once a DONE task is lean, a later
// call returns nothing for it because its body no longer opens a narrative
// section. Callers persist the returned narrative (e.g. to docs/plan-archive/)
// before serializing the leaned plan.
func (p *Plan) LeanDone() map[string][]string {
	out := make(map[string][]string)
	for _, t := range p.Tasks {
		if t.Status != StatusDone {
			continue
		}
		card, narrative := splitCard(t.Body)
		if len(narrative) == 0 {
			continue
		}
		t.Body = trimTrailingBlank(card)
		out[t.ID] = narrative
	}
	return out
}

// SameMeta returns an error describing the first task-metadata difference between
// a and b, or nil when both plans carry the same tasks — same identities in the
// same order with identical status, deps, weight, attempts, session, and
// heartbeat. Bodies are intentionally ignored: it is exactly the invariant an
// archive pass must preserve (behavior-preserving for the parser and selector).
func SameMeta(a, b *Plan) error {
	if len(a.Tasks) != len(b.Tasks) {
		return fmt.Errorf("plan: task count changed %d -> %d", len(a.Tasks), len(b.Tasks))
	}
	for i := range a.Tasks {
		x, y := a.Tasks[i], b.Tasks[i]
		switch {
		case x.ID != y.ID:
			return fmt.Errorf("plan: task %d id changed %q -> %q", i, x.ID, y.ID)
		case x.Status != y.Status:
			return fmt.Errorf("plan: task %s status changed %s -> %s", x.ID, x.Status, y.Status)
		case x.Weight != y.Weight:
			return fmt.Errorf("plan: task %s weight changed %d -> %d", x.ID, x.Weight, y.Weight)
		case x.Attempts != y.Attempts:
			return fmt.Errorf("plan: task %s attempts changed %d -> %d", x.ID, x.Attempts, y.Attempts)
		case x.Session != y.Session:
			return fmt.Errorf("plan: task %s session changed %q -> %q", x.ID, x.Session, y.Session)
		case !x.Heartbeat.Equal(y.Heartbeat):
			return fmt.Errorf("plan: task %s heartbeat changed %v -> %v", x.ID, x.Heartbeat, y.Heartbeat)
		case !slices.Equal(x.Deps, y.Deps):
			return fmt.Errorf("plan: task %s deps changed %v -> %v", x.ID, x.Deps, y.Deps)
		}
	}
	return nil
}
