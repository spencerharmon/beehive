package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

// subStat is one submodule's honeybee-performance figures, all derived on read
// from git — never stored, so they can't drift from reality (the same signals as
// skills/bin/beehive-stats.sh; see docs/conflict-resolution.md).
//
//	DeliveredTasks (✅) = tasks at PLAN [DONE] — a task can take more than one
//	                     merge, so we count the task, not merges.
//	Honeybees      (🐝) = session transcript files (one per honeybee pass);
//	                     an ALL-TIME historical count, unlike ActiveNow below.
//	ActiveNow           = honeybees actively working THIS submodule right now,
//	                     via activeHoneybees — the SAME canonical active set
//	                     the dashboard counter and the sessions page/list read
//	                     (active-honeybee-count-unify), so this figure can never
//	                     disagree with either of them.
//	Stranded            = tasks with a stamped bee-<task> branch ahead of main that
//	                     never merged (finished work whose merge didn't land — the
//	                     wedge indicator; not lost, GC never drops an unmerged branch).
//	DeliveredPerBeePct  = 100 * DeliveredTasks / Honeybees   (the ✅/🐝 yield)
//	Models              = the same figures split by the agent model each session
//	                     ran on (transcript-header `model:` stamp), for A/B
//	                     comparison across models. Delivered is attributed to the
//	                     model of a DONE task's most-recent session.
//	Deliveries          = delivery-traceability: one DeliveryLink per DONE task,
//	                     linking the hive commit that flipped it to DONE and the
//	                     submodule commit/doc that carries its code (see delivery.go).
type subStat struct {
	Name               string
	DeliveredTasks     int
	Honeybees          int
	ActiveNow          int
	Stranded           int
	DeliveredPerBeePct float64
	Models             []modelStat
	Deliveries         []DeliveryLink
}

// modelStat is one agent model's slice of a submodule's (or the total's)
// performance, derived from the per-session transcript `model:` stamp.
type modelStat struct {
	Model              string
	DeliveredTasks     int
	Honeybees          int
	DeliveredPerBeePct float64
}

func (m *modelStat) derive() {
	if m.Honeybees > 0 {
		m.DeliveredPerBeePct = 100 * float64(m.DeliveredTasks) / float64(m.Honeybees)
	}
}

// defaultModel labels sessions whose transcript predates the model stamp (or was
// written by a build without it). This host has only ever run opus, so crediting
// unstamped history to it is exact rather than a guess (operator-approved).
const defaultModel = "github-copilot/claude-opus-4.8"

// sessionNameRE splits a transcript stem `bee-<task>-<epoch>-<pid>` into the task
// id (1) and the epoch (2) / pid (3) that order a task's repeated attempts.
var sessionNameRE = regexp.MustCompile(`^bee-(.+)-(\d+)-(\d+)$`)

func (st *subStat) derive() {
	if st.Honeybees > 0 {
		st.DeliveredPerBeePct = 100 * float64(st.DeliveredTasks) / float64(st.Honeybees)
	}
}

// smAggregate is the TIME-INDEPENDENT, git-derived slice of one submodule's
// /stats figures: everything that turns purely on committed history (DONE task
// count, per-model session tallies, stranded branches) and NOT on the wall
// clock. It is memoized per HEAD generation in viewCache (statsAggregate), so a
// warm /stats never re-reads the submodule's thousands of session-transcript
// HEADERS or re-walks its branch refs — that per-file I/O, not any single
// parse, is what made /stats cost seconds on the live hive. The one figure that
// is time-dependent — ActiveNow, a claim's active/stale flip across the TTL
// with no new commit — is DELIBERATELY excluded here and recomputed fresh each
// request in computeStats (the cache doc forbids caching that projection).
type smAggregate struct {
	DeliveredTasks int
	Honeybees      int
	Stranded       int
	Models         []modelStat
	Bees           map[string]int // per-model session tally, for the total row
	Delivered      map[string]int // per-model delivered tally, for the total row
	DoneIDs        []string       // DONE task ids, for buildDeliveries (order preserved)
}

