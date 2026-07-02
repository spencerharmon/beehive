package plan

import (
	"path"
	"regexp"
)

// Archiving keeps PLAN.md proportional to OPEN work. A completed task accumulates
// post-hoc audit prose (its Impl/Review/Reconciled/Arbitration narrative) that is
// history, not task input, yet every honeybee re-reads the whole file each session
// because it carries the live claim metadata. Archiving MOVES that narrative out of
// a DONE task into a sibling store (docs/plan-archive/<id>.md), leaving the lean
// "task card": the `## id [STATUS] <!-- ... -->` header + the description +
// Files/Doc/Accept — exactly what an agent needs to act, and what plan.Parse reads.
//
// Round-trip safety: ArchiveDone only rewrites DONE task BODIES. Headers (status,
// attempts, deps, weight, and the session/heartbeat claim) and every OPEN task are
// left byte-for-byte untouched, so Parse(p.String()) preserves the full task set.

// ArchiveDir is the per-submodule store (relative to the submodule root) that holds
// archived DONE-task narrative, one Markdown file per task id.
const ArchiveDir = "docs/plan-archive"

// archivedPrefix is the one-line breadcrumb left in a leaned card pointing at the
// extracted narrative. It is plain body text (not a card field or a narrative
// marker), so it is retained on re-parse and a re-archive run is a no-op.
const archivedPrefix = "Archived:"

// ArchivePath returns the archive file path (relative to the submodule root) for a
// task id: docs/plan-archive/<id>.md.
func ArchivePath(id string) string { return path.Join(ArchiveDir, id+".md") }

var (
	// narrativeRe matches a body line that BEGINS a DONE task's post-hoc audit
	// narrative. The required trailing " (" or ":" (e.g. "Impl (bee-x, ...", or
	// "Review: branch ...") is what distinguishes a section header from prose that
	// merely mentions the word, so a description like "Review the diff base" is
	// never mistaken for the narrative boundary.
	narrativeRe = regexp.MustCompile(`^(Impl|Review|Reconciled|Arbitration)( *\(|:)`)
	// cardFieldRe matches the structured card fields (Files/Doc/Accept) that always
	// precede the narrative. A narrative marker only counts once one of these has
	// been seen, so the boundary can never fall inside the free-form description.
	cardFieldRe = regexp.MustCompile(`^(Files|Doc|Accept):`)
)

// splitCard divides a task body into the retained lean card (description +
// Files/Doc/Accept) and the archivable narrative (the Impl/Review/Reconciled/
// Arbitration audit prose that follows). The split point is the first
// narrative-marker line seen AFTER a card field, so the free-form description is
// never cut. narrative is nil when there is nothing to archive (an already-lean
// card, or a body with no recognizable narrative). The returned slices alias body;
// callers that mutate must copy first.
func splitCard(body []string) (card, narrative []string) {
	sawField := false
	for i, line := range body {
		if cardFieldRe.MatchString(line) {
			sawField = true
			continue
		}
		if sawField && narrativeRe.MatchString(line) {
			return trimTrailingBlank(body[:i]), body[i:]
		}
	}
	return body, nil
}

// ArchiveDone leans every DONE task in place: it strips the post-hoc narrative
// (Impl/Review/Reconciled/Arbitration prose) from the task body, leaving the lean
// card (description + Files/Doc/Accept) plus a one-line "Archived:" pointer to the
// extracted file. It returns id -> extracted narrative lines for the caller to
// persist under ArchivePath(id).
//
// Behavior-preserving by construction: only DONE task bodies change. A DONE task
// already lean (no narrative) and every OPEN task (TODO / NEEDS-REVIEW /
// NEEDS-ARBITRATION / NEEDS-HUMAN) are left untouched, and no header metadata
// (status, attempts, deps, weight, session, heartbeat) is ever modified — so
// Parse(p.String()) yields the identical task set/statuses/deps/weights/claims, and
// a second ArchiveDone returns an empty map (idempotent no-op).
func (p *Plan) ArchiveDone() map[string][]string {
	out := map[string][]string{}
	for _, t := range p.Tasks {
		if t.Status != StatusDone {
			continue
		}
		card, narrative := splitCard(t.Body)
		if len(narrative) == 0 {
			continue
		}
		// splitCard returns subslices aliasing t.Body; copy both so rebuilding the
		// body cannot clobber the narrative we hand back.
		out[t.ID] = append([]string(nil), narrative...)
		lean := append([]string(nil), card...)
		lean = append(lean, archivedPrefix+" "+ArchivePath(t.ID))
		t.Body = lean
	}
	return out
}
