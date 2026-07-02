package plan

import (
	"path"
	"regexp"
	"strings"
)

// PLAN.md grows unbounded as tasks close: every DONE card accretes a long
// Impl/Review/Reconciled/Arbitration narrative that is post-hoc audit history,
// not task input, yet every honeybee re-reads the whole file each session (it
// carries the live claim metadata). Archiving moves that closure prose out of
// PLAN.md into a sibling store, leaving the lean "task card" an agent actually
// acts on: the H2 header (id/status/attempts/deps/weight/claim) plus the task
// definition (description + Files/Doc/Accept). It is behavior-preserving for the
// parser and selector — only free-form body prose moves, never a header field —
// so plan.Parse round-trips the same task set/statuses/deps/weights/claims.

// archivedPrefix is the pointer line left on a leaned DONE card in place of its
// moved narrative, and the idempotency sentinel: a card already carrying it is
// never re-archived, so a re-run is a no-op.
const archivedPrefix = "Archived:"

// narrativeStartRe matches the first body line of a closed task's closure
// narrative — Impl / Review / Reconciled / Arbitration / Merged followed by a
// space+paren or a colon, the stable shapes those notes take. From that line to
// the end of the body is archived; the lean-card definition lines above it stay.
// The `[:(]` anchor keeps a description/Files/Doc/Accept line from ever matching.
var narrativeStartRe = regexp.MustCompile(`^(Impl|Review|Reconciled|Arbitration|Merged)\s*[:(]`)

// ArchivedTask is one task whose closure narrative was lifted out of PLAN.md.
// Narrative is the moved prose verbatim (no trailing newline); the caller
// persists it (e.g. to docs/plan-archive/<ID>.md).
type ArchivedTask struct {
	ID        string
	Narrative string
}

// Archived reports whether the task's card is already leaned — it carries the
// Archived: pointer stub — so archiving is a no-op for it.
func (t *Task) Archived() bool {
	for _, l := range t.Body {
		if strings.HasPrefix(strings.TrimSpace(l), archivedPrefix) {
			return true
		}
	}
	return false
}

// ArchivePath renders the archive-store path for a task id under storeRel, using
// forward slashes (it is a doc-relative markdown link, not an OS path).
func ArchivePath(storeRel, id string) string { return path.Join(storeRel, id+".md") }

// splitNarrative divides a task body into the retained lean-card lines and the
// archivable closure-narrative lines at the first narrative marker. ok is false
// when the body has no narrative to move.
func splitNarrative(body []string) (card, narrative []string, ok bool) {
	for i, l := range body {
		if narrativeStartRe.MatchString(l) {
			card = trimTrailingBlank(append([]string(nil), body[:i]...))
			narrative = trimTrailingBlank(append([]string(nil), body[i:]...))
			return card, narrative, len(narrative) > 0
		}
	}
	return body, nil, false
}

// ArchiveDone leans every DONE task in the plan in place: it moves each closed
// task's Impl/Review/Reconciled/Arbitration narrative out of the body, leaving
// the lean card (description + Files/Doc/Accept) plus a single
// "Archived: <storeRel>/<id>.md" pointer, and returns the extracted narratives
// in plan order for the caller to persist.
//
// It is behavior-preserving and idempotent:
//   - Only DONE tasks are considered; every OPEN task (and all claim metadata:
//     session, heartbeat, status, deps, weight, attempts) is untouched.
//   - A card already carrying the Archived: pointer, or a DONE card with no
//     narrative to move, is skipped — so a re-run returns nil and changes nothing.
//   - No header field is ever rewritten, so plan.Parse round-trips the same task
//     set/statuses/deps/weights/claims; only free-form body prose shrinks.
//
// storeRel (e.g. "docs/plan-archive") is used only to render the pointer line,
// keeping the transformation filesystem-free and unit-testable.
func (p *Plan) ArchiveDone(storeRel string) []ArchivedTask {
	var out []ArchivedTask
	for _, t := range p.Tasks {
		if t.Status != StatusDone || t.Archived() {
			continue
		}
		card, narrative, ok := splitNarrative(t.Body)
		if !ok {
			continue
		}
		t.Body = append(card, archivedPrefix+" "+ArchivePath(storeRel, t.ID))
		out = append(out, ArchivedTask{ID: t.ID, Narrative: strings.Join(narrative, "\n")})
	}
	return out
}
