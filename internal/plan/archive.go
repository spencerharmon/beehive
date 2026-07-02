package plan

import (
	"regexp"
	"strings"
)

// PLAN.md accretes post-completion audit prose: every DONE task carries its
// Impl/Review/Arbitration narrative inline, and the whole file is re-read by
// every honeybee each pass (it holds the live claim metadata). That prose is the
// DONE record, not task input — the signal an agent needs to ACT is the terse
// task card (the header + description + Files:/Doc:/Accept:). Archive offloads
// the narrative to a sibling store so the live plan stays proportional to OPEN
// work, cutting the per-pass re-read. The parser round-trips the leaned file, so
// the runner/selector/web see the identical task set.

// archiveDir is the sibling store (relative to the submodule directory, where
// PLAN.md lives) that holds offloaded DONE-task narrative, one file per task. It
// mirrors the Doc: convention (docs/tasks/<id>.md) so paths read the same way.
const archiveDir = "docs/plan-archive"

// archivePointerPrefix marks the one-line breadcrumb a leaned DONE card keeps in
// place of its moved prose. Its presence is the idempotency guard: an already
// archived task is never re-moved or duplicated on a re-run.
const archivePointerPrefix = "Archived-details:"

// narrativeRe matches the first line of a post-completion narrative section: the
// audit prose (Impl/Review/Arbitration/Reconciled) that records HOW a task was
// closed, which an agent does not need to act. A section opens with one of these
// keywords immediately followed by " (" (a parenthetical that may span multiple
// lines, e.g. "Impl (bee-x, commit abc,\n...):") or ":" (a bare header, e.g.
// "Impl:"). Anchoring on that boundary avoids matching ordinary prose that
// merely starts with the word ("Impl/Review prose ...", "Impl note ...",
// "Implementation ...", "Reviewer ...").
var narrativeRe = regexp.MustCompile(`^(Impl|Review|Arbitration|Reconciled)( \(|:)`)

// ArchiveDoc is a DONE task's offloaded narrative. Path is relative to the
// submodule directory (alongside PLAN.md); the caller persists Content there.
type ArchiveDoc struct {
	ID      string
	Path    string
	Content string
}

// Archive offloads the post-completion Impl/Review/Arbitration/Reconciled
// narrative of every DONE task out of the plan body, leaving the lean task card
// (header + description + Files:/Doc:/Accept:) plus a one-line pointer to the
// archived prose. It mutates p in place and returns the archive docs the caller
// must write (Path relative to the submodule dir). It is behavior-preserving:
//
//   - Only DONE tasks are touched. TODO/NEEDS-REVIEW/NEEDS-ARBITRATION/NEEDS-HUMAN
//     tasks (the OPEN set) and ALL task metadata on every task — status, deps,
//     weight, attempts, session, heartbeat — are left byte-for-byte identical, so
//     Parse still round-trips the leaned plan and the selector sees the same set.
//   - A DONE task with no narrative section (already lean, or never had one) is a
//     no-op, and a DONE card already carrying the pointer is skipped outright, so
//     re-running Archive changes nothing (idempotent) — no re-move, no duplication.
func (p *Plan) Archive() []ArchiveDoc {
	var docs []ArchiveDoc
	for _, t := range p.Tasks {
		if t.Status != StatusDone {
			continue // never touch OPEN work or its claim metadata
		}
		if bodyHasPointer(t.Body) {
			continue // already archived; keep re-runs a strict no-op
		}
		card, narrative := splitCard(t.Body)
		if len(narrative) == 0 {
			continue // no post-completion prose to offload
		}
		path := archiveDir + "/" + t.ID + ".md"
		docs = append(docs, ArchiveDoc{
			ID:      t.ID,
			Path:    path,
			Content: archiveContent(t.ID, narrative),
		})
		t.Body = append(card, archivePointerPrefix+" "+path)
	}
	return docs
}

// splitCard divides a task body at the first narrative-section marker: the lines
// before it are the retained card (description + Files:/Doc:/Accept:), the marker
// and everything after are the offloadable narrative. A body with no marker
// yields the whole body as the card and an empty narrative. The returned card is
// a fresh slice with trailing blanks trimmed, safe for the caller to append to.
func splitCard(body []string) (card, narrative []string) {
	for i, line := range body {
		if narrativeRe.MatchString(line) {
			return trimTrailingBlank(append([]string(nil), body[:i]...)), body[i:]
		}
	}
	return body, nil
}

// bodyHasPointer reports whether a body already carries the archive breadcrumb.
func bodyHasPointer(body []string) bool {
	for _, line := range body {
		if strings.HasPrefix(strings.TrimSpace(line), archivePointerPrefix) {
			return true
		}
	}
	return false
}

// archiveContent renders a DONE task's offloaded narrative as a standalone doc.
// Deterministic: identical narrative yields identical bytes.
func archiveContent(id string, narrative []string) string {
	var b strings.Builder
	b.WriteString("# " + id + " — archived plan narrative\n\n")
	b.WriteString("Offloaded from PLAN.md by `beehive plan archive` to keep the live plan proportional to\n")
	b.WriteString("OPEN work. The authoritative change record is this task's Doc: change doc; the prose below\n")
	b.WriteString("is the post-completion Impl/Review narrative that formerly sat inline in PLAN.md.\n\n")
	b.WriteString(strings.Join(trimTrailingBlank(narrative), "\n"))
	b.WriteString("\n")
	return b.String()
}