// statsAggregate computes (or returns memoized) the time-independent figures for
// one submodule at HEAD generation head. Everything it touches is a pure
// projection of committed history, so it is safe to cache per HEAD; ActiveNow is
// computed by the caller, never here. An empty head bypasses the cache (loads
// fresh) exactly like every other cachedView caller.
func (s *Server) statsAggregate(ctx context.Context, head string, sm repo.Submodule) smAggregate {
	agg, _ := cachedView(head, s.cache, "stats-aggregate:"+sm.Name, func() (smAggregate, error) {
		return s.computeSmAggregate(ctx, sm), nil
	})
	return agg
}

// computeSmAggregate does the actual (uncached) work behind statsAggregate: the
// session-header scan + per-model attribution + stranded-branch walk for one
// submodule. Split out from computeStats so the expensive, time-independent part
// can be memoized while ActiveNow stays fresh.
func (s *Server) computeSmAggregate(ctx context.Context, sm repo.Submodule) smAggregate {
	agg := smAggregate{Bees: map[string]int{}, Delivered: map[string]int{}}
	doneIDs := doneTaskIDs(sm)
	agg.DoneIDs = doneIDs
	done := make(map[string]bool, len(doneIDs))
	for _, id := range doneIDs {
		done[id] = true
	}
	agg.DeliveredTasks = len(doneIDs)
	// Per-model tallies for this submodule, plus the model of each task's
	// most-recent session (epoch then pid) so a DONE task's delivery is
	// attributed to the model that last drove it.
	bees := agg.Bees
	type latest struct {
		epoch, pid int
		model      string
	}
	taskLatest := map[string]latest{}
	if ents := scanSessionDir(sm.SessionsDir()); len(ents) > 0 {
		// Derive each session's model tag in PARALLEL: sessionTags reads the
		// transcript HEADER per file, and over thousands of transcripts that
		// accumulated per-file I/O — not any single parse — is what made
		// /stats slow, so fan it across a worker pool. Each result is
		// index-aligned with ents; the fold below stays serial (map writes).
		names := make([]string, len(ents))
		for i, e := range ents {
			names[i] = e.ID
		}
		models := parallelMap(names, func(id string) string {
			stem := id
			if sessionNameRE.FindStringSubmatch(stem) == nil {
				return ""
			}
			// Model comes from the session's BUILT-IN `model` TAG
			// (sessionTags — the extensible, git-derived tag model),
			// single-sourcing the model parse with the rest of the tag set.
			// sessionTags OMITS an absent model; the by-model view credits
			// that unstamped history to opus (defaultModel), below.
			return s.sessionTags(sessionRef{submodule: sm.Name, path: filepath.Join(sm.SessionsDir(), stem+".md")})["model"]
		})
		for i, e := range ents {
			stem := e.ID
			m := sessionNameRE.FindStringSubmatch(stem)
			if m == nil {
				continue
			}
			agg.Honeybees++
			model := models[i]
			if model == "" {
				model = defaultModel
			}
			bees[model]++
			task := m[1]
			epoch, _ := strconv.Atoi(m[2])
			pid, _ := strconv.Atoi(m[3])
			if cur, ok := taskLatest[task]; !ok || epoch > cur.epoch || (epoch == cur.epoch && pid > cur.pid) {
				taskLatest[task] = latest{epoch, pid, model}
			}
		}
	}
	// Attribute each delivered task to its latest session's model.
	for task := range done {
		if l, ok := taskLatest[task]; ok {
			agg.Delivered[l.model]++
		}
	}
	agg.Models = buildModelStats(bees, agg.Delivered)
	agg.Stranded = strandedCount(ctx, git.New(sm.RepoDir()), done)
	return agg
}

