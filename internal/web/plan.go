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

// Dep is one of a task's dependencies projected for the plan view: the
// depended-on task id and whether it is satisfied (that task is DONE in this
// plan). A dep id absent from this plan (e.g. a cross-submodule reference the
// per-plan view cannot resolve) is shown unsatisfied — satisfaction is resolved
// within the plan's own DONE set.
type Dep struct {
	Name string
	Done bool
}

// PlanItem is one task row projected from a plan.Task for the templates. The
// view needs Desc/Doc, which plan.Task does not carry, so they are derived from
// the task body (first non-empty line / a "Doc:" convention line). Active/Stale
// are the unified claim state (session + heartbeat freshness vs the TTL).
// DepStates and DocHref are view-only enrichments: DepStates marks each dep
// satisfied/pending against this plan, and DocHref links the change doc the
// implementing commit stamped (set by the handler, which has the repo + docs/).
type PlanItem struct {
	ID        string
	Status    string
	Desc      string // first non-empty body line (plan.Task has no Desc field)
	Deps      []string
	DepStates []Dep // deps resolved to satisfied/pending against this plan's DONE set
	Weight    int
	Session   string    // claim owner; "" when unclaimed
	Heartbeat time.Time // last claim stamp; zero when unclaimed
	Active    bool      // claim fresh within the TTL (the unified "in progress")
	Stale     bool      // claim past the TTL (GC-reclaimable; owner presumed dead)
	Doc       string    // linked change-doc path from a body "Doc:" line, "" if none
	DocHref   string    // link to view the change doc (from the commit stamp), "" if unresolved
}

// StatusClass is the design-system pill class for the task's status: the base
// `status` shape class plus a `status-<slug>` hue class where slug is the
// lower-cased status (NEEDS-REVIEW -> status-needs-review). Emitting the base
// class too keeps an unknown/empty status shaped (neutral) rather than unstyled.
func (it PlanItem) StatusClass() string {
	return "status status-" + strings.ToLower(it.Status)
}

// ClaimState is the unified claim phase surfaced as a label: "active" (fresh
// session+heartbeat within the TTL), "stale" (claim past the TTL — owner
// presumed dead), or "" when unclaimed. Derived from session+heartbeat
// freshness, never from a status (there is no IN-PROGRESS status).
func (it PlanItem) ClaimState() string {
	switch {
	case it.Active:
		return "active"
	case it.Stale:
		return "stale"
	default:
		return ""
	}
}

// HeartbeatLabel renders the claim heartbeat as a compact UTC timestamp for the
// view, or "" when the task is unclaimed (zero heartbeat).
func (it PlanItem) HeartbeatLabel() string {
	if it.Heartbeat.IsZero() {
		return ""
	}
	return it.Heartbeat.UTC().Format("2006-01-02 15:04Z")
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
	resolveDeps(p.Items)
	return p, nil
}

// resolveDeps fills each item's DepStates, marking which of its deps are DONE in
// this plan (a satisfied/pending dependency indicator for the view). It is a
// second pass because satisfaction needs every task's status. A dep id absent
// from the plan (e.g. a cross-submodule reference) stays unsatisfied — the plan
// view resolves dependency satisfaction within its own plan.
func resolveDeps(items []PlanItem) {
	done := make(map[string]bool, len(items))
	for _, it := range items {
		if it.Status == StatusDone {
			done[it.ID] = true
		}
	}
	for i := range items {
		for _, d := range items[i].Deps {
			items[i].DepStates = append(items[i].DepStates, Dep{Name: d, Done: done[d]})
		}
	}
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
