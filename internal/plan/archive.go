package plan

import (
	"regexp"
	"strings"
)

// narrativeRe matches the first line of a task's post-hoc narrative section: the
// Impl / Review / Reconciled / Arbitration prose a honeybee appends AFTER a task's
// card (its description + Files/Doc/Accept). Every such section starts a body line
// with one of those four keywords followed immediately by ":" or " (" — the two
// forms every honeybee has ever written (e.g. "Impl:", "Impl (bee-x, commit ...",
// "Review (approved, ...)", "Reconciled (ROI ...", "Arbitration (BINDING, ..."). The
// suffix is anchored on purpose: a description line that merely MENTIONS one of
// these words (e.g. "Impl/Review prose to docs/..." or "Impl note in this PLAN
// body") is not a section start and must not be a split point.
var narrativeRe = regexp.MustCompile(`^(Impl|Review|Reconciled|Arbitration)(:| \()`)

// Archived is a DONE task's narrative prose lifted out of its PLAN.md card. ID is
// the task id; Narrative holds the removed lines verbatim (leading/trailing blank
// lines trimmed). The lean card — the task description plus Files/Doc/Accept —
// stays in PLAN.md; the narrative moves to a sibling archive store.
type Archived struct {
	ID        string
	Narrative []string
}

// ArchiveDone leans every DONE task in place and returns the narrative it lifted
// out. For each task whose Status is DONE and whose body carries an appended
// narrative section (an Impl/Review/Reconciled/Arbitration block — matched by
// narrativeRe), it splits the body at the FIRST narrative line: the card portion
// (task description + Files/Doc/Accept) is retained as the task body and every
// line from the first marker onward is removed and returned as an Archived.
//
// It NEVER touches a non-DONE task, and it NEVER touches header metadata (status,
// deps, weight, session, heartbeat), so Parse(p.String()) round-trips the same
// task set / statuses / deps / weights / claims — only DONE-task body prose
// shrinks. It is idempotent: a DONE task that already carries no narrative
// (already leaned, or that never had one) is skipped, so a second call returns nil
// and leaves p.String() byte-identical. Pure: no I/O; the caller persists the
// returned narratives (see RenderArchive) and the leaned p.String().
func (p *Plan) ArchiveDone() []Archived {
	var out []Archived
	for _, t := range p.Tasks {
		if t.Status != StatusDone {
			continue
		}
		i := narrativeStart(t.Body)
		if i < 0 {
			continue // no narrative section: already lean, nothing to move
		}
		card := trimTrailingBlank(append([]string(nil), t.Body[:i]...))
		narrative := trimTrailingBlank(append([]string(nil), t.Body[i:]...))
		t.Body = card
		out = append(out, Archived{ID: t.ID, Narrative: narrative})
	}
	return out
}

// narrativeStart returns the index of the first body line beginning a narrative
// section, or -1 when the body is entirely card (no narrative to archive).
func narrativeStart(body []string) int {
	for i, line := range body {
		if narrativeRe.MatchString(line) {
			return i
		}
	}
	return -1
}

// RenderArchive serializes one archived narrative to its sibling store file
// (docs/plan-archive/<id>.md). It is deterministic — identical input yields a
// byte-identical file — so re-archiving the same narrative never churns the store.
// The authoritative change record remains the task's docs/<branch>-<id>.md change
// doc; this file just holds the plan-embedded prose the card no longer carries.
func RenderArchive(a Archived) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(a.ID)
	b.WriteString(" - archived PLAN.md narrative\n\n")
	if len(a.Narrative) > 0 {
		b.WriteString(strings.Join(a.Narrative, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}