// computeStats returns per-submodule figures plus a total row.
func (s *Server) computeStats(ctx context.Context) (subs []subStat, total subStat, err error) {
	sms, err := s.repo.Submodules()
	if err != nil {
		return nil, subStat{}, err
	}
	total.Name = "total"
	// Total-row per-model accumulators, summed across submodules.
	totBees := map[string]int{}
	totDelivered := map[string]int{}
	// Resolved ONCE and shared across every submodule's delivery lookup below,
	// so a multi-submodule /stats render pays a single `rev-parse`, not one per
	// submodule (mirrors headSHA's own doc comment / the planView cache key).
	head := s.headSHA(ctx)
	// now/ttl for ActiveNow's claim-freshness projection (activeHoneybees),
	// resolved once and shared across every submodule exactly like head above.
	now, ttl := time.Now(), s.ttl()
	// Resolve the live-stream-branch snapshot ONCE and share it across every
	// submodule's ActiveNow computation (a single git for-each-ref for the whole
	// /stats page instead of one per submodule).
	live := s.liveBranchSet(ctx)
	for _, sm := range sms {
		// The heavy, TIME-INDEPENDENT figures (session-header scan, per-model
		// attribution, stranded walk) come memoized per HEAD from statsAggregate
		// — a warm /stats never re-reads the thousands of transcripts.
		agg := s.statsAggregate(ctx, head, sm)
		st := subStat{
			Name:           sm.Name,
			DeliveredTasks: agg.DeliveredTasks,
			Honeybees:      agg.Honeybees,
			Stranded:       agg.Stranded,
			Models:         agg.Models,
		}
		// ActiveNow: the canonical active-honeybee set (active-honeybee-count-
		// unify) — the SAME set the dashboard counter and sessions page/list
		// read, never a re-derived rule. TIME-DEPENDENT (a claim goes stale as
		// the wall clock crosses the TTL with no new commit), so it is computed
		// FRESH every request here, never memoized. A PLAN.md parse error leaves
		// it 0 rather than failing the whole page (mirrors subViews' resilience).
		if p, perr := s.planView(head, sm.PlanPath(), now, ttl); perr == nil {
			st.ActiveNow = len(s.activeHoneybeesLive(ctx, sm, p, live))
		}
		// delivery-traceability: link each DONE task to the hive commit that
		// flipped it (half a) and its submodule code/doc (half b) — see
		// delivery.go. Best-effort/read-only; never fails the page. Its flip
		// half is served stale-while-revalidate so it never blocks the page.
		st.Deliveries = s.buildDeliveries(ctx, head, sm, agg.DoneIDs)
		for mdl, n := range agg.Bees {
			totBees[mdl] += n
		}
		for mdl, n := range agg.Delivered {
			totDelivered[mdl] += n
		}
		st.derive()
		subs = append(subs, st)
		total.DeliveredTasks += st.DeliveredTasks
		total.Honeybees += st.Honeybees
		total.ActiveNow += st.ActiveNow
		total.Stranded += st.Stranded
	}
	total.Models = buildModelStats(totBees, totDelivered)
	total.derive()
	return subs, total, nil
}

