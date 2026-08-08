package web

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
)

// ============================================================================
// beehive PromQL API — a Prometheus-compatible query surface whose ONLY backing
// store is the git repo. There is no TSDB, no scrape, no remote_write: every
// instant value is synthesized from committed state (liveSamples), and every
// TIME SERIES is reconstructed from git history — commit timestamps ARE the time
// axis. A Grafana Prometheus datasource pointed at http://<host>:8955/prometheus
// queries this engine directly.
//
// Two classes of family (metricFamilies.historical):
//   - HISTORICAL (beehive_tasks, beehive_delivered_tasks, beehive_sessions):
//     beehive replays git history into a real step function — PLAN.md parsed at a
//     daily sample of commits (task/delivered counts as they actually were), and
//     the session transcripts' add-commit times accumulated. So range queries and
//     rate()/increase() over these are REAL history, not a flat line.
//   - INSTANT-ONLY (active honeybees, stranded branches, per-model split, the
//     process view-cache counters): a live claim/branch/process reading git
//     history does not carry. These get a single point at "now"; a range query
//     returns them only at steps at/after that point (never a fabricated past).
//
// The reconstructed series set is memoized for a short TTL (git history moves
// slowly relative to the per-heartbeat commit rate, so HEAD-keying would thrash);
// the first query computes it synchronously (real data), later queries serve it
// while a single background goroutine refreshes.
// ============================================================================

// point is one (unix-seconds, value) sample of a series.
type point struct {
	t int64
	v float64
}

// series is one metric time series: a family name, a canonical (sorted) label
// set excluding __name__, and its points sorted ascending by time.
type series struct {
	name   string
	labels labels
	pts    []point
}

// valueAt returns the series value as-of unix time t (the last point at or before
// t) and whether such a point exists. A query at a time before the series' first
// point yields no sample (honest absence, never an invented zero).
func (s *series) valueAt(t int64) (float64, bool) {
	lo, hi := 0, len(s.pts)-1
	idx := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if s.pts[mid].t <= t {
			idx = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if idx < 0 {
		return 0, false
	}
	return s.pts[idx].v, true
}

func sigKey(name string, lb labels) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('\xff')
	for _, l := range lb {
		b.WriteString(l.name)
		b.WriteByte('=')
		b.WriteString(l.value)
		b.WriteByte('\xff')
	}
	return b.String()
}

