package web

import (
	"html/template"
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
// are the unified claim state (session + heartbeat freshness vs the TTL); Active
// is also, verbatim, the PLAN-claim half of active.go's canonical activeHoneybees
// union (active-honeybee-count-unify) — the plan view and the dashboard/sessions/
// stats consumers all read this SAME field, never a second computation of it.
// DepStates and DocHref are view-only enrichments: DepStates marks each dep
// satisfied/pending against this plan, and DocHref links the change doc the
// implementing commit stamped (set by the handler, which has the repo + docs/).
// Body is the task's full body text verbatim (plan-view-detail-polish): the
// expand-in-place detail view renders it (via BodyHTML) so a row can reveal
// more than Desc's single clipped line.
type PlanItem struct {
	ID          string
	Status      string
	Desc        string // first non-empty body line (plan.Task has no Desc field)
	Body        string // full body text verbatim (all lines, joined with "\n"), "" if empty
	Deps        []string
	DepStates   []Dep // deps resolved to satisfied/pending against this plan's DONE set
	Weight      int
	Session     string    // claim owner; "" when unclaimed
	Heartbeat   time.Time // last claim stamp; zero when unclaimed
	NotBefore   time.Time // optional wall-clock selection gate; zero when no gate set
	Active      bool      // claim fresh within the TTL (the unified "in progress")
	Stale       bool      // claim past the TTL (GC-reclaimable; owner presumed dead)
	Doc         string    // linked change-doc path from a body "Doc:" line, "" if none
	DocHref     string    // link to view the change doc (from the commit stamp or the design Doc), "" if unresolved
	HumanReason string    // explicit NEEDS-HUMAN reason from a body "Human-needed:" line (may span multiple lines)
	Category    string    // NEEDS-HUMAN escalation category (secret|external-permission|contradiction|architecture), "" if unclassified/runner-forced
}

// BodyHTML renders the task's full body (Body) as sanitized markdown for the
// expand-in-place detail affordance (plan-view-detail-polish): the complete
// task description, not just the clipped Desc first line, through the same
// renderMarkdown helper the explorer/ROI/doc views already use (editor-
// markdown-render). "" when the task carries no body.
func (it PlanItem) BodyHTML() template.HTML {
	if it.Body == "" {
		return ""
	}
	return renderMarkdown(it.Body)
}

// HumanReasonHTML renders the task's NEEDS-HUMAN reason (HumanReason) as
// sanitized markdown for the /human view (plan-view-detail-polish): a
// structured reason (a one-line summary plus bullets, per HONEYBEE.md's
// escalation guidance) renders as real markup instead of raw escaped text.
// "" when the task carries no reason.
func (it PlanItem) HumanReasonHTML() template.HTML {
	if it.HumanReason == "" {
		return ""
	}
	return renderMarkdown(it.HumanReason)
}

// StatusClass is the design-system pill class for the task's status: the base
// `status` shape class plus a `status-<slug>` hue class where slug is the
// lower-cased status (NEEDS-REVIEW -> status-needs-review). Emitting the base
// class too keeps an unknown/empty status shaped (neutral) rather than unstyled.
func (it PlanItem) StatusClass() string {
	return "status status-" + strings.ToLower(it.Status)
}

// CategoryClass is the design-system badge class for a NEEDS-HUMAN escalation
// category: the base `cat` shape plus a `cat-<value>` hue class, or `cat
// cat-unclassified` when the task carries no category (a runner-forced overflow
// escalation or a legacy pre-category task). Parallels StatusClass.
func (it PlanItem) CategoryClass() string {
	slug := it.Category
	if slug == "" {
		slug = "unclassified"
	}
	return "cat cat-" + slug
}

// CategoryLabel is the short human-facing badge text for the escalation category:
// a compact label per category, or "unclassified" when none is set. Kept terse so
// it reads as a badge; the resolve page carries the full per-category guidance.
func (it PlanItem) CategoryLabel() string {
	switch plan.Category(it.Category) {
	case plan.CatSecret:
		return "secret"
	case plan.CatExternalPermission:
		return "external permission"
	case plan.CatContradiction:
		return "contradiction"
	case plan.CatArchitecture:
		return "architecture decision"
	default:
		return "unclassified"
	}
}

// Ask is the one-line, category-appropriate framing of what the operator must do
// — the lead line the resolve page shows above the reason so the operator sees
// the KIND of ask before any technical detail. "" for an unclassified escalation
// (the page falls back to the raw reason).
func (it PlanItem) Ask() string {
	switch plan.Category(it.Category) {
	case plan.CatSecret:
		return "The swarm needs a credential only you can provide. Add the named store key, then mark resolved."
	case plan.CatExternalPermission:
		return "The swarm needs an action on infrastructure it does not control (out-of-cluster / host-root / vendor). Do it, then mark resolved."
	case plan.CatContradiction:
		return "The intent is contradictory and the swarm cannot tell which side wins. Decide, record it in ROI/PLAN, then mark resolved."
	case plan.CatArchitecture:
		return "A high-level, hard-to-reverse design decision is needed. Choose, record it in ROI, then mark resolved."
	default:
		return ""
	}
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

// NotBeforeLabel renders the task's optional not_before wall-clock gate as a
// compact UTC timestamp for the plan view (shown greyed at the top of the task
// description), or "" when the task carries no gate (zero NotBefore). Same
// format as HeartbeatLabel so the two read consistently.
func (it PlanItem) NotBeforeLabel() string {
	if it.NotBefore.IsZero() {
		return ""
	}
	return it.NotBefore.UTC().Format("2006-01-02 15:04Z")
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
//
// This is the UNCACHED baseline (read + parse + project every call); the
// server's planView memoizes the read+parse half through viewCache. The two must
// stay equivalent — the cache test asserts planView == parsePlan.
func parsePlan(path string, now time.Time, ttl time.Duration) (Plan, error) {
	parsed, err := readParsePlan(path)
	if err != nil {
		return Plan{}, err
	}
	return projectPlan(parsed, now, ttl), nil
}

// planView is the cached equivalent of parsePlan: it memoizes the expensive,
// time-independent read+parse (readParsePlan) through the HEAD-keyed viewCache
// and recomputes the cheap, time-dependent projection (projectPlan) fresh every
// call. For any single HEAD generation planView(head, path, now, ttl) equals
// parsePlan(path, now, ttl) — the cache changes only WHEN the read+parse runs,
// never WHAT the view contains (the cache test asserts this equivalence, and
// that a claim still goes stale on TTL expiry with no intervening commit because
// the projection is never cached).
//
// head is the beehive repo HEAD short SHA, resolved once per request by the
// caller (Server.headSHA) and shared across every submodule read; the cache key
// is the PLAN.md path (unique per submodule), so a commit to any tracked file
// (which advances head) re-parses on next access.
func (s *Server) planView(head, path string, now time.Time, ttl time.Duration) (Plan, error) {
	parsed, err := cachedView(head, s.cache, path, func() (*plan.Plan, error) {
		return readParsePlan(path)
	})
	if err != nil {
		return Plan{}, err
	}
	return projectPlan(parsed, now, ttl), nil
}

// readParsePlan reads a PLAN.md and returns the raw internal/plan model. This is
// the expensive, TIME-INDEPENDENT half of parsePlan (disk read + parse) — the
// exact work viewCache memoizes per HEAD generation. A missing file is an empty
// plan (never an error): a freshly-added, pre-bootstrap submodule. The result is
// treated as read-only by callers (projectPlan only reads it), so a cached
// *plan.Plan is safe to share across concurrent requests.
func readParsePlan(path string) (*plan.Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &plan.Plan{}, nil
		}
		return nil, err
	}
	return plan.Parse(string(b))
}

// projectPlan derives the view Plan from a raw parsed plan, computing each task's
// active/stale claim state against now/ttl. This is the cheap, TIME-DEPENDENT
// half — it is recomputed every request (never cached) so a claim goes stale on
// TTL expiry even when no new commit advanced HEAD.
func projectPlan(parsed *plan.Plan, now time.Time, ttl time.Duration) Plan {
	var p Plan
	if parsed == nil {
		return p
	}
	p.ROIStamp = parsed.ROI
	for _, t := range parsed.Tasks {
		p.Items = append(p.Items, projectTask(t, now, ttl))
	}
	resolveDeps(p.Items)
	return p
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
// non-empty body line), Body (the full body verbatim, for the expand-in-place
// detail view), and Doc (a "Doc:" convention line in the body), and the
// active/stale claim flags against now/ttl.
func projectTask(t *plan.Task, now time.Time, ttl time.Duration) PlanItem {
	it := PlanItem{
		ID:          t.ID,
		Status:      string(t.Status),
		Deps:        t.Deps,
		Weight:      t.Weight,
		Session:     t.Session,
		Heartbeat:   t.Heartbeat,
		NotBefore:   t.NotBefore,
		Active:      t.Active(now, ttl),
		Stale:       t.Stale(now, ttl),
		HumanReason: t.HumanReason(),
		Category:    string(t.HumanCategory),
	}
	if len(t.Body) > 0 {
		it.Body = strings.Join(t.Body, "\n")
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