// buildModelStats folds the per-model session and delivered tallies into a stable,
// display-ordered slice (most honeybees first, then model name) with the yield
// derived. A model appears if it ran any session or delivered any task.
func buildModelStats(bees, delivered map[string]int) []modelStat {
	seen := map[string]bool{}
	for m := range bees {
		seen[m] = true
	}
	for m := range delivered {
		seen[m] = true
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]modelStat, 0, len(seen))
	for m := range seen {
		ms := modelStat{Model: m, Honeybees: bees[m], DeliveredTasks: delivered[m]}
		ms.derive()
		out = append(out, ms)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Honeybees != out[j].Honeybees {
			return out[i].Honeybees > out[j].Honeybees
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// strandedTaskSet returns the SET of task ids whose bee-<task> branch is ahead
// of the submodule's pull target and carries that task's completion stamp,
// while the task itself isn't DONE — finished work whose merge never landed.
// Best-effort: any git error yields an empty set for that submodule rather
// than failing the page. Split out from strandedCount (which just counts it)
// so stats-filter-groupby's grouped view can attribute EACH stranded task to
// its tag tuple, the same way delivered tasks already are.
func strandedTaskSet(ctx context.Context, g *git.Repo, done map[string]bool) map[string]bool {
	ref := "main"
	if _, err := g.RevParse(ctx, "origin/main"); err == nil {
		ref = "origin/main"
	}
	out, err := g.Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/remotes/origin/bee-*")
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, br := range strings.Fields(out) {
		i := strings.Index(br, "/bee-")
		if i < 0 {
			continue
		}
		task := br[i+len("/bee-"):]
		if done[task] {
			continue
		}
		if c, _ := g.Run(ctx, "rev-list", "--count", ref+".."+br); strings.TrimSpace(c) == "0" || strings.TrimSpace(c) == "" {
			continue
		}
		tr, _ := g.Run(ctx, "log", ref+".."+br, "--format=%(trailers:key=Beehive,valueonly)")
		for _, line := range strings.Split(tr, "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0] == task {
				set[task] = true
				break
			}
		}
	}
	return set
}

// strandedCount counts bee-<task> branches ahead of the submodule's pull target
// that carry the task's completion stamp but whose task isn't DONE — finished
// work whose merge never landed. Best-effort: any git error yields 0 for that
// submodule rather than failing the page.
func strandedCount(ctx context.Context, g *git.Repo, done map[string]bool) int {
	return len(strandedTaskSet(ctx, g, done))
}

// tagFilter is one FILTER BAR chip: an ANDed test over a session's resolved
// tags (sessionTags). Composed by the operator through separate key/operator/
// value inputs (never a query string to type) but always canonicalized to the
// `filter=key<op>value` URL shape (buildStatsURL), so the FILTER BAR stays
// shareable/bookmarkable. Op is the per-chip selector — never typed syntax —
// and is one of:
//
//	""  or "=" — equality (tags[Key] == Value); "" is the zero value so every
//	             pre-existing plain-equality chip/literal keeps working with no
//	             call site needing to set it explicitly.
//	"!=" — not-equal (tags[Key] != Value). A tag a session's resolved set
//	       omits reads as "" (sessionTags never stores an empty value), so a
//	       missing tag counts as "not equal" to any non-empty Value — the same
//	       missing-tag-reads-as-empty-string semantics `=` already had.
//	"=~" — regex-match (regexp.MatchString(Value, tags[Key])). An invalid
//	       Value pattern degrades to "matches nothing" (see matchesFilters)
//	       rather than a 500 — still no query-expression language, just a
//	       chip whose own pattern happens to be unusable.
type tagFilter struct {
	Key, Op, Value string
}

// filterChip is one rendered FILTER BAR entry: the chip's key/operator/value
// plus the href that removes JUST this chip — every OTHER active filter and
// the current group-by selection carried through unchanged — so removing a
// chip is a single plain link, no JS, no partial-state loss. Op is always the
// DISPLAY form ("=", "!=", or "=~" — never "", unlike tagFilter.Op's zero
// value) since it is rendered directly into the chip's text.
type filterChip struct {
	Key, Op, Value string
	RemoveHref     string
}

// groupStat is one row of the /stats filter+group-by view: the resolved tag
// VALUE for each key in the request's group-by key list (Values, aligned by
// index with the top-level GroupBy key list the template renders as column
// headers), plus the same delivered/honeybee/stranded figures the default
// view shows — computed ONLY over sessions that pass every filter chip
// (matchesFilters) and attributed to this row's exact tag tuple.
type groupStat struct {
	Values             []string
	DeliveredTasks     int
	Honeybees          int
	Stranded           int
	DeliveredPerBeePct float64
}

func (g *groupStat) derive() {
	if g.Honeybees > 0 {
		g.DeliveredPerBeePct = 100 * float64(g.DeliveredTasks) / float64(g.Honeybees)
	}
}

// matchesFilters reports whether tags satisfies EVERY filter chip (AND, never
// OR), each tested with its OWN operator (tagFilter.Op — a per-chip selector,
// never typed syntax): a filter whose key is absent from tags reads as ""
// (sessionTags never stores an empty value), so `=`/`!=` decide against that
// empty string and `=~` matches its pattern against it — exactly the "unknown
// tag key or value yields an empty group" contract, with no special-casing
// needed here. An invalid `=~` pattern degrades to matching NOTHING (never a
// 500): the chip itself just yields an empty group, same as any other
// unsatisfiable filter.
func matchesFilters(tags map[string]string, filters []tagFilter) bool {
	for _, f := range filters {
		v := tags[f.Key]
		switch f.Op {
		case "!=":
			if v == f.Value {
				return false
			}
		case "=~":
			re, err := regexp.Compile(f.Value)
			if err != nil || !re.MatchString(v) {
				return false
			}
		default: // "" (zero value) or "=": plain equality, unchanged from before Op existed.
			if v != f.Value {
				return false
			}
		}
	}
	return true
}

// tagValues resolves keys against tags in order, "" for any key a session's
// tag set omits — the group-by tuple a session is attributed to.
func tagValues(tags map[string]string, keys []string) []string {
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = tags[k]
	}
	return vals
}

