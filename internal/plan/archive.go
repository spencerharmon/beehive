package plan

import (
	"regexp"
	"strings"
)

// PLAN.md leaning ("task card" archival).
//
// A task body is written in two halves. The first half is the LEAN TASK CARD: the
// description an agent needs to ACT, plus the `Files:`/`Doc:`/`Accept:` fields.
// The second half is post-hoc AUDIT NARRATIVE a honeybee appends after the task is
// worked — the `Impl`/`Review`/`Reconciled`/`Arbitration` blocks recording how it
// was done and reviewed. That narrative is history, not task input, yet every
// honeybee re-reads the whole PLAN.md each session (it carries the live claim
// metadata), so the accumulated DONE-task narrative is pure context bloat.
//
// Archive moves each DONE task's narrative out of PLAN.md into a sibling store
// (docs/plan-archive/<id>.md), leaving the lean card plus a one-line pointer. It
// is behavior-preserving for the parser and selector: only DONE task bodies change
// — every header, its claim metadata (session/heartbeat), deps, weight, attempts,
// and every OPEN task are left byte-identical — so Parse(p.String()) round-trips
// the task set unchanged. It is idempotent: an already-lean DONE task carries no
// narrative leader, so a re-run moves nothing.

// ArchiveDir is the per-submodule store (relative to the submodule dir, like the
// `Doc:` convention) that holds archived PLAN.md narrative, one file per task.
const ArchiveDir = "docs/plan-archive"

// ArchivePath returns the submodule-relative archive-doc path for task id.
func ArchivePath(id string) string { return ArchiveDir + "/" + id + ".md" }

// archivePointerPrefix marks the lean card's one-line pointer to the archive doc.
// It is NOT a narrative leader, so a re-run never re-archives (or duplicates) it.
const archivePointerPrefix = "Archived: "

// narrativeLeadRe matches the first line of a task's audit narrative: an
// `Impl`/`Review`/`Reconciled`/`Arbitration` section leader. The leader word must
// be followed by "(" or ":" (the only two forms used in every PLAN.md entry —
// "Impl (bee-x, ...", "Review:", "Reconciled (ROI abc): ...", "Arbitration
// (BINDING, ..."), so a card sentence that merely begins with one of these words
// is never mistaken for a section leader.
var narrativeLeadRe = regexp.MustCompile(`^(Impl|Review|Reconciled|Arbitration)\b\s*[(:]`)

// narrativeStart returns the index into body of the first audit-narrative line, or
// len(body) if body is a lean card with no narrative. body[:i] is the retained
// card; body[i:] is archivable narrative.
func narrativeStart(body []string) int {
	for i, line := range body {
		if narrativeLeadRe.MatchString(line) {
			return i
		}
	}
	return len(body)
}

// Archived is one DONE task whose audit narrative was moved out of PLAN.md.
type Archived struct {
	ID        string   // task id
	Narrative []string // the moved narrative body lines, verbatim
}

// Doc renders the standalone archive document for a leaned task (the content
// written to docs/plan-archive/<id>.md). Deterministic.
func (a Archived) Doc() string {
	var b strings.Builder
	b.WriteString("# " + a.ID + "\n\n")
	b.WriteString("Archived PLAN.md audit narrative (Impl/Review/Reconciled/Arbitration) for the DONE\n")
	b.WriteString("task `" + a.ID + "`, moved out of PLAN.md by `beehive plan archive` to keep the plan\n")
	b.WriteString("proportional to open work. The lean task card stays in PLAN.md; the authoritative\n")
	b.WriteString("change record is this task's docs/ change doc.\n\n")
	b.WriteString(strings.Join(a.Narrative, "\n"))
	b.WriteString("\n")
	return b.String()
}

// Archive moves the audit narrative out of every DONE task's body, leaving the
// lean card plus a one-line `Archived: <path>` pointer, and returns one Archived
// per task leaned THIS call (in plan order) so the caller can persist each task's
// prose. Only DONE tasks are touched; OPEN tasks and all header/claim metadata are
// left byte-identical, and an already-lean DONE task is skipped, so a second call
// returns nil and mutates nothing.
func (p *Plan) Archive() []Archived {
	var out []Archived
	for _, t := range p.Tasks {
		if t.Status != StatusDone {
			continue
		}
		n := narrativeStart(t.Body)
		if n >= len(t.Body) {
			continue // already lean: no narrative to move
		}
		// Copy both halves before mutating so the returned narrative never
		// aliases the task's rewritten backing array.
		narrative := trimBlankEdges(append([]string(nil), t.Body[n:]...))
		if len(narrative) == 0 {
			continue
		}
		card := trimTrailingBlank(append([]string(nil), t.Body[:n]...))
		card = append(card, archivePointerPrefix+ArchivePath(t.ID))
		t.Body = card
		out = append(out, Archived{ID: t.ID, Narrative: narrative})
	}
	return out
}

// trimBlankEdges drops leading and trailing blank lines.
func trimBlankEdges(ls []string) []string {
	for len(ls) > 0 && strings.TrimSpace(ls[0]) == "" {
		ls = ls[1:]
	}
	return trimTrailingBlank(ls)
}
