package web

import (
	"os"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
)

// Task status strings surfaced to the views. They mirror the internal/plan state
// machine values verbatim (the single source of truth). There is NO IN-PROGRESS
// status: "being worked right now" is derived from a fresh session+heartbeat
// claim (PlanItem.Active), not a status — see the unified claim model.
const (
	StatusTODO   = string(plan.StatusTODO)   // "TODO"
	StatusReview = string(plan.StatusReview) // "NEEDS-REVIEW"
	StatusArb    = string(plan.StatusArb)    // "NEEDS-ARBITRATION"
	StatusDone   = string(plan.StatusDone)   // "DONE"
	StatusHuman  = string(plan.StatusHuman)  // "NEEDS-HUMAN"
)

// PlanItem is one task row projected from a plan.Task for the templates. The
// view needs Desc/Doc, which plan.Task does not carry, so they are derived from
// the task body (first non-empty line / a "Doc:" convention line). Active/Stale
// are the unified claim state (session + heartbeat freshness vs the TTL).
type PlanItem struct {
	ID        string
	Status    string
	Desc      string // first non-empty body line (plan.Task has no Desc field)
	Deps      []string
	Weight    int
	Session   string    // claim owner; "" when unclaimed
	Heartbeat time.Time // last claim stamp; zero when unclaimed
	Active    bool      // claim fresh within the TTL (the unified "in progress")
	Stale     bool      // claim past the TTL (GC-reclaimable; owner presumed dead)
	Doc       string    // linked change-doc path from a body "Doc:" line, "" if none
}

// Claim renders the unified claim state for the plan view's claim column:
// "active <session>" / "stale <session>" (derived from session+heartbeat
// freshness, never from a status), or "" when the task is unclaimed.
func (it PlanItem) Claim() string {
	switch {
	case it.Active:
		return "active " + it.Session
	case it.Stale:
		return "stale " + it.Session
	default:
		return ""
	}
}

// Plan is the parsed PLAN.md projected for the views.
type Plan struct {
	ROIStamp string
	Items    []PlanItem
}

// parsePlan reads PLAN.md and projects it for the views via internal/plan.Parse
// (the single PLAN.md parser — the H2 header format `## <id> [STATUS] <!--
// attempts=N deps=a,b weight=W session=<id> heartbeat=<RFC3339> -->`). A missing
// file is an empty plan. Each task's active/stale claim state is derived against
// now and ttl. now/ttl are passed in so the projection is deterministically
// testable; handlers supply time.Now() and the resolved TTL.
func parsePlan(path string, now time.Time, ttl time.Duration) (Plan, error) {
	var p Plan
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return p, err
	}
	parsed, err := plan.Parse(string(b))
	if err != nil {
		return p, err
	}
	p.ROIStamp = parsed.ROI
	for _, t := range parsed.Tasks {
		p.Items = append(p.Items, projectTask(t, now, ttl))
	}
	return p, nil
}

// projectTask maps a plan.Task to the view's PlanItem, deriving Desc (first
// non-empty body line) and Doc (a "Doc:" convention line in the body) and the
// active/stale claim flags against now/ttl.
func projectTask(t *plan.Task, now time.Time, ttl time.Duration) PlanItem {
	it := PlanItem{
		ID:        t.ID,
		Status:    string(t.Status),
		Deps:      t.Deps,
		Weight:    t.Weight,
		Session:   t.Session,
		Heartbeat: t.Heartbeat,
		Active:    t.Active(now, ttl),
		Stale:     t.Stale(now, ttl),
	}
	for _, line := range t.Body {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		if it.Desc == "" {
			it.Desc = s
		}
		if rest, ok := strings.CutPrefix(s, "Doc:"); ok && it.Doc == "" {
			it.Doc = strings.TrimSpace(rest)
		}
	}
	return it
}