// computeGroupedStats computes the /stats filter+group-by view: delivered
// ✅/honeybees 🐝/✅ per 🐝/stranded aggregated per DISTINCT group-by tag
// tuple, over ONLY the sessions that pass every filter chip (ANDed equality on
// sessionTags, matchesFilters). Sessions are pooled across EVERY submodule —
// group-by is free to name `submodule` itself as one of its keys, so a global
// comparison ("opus vs sonnet on reviews", group-by=model with no submodule
// key) and a per-target breakdown (group-by=submodule,model) are the exact
// same mechanism, just a different key list. groupBy may be empty: every
// surviving session then collapses into the single "" tuple (a filtered
// aggregate with no breakdown — the filter-only, no-group-by case). An unknown
// tag key/value simply matches no session, yielding zero rows — never an
// error.
//
// Task attribution mirrors computeStats' own by-model logic exactly, just
// keyed by the resolved tuple instead of hardcoded to model: for each task,
// find its LATEST (by epoch then pid) session that survives the filter, and
// credit that task's delivered/stranded status to THAT session's tuple. A task
// with no filter-surviving session is attributed nowhere (correctly absent
// from a kind=review view if none of its sessions were review passes).
func (s *Server) computeGroupedStats(ctx context.Context, filters []tagFilter, groupBy []string) ([]groupStat, error) {
	sms, err := s.repo.Submodules()
	if err != nil {
		return nil, err
	}
	type latest struct {
		epoch, pid int
		tuple      string
	}
	rows := map[string]*groupStat{}
	var order []string
	rowFor := func(vals []string) *groupStat {
		key := strings.Join(vals, "\x1f")
		gs, ok := rows[key]
		if !ok {
			gs = &groupStat{Values: append([]string(nil), vals...)}
			rows[key] = gs
			order = append(order, key)
		}
		return gs
	}
	for _, sm := range sms {
		doneIDs := doneTaskIDs(sm)
		done := make(map[string]bool, len(doneIDs))
		for _, id := range doneIDs {
			done[id] = true
		}
		stranded := strandedTaskSet(ctx, git.New(sm.RepoDir()), done)
		taskLatest := map[string]latest{}
		ents, rerr := os.ReadDir(sm.SessionsDir())
		if rerr != nil {
			continue
		}
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			stem := strings.TrimSuffix(e.Name(), ".md")
			m := sessionNameRE.FindStringSubmatch(stem)
			if m == nil {
				continue
			}
			tags := s.sessionTags(sessionRef{submodule: sm.Name, path: filepath.Join(sm.SessionsDir(), e.Name())})
			if !matchesFilters(tags, filters) {
				continue
			}
			vals := tagValues(tags, groupBy)
			rowFor(vals).Honeybees++
			task := m[1]
			epoch, _ := strconv.Atoi(m[2])
			pid, _ := strconv.Atoi(m[3])
			tuple := strings.Join(vals, "\x1f")
			if cur, ok := taskLatest[task]; !ok || epoch > cur.epoch || (epoch == cur.epoch && pid > cur.pid) {
				taskLatest[task] = latest{epoch, pid, tuple}
			}
		}
		for task, l := range taskLatest {
			row := rows[l.tuple]
			if done[task] {
				row.DeliveredTasks++
			}
			if stranded[task] {
				row.Stranded++
			}
		}
	}
	out := make([]groupStat, 0, len(order))
	for _, key := range order {
		rows[key].derive()
		out = append(out, *rows[key])
	}
	// Stable, deterministic render order: lexicographic by the tuple's own
	// values (independent of submodule iteration/map order).
	sort.Slice(out, func(i, j int) bool {
		for k := range out[i].Values {
			if out[i].Values[k] != out[j].Values[k] {
				return out[i].Values[k] < out[j].Values[k]
			}
		}
		return false
	})
	return out, nil
}

