package plan

import (
	"regexp"
	"strings"
)

// narrativeMarkerRe matches the first line of a DONE task's post-hoc narrative:
// the Impl / Review / Reconciled / Arbitration prose the runner and reviewers
// append after the task card. It is anchored to a genuine section head — the
// marker word immediately followed by ":" or " (" ("Impl:", "Impl (", "Review:",
// "Review (", "Reconciled (", "Arbitration (") — so it never fires on ordinary
// description text that merely begins with one of those words (e.g.
// "Impl/Review prose to docs/..." or "Reconcile completion never fires").
var narrativeMarkerRe = regexp.MustCompile(`^(Impl|Review|Reconciled|Arbitration)( \(|:)`)

// Archived is the narrative prose lifted out of one DONE task's PLAN.md card by
// Archive. The lean card (header + description + Files/Doc/Accept) stays in
// PLAN.md; this carries the trailing audit history a caller writes to a sibling
// store (docs/plan-archive/<id>.md).
type Archived struct {
	ID        string   // the task id
	Header    string   // the task's "## <id> [STATUS] <!-- ... -->" header line
	Narrative []string // the moved body lines (Impl/Review/Reconciled/Arbitration...)
}

// cardBodyLen returns how many leading body lines form the retained task card:
// the free-form description plus the Files:/Doc:/Accept: lines, i.e. everything
// before the first narrative-marker line. It returns len(body) when the body has
// no narrative marker (the whole body is already the card).
func cardBodyLen(body []string) int {
	for i, line := range body {
		if narrativeMarkerRe.MatchString(line) {
			return i
		}
	}
	return len(body)
}

// Archive leans the plan's DONE task cards. For every DONE task it splits the
// body at the first narrative marker, keeping the card (description +
// Files/Doc/Accept) on the task and returning the trailing Impl/Review/
// Reconciled/Arbitration prose as an Archived entry for the caller to persist.
//
// It mutates only DONE tasks' Body slices. No header metadata (status, attempts,
// deps, weight, session, heartbeat) and no OPEN (non-DONE) task is touched, so
// Parse(p.String()) yields the identical task set / statuses / deps / weights /
// claims after archiving — only the DONE-narrative bytes move out. Because a DONE
// task's claim is released on the DONE transition, DONE cards carry no live
// session/heartbeat, so the runner's heartbeat restamp is never raced.
//
// Deterministic and idempotent: a DONE card with no narrative marker is left
// unchanged and produces no Archived entry, so a second Archive on an already-
// leaned plan returns nil and mutates nothing.
func (p *Plan) Archive() []Archived {
	var out []Archived
	for _, t := range p.Tasks {
		if t.Status != StatusDone {
			continue // only closed-task narrative is archived; OPEN tasks untouched
		}
		n := cardBodyLen(t.Body)
		if n >= len(t.Body) {
			continue // no narrative to move
		}
		// Copy the narrative out before truncating the task's body so the
		// returned slice does not alias the plan's backing array.
		narrative := trimBlankEdges(t.Body[n:])
		if len(narrative) == 0 {
			continue
		}
		out = append(out, Archived{
			ID:        t.ID,
			Header:    t.header(),
			Narrative: narrative,
		})
		t.Body = trimTrailingBlank(t.Body[:n])
	}
	return out
}

// trimBlankEdges returns a copy of ls with leading and trailing all-blank lines
// removed, so the archived narrative is a clean, self-contained block independent
// of the task's backing array.
func trimBlankEdges(ls []string) []string {
	out := append([]string(nil), ls...)
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	return trimTrailingBlank(out)
}