func sortedLabels(lb labels) labels {
	out := make(labels, len(lb))
	copy(out, lb)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// ---------------------------------------------------------------------------
// History reconstruction from git.
// ---------------------------------------------------------------------------

const historyTTL = 30 * time.Second
const historyMaxDays = 400 // bound the daily PLAN sampling window

// hiveSeries returns the full reconstructed series set (historical step functions
// + instant now-points), memoized for historyTTL on the Server's own promHist
// lock. The cold build runs WITHOUT holding the lock (buildHiveSeries re-enters
// the viewCache, whose cachedTTL loader holds ITS lock — sharing a lock would
// deadlock). A stale hit serves the last value and refreshes in one background
// goroutine; only the very first query for a fresh process blocks on the build.
func (s *Server) hiveSeries(ctx context.Context) []series {
	s.promHistMu.Lock()
	now := time.Now()
	if s.promHist != nil && now.Before(s.promHistExp) {
		cur := s.promHist
		s.promHistMu.Unlock()
		return cur
	}
	if s.promHist != nil {
		cur := s.promHist
		if !s.promHistRun {
			s.promHistRun = true
			go func() {
				v := s.buildHiveSeries(context.Background())
				s.promHistMu.Lock()
				s.promHist = v
				s.promHistExp = time.Now().Add(historyTTL)
				s.promHistRun = false
				s.promHistMu.Unlock()
			}()
		}
		s.promHistMu.Unlock()
		return cur
	}
	s.promHistMu.Unlock()
	// Cold: compute synchronously without holding promHistMu.
	v := s.buildHiveSeries(ctx)
	s.promHistMu.Lock()
	s.promHist = v
	s.promHistExp = time.Now().Add(historyTTL)
	s.promHistMu.Unlock()
	return v
}

func (s *Server) buildHiveSeries(ctx context.Context) []series {
	byKey := map[string]*series{}
	get := func(name string, lb labels) *series {
		lb = sortedLabels(lb)
		k := sigKey(name, lb)
		se := byKey[k]
		if se == nil {
			se = &series{name: name, labels: lb}
			byKey[k] = se
		}
		return se
	}

	sms, err := s.repo.Submodules()
	if err == nil {
		for _, sm := range sms {
			s.buildPlanHistory(ctx, sm.Name, s.relPath(sm.PlanPath()), get)
			s.buildSessionHistory(ctx, sm.Name, s.relPath(sm.SessionsDir()), get)
		}
	}

	// Append the exact CURRENT value of every family as a now-point. For a
	// historical family this pins the series' latest value to the live figure
	// (the daily PLAN sample can lag the newest commit); for an instant-only
	// family this is its sole point.
	now := time.Now().Unix()
	for _, sp := range s.liveSamples(ctx) {
		get(sp.Name, sp.Labels).pts = append(get(sp.Name, sp.Labels).pts, point{now, sp.Value})
	}

	out := make([]series, 0, len(byKey))
	for _, se := range byKey {
		sort.Slice(se.pts, func(i, j int) bool { return se.pts[i].t < se.pts[j].t })
		// Collapse points sharing a timestamp (keep the last), so valueAt's
		// binary search is over strictly increasing times.
		dedup := se.pts[:0]
		for i, p := range se.pts {
			if i > 0 && dedup[len(dedup)-1].t == p.t {
				dedup[len(dedup)-1] = p
				continue
			}
			dedup = append(dedup, p)
		}
		se.pts = dedup
		out = append(out, *se)
	}
	return out
}

// relPath maps a hive-absolute submodule path to a repo-relative path git can
// address (submodules/<name>/PLAN.md). Submodule paths that are already
// repo-relative pass through unchanged.
func (s *Server) relPath(p string) string {
	root := s.repo.Root
	if strings.HasPrefix(p, root+"/") {
		return p[len(root)+1:]
	}
	return p
}

// buildPlanHistory reconstructs beehive_tasks{submodule,status} and
// beehive_delivered_tasks{submodule} for one submodule by parsing PLAN.md at a
// DAILY sample of the commits that touched it — real historical counts at real
// commits, at day resolution (the useful granularity for a progress dashboard,
// and a hard bound on how many blobs it parses).
func (s *Server) buildPlanHistory(ctx context.Context, sm, planRel string, get func(string, labels) *series) {
	// All commits that touched this PLAN.md, oldest→newest, with commit time.
	out, err := s.git.Run(ctx, "log", "--reverse", "--format=%H %ct", "--", planRel)
	if err != nil {
		return
	}
	type commit struct {
		sha string
		ct  int64
	}
	var commits []commit
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		ct, e := strconv.ParseInt(f[1], 10, 64)
		if e != nil {
			continue
		}
		commits = append(commits, commit{f[0], ct})
	}
	if len(commits) == 0 {
		return
	}
	// Keep the LAST commit of each UTC day (day-resolution step function),
	// bounded to the most recent historyMaxDays days.
	lastPerDay := map[int64]commit{}
	var days []int64
	for _, c := range commits {
		day := c.ct / 86400
		if _, ok := lastPerDay[day]; !ok {
			days = append(days, day)
		}
		lastPerDay[day] = c
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	if len(days) > historyMaxDays {
		days = days[len(days)-historyMaxDays:]
	}
	statuses := []plan.Status{plan.StatusTODO, plan.StatusReview, plan.StatusArb, plan.StatusDone, plan.StatusHuman}
	for _, day := range days {
		c := lastPerDay[day]
		content, e := s.git.Run(ctx, "show", c.sha+":"+planRel)
		if e != nil {
			continue
		}
		parsed, e := plan.Parse(content)
		if e != nil || parsed == nil {
			continue
		}
		counts := map[plan.Status]int{}
		for _, tk := range parsed.Tasks {
			counts[tk.Status]++
		}
		for _, st := range statuses {
			get("beehive_tasks", labels{{"submodule", sm}, {"status", string(st)}}).pts =
				append(get("beehive_tasks", labels{{"submodule", sm}, {"status", string(st)}}).pts,
					point{c.ct, float64(counts[st])})
		}
		get("beehive_delivered_tasks", labels{{"submodule", sm}}).pts =
			append(get("beehive_delivered_tasks", labels{{"submodule", sm}}).pts,
				point{c.ct, float64(counts[plan.StatusDone])})
	}
}

// buildSessionHistory reconstructs beehive_sessions{submodule} as the cumulative
// count of session transcripts over time, from each transcript's ADD-commit time
// (sessions are append-only, so an exact cumulative — no per-commit parse needed).
func (s *Server) buildSessionHistory(ctx context.Context, sm, sessionsRel string, get func(string, labels) *series) {
	out, err := s.git.Run(ctx, "log", "--diff-filter=A", "--format=C%ct", "--name-only", "--", sessionsRel)
	if err != nil {
		return
	}
	type add struct {
		ct int64
		n  int
	}
	var cur int64
	perCommit := map[int64]int{}
	var order []int64
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "C") {
			if ct, e := strconv.ParseInt(ln[1:], 10, 64); e == nil {
				cur = ct
			}
			continue
		}
		if strings.HasSuffix(ln, ".md") && strings.Contains(ln, "/sessions/") {
			if _, ok := perCommit[cur]; !ok {
				order = append(order, cur)
			}
			perCommit[cur]++
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	adds := make([]add, 0, len(order))
	for _, ct := range order {
		adds = append(adds, add{ct, perCommit[ct]})
	}
	total := 0
	for _, a := range adds {
		total += a.n
		get("beehive_sessions", labels{{"submodule", sm}}).pts =
			append(get("beehive_sessions", labels{{"submodule", sm}}).pts, point{a.ct, float64(total)})
	}
}

// ---------------------------------------------------------------------------
// PromQL engine (a focused, correct subset).
// ---------------------------------------------------------------------------

type engine struct {
	all []series
}

// instantVector returns every series' value as-of t (skipping series with no
// point at/before t), as an instant vector.
func (e *engine) instantVector(t int64) []promSample {
	out := make([]promSample, 0, len(e.all))
	for i := range e.all {
		if v, ok := e.all[i].valueAt(t); ok {
			out = append(out, promSample{Name: e.all[i].name, Labels: e.all[i].labels, Value: v})
		}
	}
	return out
}

type evalError struct{ msg string }

func (e evalError) Error() string { return e.msg }

// value is a PromQL evaluation result: either a scalar or an instant vector.
type value struct {
	scalar   float64
	isScalar bool
	vec      []promSample
}

// eval parses and evaluates expr at instant time t (unix seconds).
func (e *engine) eval(expr string, t int64) (value, error) {
	toks, err := lex(expr)
	if err != nil {
		return value{}, err
	}
	p := &parser{toks: toks}
	node, err := p.parseExpr()
	if err != nil {
		return value{}, err
	}
	if p.pos != len(p.toks) {
		return value{}, evalError{"unexpected trailing input in query"}
	}
	return e.evalNode(node, t)
}

func (e *engine) evalNode(n *node, t int64) (value, error) {
	switch n.kind {
	case nNum:
		return value{scalar: n.num, isScalar: true}, nil
	case nSelector:
		return value{vec: e.selectVec(n, t)}, nil
	case nRange:
		return value{}, evalError{"range vector not allowed here (only inside rate/increase/delta)"}
	case nUnary:
		v, err := e.evalNode(n.lhs, t)
		if err != nil {
			return value{}, err
		}
		if v.isScalar {
			v.scalar = -v.scalar
			return v, nil
		}
		for i := range v.vec {
			v.vec[i].Value = -v.vec[i].Value
		}
		return v, nil
	case nCall:
		return e.evalCall(n, t)
	case nAgg:
		return e.evalAgg(n, t)
	case nBinary:
		return e.evalBinary(n, t)
	}
	return value{}, evalError{"unknown expression node"}
}

func (e *engine) selectVec(n *node, t int64) []promSample {
	var out []promSample
	for _, s := range e.instantVector(t) {
		if n.name != "" && s.Name != n.name {
			continue
		}
		if matchAll(n.matchers, s) {
			out = append(out, s)
		}
	}
	return out
}

func matchAll(ms []matcher, s promSample) bool {
	for _, m := range ms {
		var lv string
		if m.name == "__name__" {
			lv = s.Name
		} else {
			lv = labelVal(s.Labels, m.name)
		}
		if !m.matches(lv) {
			return false
		}
	}
	return true
}

func labelVal(lb labels, name string) string {
	for _, l := range lb {
		if l.name == name {
			return l.value
		}
	}
	return ""
}

func (e *engine) evalCall(n *node, t int64) (value, error) {
	switch n.fn {
	case "rate", "increase", "delta":
		if len(n.args) != 1 || n.args[0].kind != nRange {
			return value{}, evalError{n.fn + "() expects a range-vector argument like metric[5m]"}
		}
		rs := n.args[0]
		dur := int64(rs.dur.Seconds())
		if dur <= 0 {
			return value{}, evalError{n.fn + "(): range duration must be positive"}
		}
		var out []promSample
		for i := range e.all {
			s := &e.all[i]
			if rs.lhs.name != "" && s.name != rs.lhs.name {
				continue
			}
			smp := promSample{Name: s.name, Labels: s.labels}
			if !matchAll(rs.lhs.matchers, smp) {
				continue
			}
			v1, ok1 := s.valueAt(t)
			v0, ok0 := s.valueAt(t - dur)
			if !ok1 || !ok0 {
				continue
			}
			delta := v1 - v0
			val := delta
			if n.fn == "rate" {
				val = delta / float64(dur)
			}
			// rate/increase/delta drop __name__.
			out = append(out, promSample{Labels: s.labels, Value: val})
		}
		return value{vec: out}, nil
	case "scalar":
		if len(n.args) != 1 {
			return value{}, evalError{"scalar() expects one argument"}
		}
		v, err := e.evalNode(n.args[0], t)
		if err != nil {
			return value{}, err
		}
		if v.isScalar {
			return v, nil
		}
		if len(v.vec) != 1 {
			return value{scalar: math.NaN(), isScalar: true}, nil
		}
		return value{scalar: v.vec[0].Value, isScalar: true}, nil
	}
	return value{}, evalError{"unsupported function " + n.fn + "()"}
}

func (e *engine) evalAgg(n *node, t int64) (value, error) {
	arg, err := e.evalNode(n.lhs, t)
	if err != nil {
		return value{}, err
	}
	if arg.isScalar {
		return value{}, evalError{n.op + "() expects a vector"}
	}
	// Parameter (topk/bottomk) is the k count.
	k := 0
	if n.param != nil {
		pv, err := e.evalNode(n.param, t)
		if err != nil {
			return value{}, err
		}
		if !pv.isScalar {
			return value{}, evalError{n.op + "() parameter must be a scalar"}
		}
		k = int(pv.scalar)
	}

	type grp struct {
		labels  labels
		samples []promSample
	}
	groups := map[string]*grp{}
	var order []string
	for _, s := range arg.vec {
		gl := groupLabels(s.Labels, n.grouping, n.without)
		key := sigKey("", gl)
		g := groups[key]
		if g == nil {
			g = &grp{labels: gl}
			groups[key] = g
			order = append(order, key)
		}
		g.samples = append(g.samples, s)
	}

	var out []promSample
	for _, key := range order {
		g := groups[key]
		switch n.op {
		case "sum", "avg", "min", "max", "count", "group", "stddev":
			out = append(out, promSample{Labels: g.labels, Value: reduce(n.op, g.samples)})
		case "topk", "bottomk":
			ss := make([]promSample, len(g.samples))
			copy(ss, g.samples)
			sort.SliceStable(ss, func(i, j int) bool {
				if n.op == "topk" {
					return ss[i].Value > ss[j].Value
				}
				return ss[i].Value < ss[j].Value
			})
			if k < len(ss) {
				ss = ss[:k]
			}
			// topk/bottomk keep the ORIGINAL series' labels, not the grouping.
			out = append(out, ss...)
		default:
			return value{}, evalError{"unsupported aggregation " + n.op}
		}
	}
	return value{vec: out}, nil
}

func reduce(op string, ss []promSample) float64 {
	switch op {
	case "count":
		return float64(len(ss))
	case "group":
		return 1
	}
	if len(ss) == 0 {
		return 0
	}
	sum := 0.0
	min, max := ss[0].Value, ss[0].Value
	for _, s := range ss {
		sum += s.Value
		if s.Value < min {
			min = s.Value
		}
		if s.Value > max {
			max = s.Value
		}
	}
	switch op {
	case "sum":
		return sum
	case "avg":
		return sum / float64(len(ss))
	case "min":
		return min
	case "max":
		return max
	case "stddev":
		mean := sum / float64(len(ss))
		var acc float64
		for _, s := range ss {
			d := s.Value - mean
			acc += d * d
		}
		return math.Sqrt(acc / float64(len(ss)))
	}
	return sum
}

func groupLabels(lb labels, grouping []string, without bool) labels {
	in := map[string]bool{}
	for _, g := range grouping {
		in[g] = true
	}
	var out labels
	if without {
		for _, l := range lb {
			if !in[l.name] {
				out = append(out, l)
			}
		}
	} else {
		for _, l := range lb {
			if in[l.name] {
				out = append(out, l)
			}
		}
	}
	return sortedLabels(out)
}

func (e *engine) evalBinary(n *node, t int64) (value, error) {
	lv, err := e.evalNode(n.lhs, t)
	if err != nil {
		return value{}, err
	}
	rv, err := e.evalNode(n.rhs, t)
	if err != nil {
		return value{}, err
	}
	// scalar ⊗ scalar
	if lv.isScalar && rv.isScalar {
		res, keep := applyOp(n.op, lv.scalar, rv.scalar, n.boolMod)
		if isComparison(n.op) && !n.boolMod {
			if !keep {
				return value{scalar: math.NaN(), isScalar: true}, nil
			}
			return value{scalar: lv.scalar, isScalar: true}, nil
		}
		return value{scalar: res, isScalar: true}, nil
	}
	// vector ⊗ scalar / scalar ⊗ vector
	if lv.isScalar != rv.isScalar {
		vec := rv.vec
		scalar := lv.scalar
		scalarLeft := true
		if rv.isScalar {
			vec = lv.vec
			scalar = rv.scalar
			scalarLeft = false
		}
		var out []promSample
		for _, s := range vec {
			a, b := s.Value, scalar
			if scalarLeft {
				a, b = scalar, s.Value
			}
			res, keep := applyOp(n.op, a, b, n.boolMod)
			if isComparison(n.op) && !n.boolMod {
				if keep {
					out = append(out, s)
				}
				continue
			}
			out = append(out, promSample{Name: s.Name, Labels: s.Labels, Value: res})
		}
		return value{vec: out}, nil
	}
	// vector ⊗ vector — match on identical label signature (excluding __name__).
	rhs := map[string]promSample{}
	for _, s := range rv.vec {
		rhs[sigKey("", s.Labels)] = s
	}
	var out []promSample
	for _, s := range lv.vec {
		m, ok := rhs[sigKey("", s.Labels)]
		if !ok {
			continue
		}
		res, keep := applyOp(n.op, s.Value, m.Value, n.boolMod)
		if isComparison(n.op) && !n.boolMod {
			if keep {
				out = append(out, promSample{Labels: s.Labels, Value: s.Value})
			}
			continue
		}
		out = append(out, promSample{Labels: s.Labels, Value: res})
	}
	return value{vec: out}, nil
}

func isComparison(op string) bool {
	switch op {
	case "==", "!=", ">", "<", ">=", "<=":
		return true
	}
	return false
}

// applyOp computes a binary op. For a comparison it returns (1-or-0, keep) where
// keep is whether the comparison held (used to filter when `bool` is absent).
func applyOp(op string, a, b float64, boolMod bool) (float64, bool) {
	switch op {
	case "+":
		return a + b, true
	case "-":
		return a - b, true
	case "*":
		return a * b, true
	case "/":
		return a / b, true
	case "%":
		return math.Mod(a, b), true
	case "==":
		return boolVal(a == b), a == b
	case "!=":
		return boolVal(a != b), a != b
	case ">":
		return boolVal(a > b), a > b
	case "<":
		return boolVal(a < b), a < b
	case ">=":
		return boolVal(a >= b), a >= b
	case "<=":
		return boolVal(a <= b), a <= b
	}
	return math.NaN(), false
}

func boolVal(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