// parseFilters extracts every `filter=key<op>value` query param as an ANDed
// FILTER BAR chip, where <op> is `=` (the default — no operator marker at all,
// so every pre-existing `filter=key=value` chip parses exactly as before),
// `!=`, or `=~`: a trailing `!` on the key (`key!=value`) or a leading `~` on
// the value (`key=~value`) selects it. A malformed chip (no `=` at all, or an
// empty key once any operator marker is stripped) is dropped rather than
// erroring — there is no query-expression language, only composable,
// per-chip-operator filters.
func parseFilters(r *http.Request) []tagFilter {
	var out []tagFilter
	for _, raw := range r.URL.Query()["filter"] {
		i := strings.Index(raw, "=")
		if i <= 0 {
			continue
		}
		key, op, val := raw[:i], "", raw[i+1:]
		switch {
		case strings.HasSuffix(key, "!"):
			key, op = key[:len(key)-1], "!="
		case strings.HasPrefix(val, "~"):
			val, op = val[1:], "=~"
		}
		if key == "" {
			continue
		}
		out = append(out, tagFilter{Key: key, Op: op, Value: val})
	}
	return out
}

// parseFilterOp normalizes the add-filter form's `fop` operator-selector
// value (the per-chip selector — never typed syntax — the FILTER BAR's
// "+ add filter" control posts alongside fkey/fval) to the SAME Op convention
// parseFilters itself produces: "!=" and "=~" pass through, anything else
// (missing, "=", or garbage from a hand-built request) normalizes to the ""
// zero-value equality Op, exactly like a plain `filter=key=value` chip.
func parseFilterOp(r *http.Request) string {
	switch op := strings.TrimSpace(r.URL.Query().Get("fop")); op {
	case "!=", "=~":
		return op
	default:
		return ""
	}
}

// parseGroupBy extracts the GROUP-BY tag key list. It accepts both the
// canonical single comma-separated param (`group-by=model,submodule`, the
// shareable-URL shape from the docs example) and repeated params
// (`group-by=model&group-by=submodule`, what a plain HTML checkbox group
// posts with no JS) — either shape, or a mix of both, yields the same
// ordered, de-duplicated key list.
func parseGroupBy(r *http.Request) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range r.URL.Query()["group-by"] {
		for _, k := range strings.Split(raw, ",") {
			k = strings.TrimSpace(k)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// buildStatsURL composes a /stats query string in the ONE canonical shape
// every chip-removal link, group-by toggle, and add-filter redirect converges
// on: one `filter=key<op>value` param per chip (op defaults to `=` for the
// zero-value/`=` case, so a plain-equality chip's URL is byte-identical to
// before operators existed), one comma-joined `group-by` param when
// non-empty. So the URL an operator bookmarks always has the exact shape the
// docs example shows, regardless of which control produced it.
func buildStatsURL(filters []tagFilter, groupBy []string) string {
	q := url.Values{}
	for _, f := range filters {
		op := f.Op
		if op == "" {
			op = "="
		}
		q.Add("filter", f.Key+op+f.Value)
	}
	if len(groupBy) > 0 {
		q.Set("group-by", strings.Join(groupBy, ","))
	}
	if len(q) == 0 {
		return "/stats"
	}
	return "/stats?" + q.Encode()
}

// extraGroupBy returns the currently-active group-by keys that are NOT one of
// the built-in checkbox options (i.e. a config-declared tag like `cohort` or
// `tier`), comma-joined — prefilled into the group-by selector's free-text
// "extra keys" input so resubmitting the built-in checkboxes never silently
// drops a config-tag grouping the operator already set.
func extraGroupBy(groupBy []string) string {
	builtin := map[string]bool{}
	for _, b := range builtinFacets {
		builtin[b] = true
	}
	var extra []string
	for _, k := range groupBy {
		if !builtin[k] {
			extra = append(extra, k)
		}
	}
	return strings.Join(extra, ",")
}

// toSet is a plain slice->set conversion, used to drive the group-by
// checkboxes' checked state from a template (`{{if index .GroupBySet "model"}}`).
func toSet(keys []string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// stats renders /stats: the read-only honeybee-performance view. With no
// `filter`/`group-by` query params it is byte-for-byte the pre-existing
// per-submodule + total view (computeStats, unchanged — stats-filter-groupby's
// "empty filter set + no group-by == today's default view" contract). Either
// param present switches to the filter+group-by aggregation
// (computeGroupedStats) instead: sessions surviving every filter chip,
// aggregated per distinct group-by tuple.
//
// The add-filter control posts its two plain key/value inputs as fkey/fval
// (composable chips, never a query string to type) rather than a pre-joined
// `filter=k=v`; that request is canonicalized into the standard shape and
// 303-redirected so the URL the operator lands on (and can bookmark) always
// matches the one grammar buildStatsURL emits — no JS needed anywhere on this
// page. The handler never writes anything: every branch below only reads.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	if fkey := strings.TrimSpace(r.URL.Query().Get("fkey")); fkey != "" {
		filters := append(parseFilters(r), tagFilter{Key: fkey, Op: parseFilterOp(r), Value: strings.TrimSpace(r.URL.Query().Get("fval"))})
		http.Redirect(w, r, buildStatsURL(filters, parseGroupBy(r)), http.StatusSeeOther)
		return
	}
	filters := parseFilters(r)
	groupBy := parseGroupBy(r)
	filtered := len(filters) > 0 || len(groupBy) > 0

	chips := make([]filterChip, len(filters))
	for i, f := range filters {
		rest := make([]tagFilter, 0, len(filters)-1)
		for j, o := range filters {
			if j != i {
				rest = append(rest, o)
			}
		}
		op := f.Op
		if op == "" {
			op = "="
		}
		chips[i] = filterChip{Key: f.Key, Op: op, Value: f.Value, RemoveHref: buildStatsURL(rest, groupBy)}
	}

	data := map[string]interface{}{
		"Filters":      chips,
		"GroupBy":      groupBy,
		"GroupBySet":   toSet(groupBy),
		"BuiltinTags":  builtinFacets,
		"ExtraGroupBy": extraGroupBy(groupBy),
		"Filtered":     filtered,
		"Title":        pageTitle("stats"),
		"Nav":          "stats",
	}
	if !filtered {
		subs, total, err := s.computeStats(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data["Subs"] = subs
		data["Total"] = total
	} else {
		rows, err := s.computeGroupedStats(r.Context(), filters, groupBy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data["Grouped"] = rows
	}
	s.render(w, "stats.html", data)
}
