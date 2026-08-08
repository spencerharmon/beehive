package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
)

// metrics serves a Prometheus text-exposition (version 0.0.4) snapshot of the
// hive synthesized ENTIRELY from committed git state — the same git-derived
// figures the /stats page renders, re-projected as Prometheus samples so a
// Prometheus server can scrape the swarm and PromQL/alert on it. Nothing here is
// stored or estimated: every value is read on request from PLAN.md (task status),
// the session transcripts (honeybee/model tallies), and the submodule branch refs
// (stranded work), so a sample can never drift from reality.
//
// SEMANTICS — gauge vs counter (deliberate, not cosmetic): a git snapshot is a
// point-in-time measurement of committed history, so every git-derived family is
// a GAUGE (its value can move in either direction as history advances — a DONE
// task is not monotonic across a plan rewrite/reconcile). Only the process-local
// view-cache instrumentation (lookups/misses) is a true monotonic COUNTER, named
// with the `_total` suffix Prometheus reserves for counters. This split is why
// no git-derived family carries `_total`: re-emitting a HEAD snapshot as a
// counter would let `rate()` compute nonsense off a value that legitimately
// decreases.
//
// The heavy per-model/session figures come from computeStats, which reads them
// through the HEAD-keyed viewCache (statsAggregate), so a warm scrape never
// re-walks the thousands of session-transcript headers; only ActiveNow (a
// time-dependent claim-freshness projection) is recomputed fresh, exactly as the
// /stats page does. A scrape is therefore as cheap as a warm /stats render.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head := s.headSHA(ctx)
	now, ttl := time.Now(), s.ttl()

	sms, err := s.repo.Submodules()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The time-independent, git-derived per-submodule figures (delivered/session/
	// per-model/stranded), memoized per HEAD; ActiveNow is fresh within.
	subs, _, err := s.computeStats(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byName := make(map[string]subStat, len(subs))
	for _, st := range subs {
		byName[st.Name] = st
	}

	var mw metricWriter

	// beehive_submodules — registered targets under the swarm.
	mw.family("beehive_submodules", "gauge", "Number of submodules registered in the hive.")
	mw.sample("beehive_submodules", nil, float64(len(sms)))

	// Per-submodule task-status breakdown, straight from PLAN.md. A parse error
	// on one submodule's plan omits ITS task series (never fails the scrape) —
	// the same per-submodule resilience the /stats page uses. Statuses are
	// emitted in a fixed order for deterministic output; a status with zero tasks
	// is still emitted (0) so an alert like `beehive_tasks{status="NEEDS-HUMAN"}
	// > 0` has a series to evaluate rather than a gap.
	statusOrder := []plan.Status{
		plan.StatusTODO, plan.StatusReview, plan.StatusArb,
		plan.StatusDone, plan.StatusHuman,
	}
	mw.family("beehive_tasks", "gauge", "PLAN.md task count per submodule, by status.")
	for _, sm := range sms {
		p, perr := s.planView(head, sm.PlanPath(), now, ttl)
		if perr != nil {
			continue
		}
		counts := map[plan.Status]int{}
		for _, it := range p.Items {
			counts[plan.Status(it.Status)]++
		}
		for _, st := range statusOrder {
			mw.sample("beehive_tasks",
				labels{{"submodule", sm.Name}, {"status", string(st)}},
				float64(counts[st]))
		}
	}

	// Per-submodule aggregate gauges from computeStats.
	mw.family("beehive_delivered_tasks", "gauge", "Tasks at PLAN [DONE] per submodule (delivered work).")
	for _, sm := range sms {
		mw.sample("beehive_delivered_tasks", labels{{"submodule", sm.Name}}, float64(byName[sm.Name].DeliveredTasks))
	}

	mw.family("beehive_sessions", "gauge", "Recorded honeybee session transcripts per submodule (all-time pass count).")
	for _, sm := range sms {
		mw.sample("beehive_sessions", labels{{"submodule", sm.Name}}, float64(byName[sm.Name].Honeybees))
	}

	mw.family("beehive_active_honeybees", "gauge", "Honeybees actively working a submodule right now (fresh claim within the TTL).")
	for _, sm := range sms {
		mw.sample("beehive_active_honeybees", labels{{"submodule", sm.Name}}, float64(byName[sm.Name].ActiveNow))
	}

	mw.family("beehive_stranded_tasks", "gauge", "Tasks with a stamped bee-<task> branch ahead of main that never merged (wedge indicator).")
	for _, sm := range sms {
		mw.sample("beehive_stranded_tasks", labels{{"submodule", sm.Name}}, float64(byName[sm.Name].Stranded))
	}

	// Per-model breakdowns. Model is a bounded label (a handful of agent models);
	// the transcript `model:` header is the source, unstamped history credited to
	// defaultModel by computeStats. Emitted as two families so PromQL can slice
	// throughput and yield per model (the honeybee-model-routing token lever).
	mw.family("beehive_sessions_by_model", "gauge", "Honeybee session transcripts per submodule, split by agent model.")
	mw.family("beehive_delivered_tasks_by_model", "gauge", "Delivered (DONE) tasks per submodule, attributed to the model of each task's most-recent session.")
	for _, sm := range sms {
		for _, ms := range byName[sm.Name].Models {
			lb := labels{{"submodule", sm.Name}, {"model", ms.Model}}
			mw.sample("beehive_sessions_by_model", lb, float64(ms.Honeybees))
			mw.sample("beehive_delivered_tasks_by_model", lb, float64(ms.DeliveredTasks))
		}
	}

	// View-cache instrumentation — the ONLY true monotonic counters here
	// (process-local, only ever increase over the process's life), so they carry
	// the `_total` suffix and TYPE counter. They let an operator watch the
	// parse-once cache's hit rate under scrape + dashboard load.
	mw.family("beehive_cache_lookups_total", "counter", "View-cache participating reads over the process's life (hits + misses).")
	mw.sample("beehive_cache_lookups_total", nil, float64(s.cache.Lookups()))
	mw.family("beehive_cache_misses_total", "counter", "View-cache loader invocations (misses) over the process's life.")
	mw.sample("beehive_cache_misses_total", nil, float64(s.cache.Misses()))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(mw.String()))
}

// label is one name/value pair; labelSet is an ordered list (Prometheus does not
// require label order, but a stable order keeps the exposition deterministic and
// diff-friendly).
type label struct{ name, value string }
type labels []label

// metricWriter accumulates a Prometheus text-exposition document, enforcing the
// format's grouping rule: every metric family emits its `# HELP`/`# TYPE` header
// exactly once, before any of its samples, and all samples of a family are
// contiguous. Callers declare a family once with family(), then emit any number
// of sample() rows for it; sample() panics on an undeclared family so a
// mis-wired metric fails loudly in tests rather than producing a malformed
// document a scraper would reject.
type metricWriter struct {
	order []string        // family names in declaration order
	seen  map[string]bool // declared families
	buf   map[string]*strings.Builder
}

func (m *metricWriter) family(name, typ, help string) {
	if m.seen == nil {
		m.seen = map[string]bool{}
		m.buf = map[string]*strings.Builder{}
	}
	if m.seen[name] {
		return
	}
	m.seen[name] = true
	m.order = append(m.order, name)
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP %s %s\n", name, escapeHelp(help))
	fmt.Fprintf(&b, "# TYPE %s %s\n", name, typ)
	m.buf[name] = &b
}

func (m *metricWriter) sample(name string, lb labels, val float64) {
	b, ok := m.buf[name]
	if !ok {
		panic("metricWriter: sample for undeclared family " + name)
	}
	b.WriteString(name)
	if len(lb) > 0 {
		b.WriteByte('{')
		for i, l := range lb {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(l.name)
			b.WriteString(`="`)
			b.WriteString(escapeLabelValue(l.value))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(formatFloat(val))
	b.WriteByte('\n')
}

func (m *metricWriter) String() string {
	var out strings.Builder
	for _, name := range m.order {
		out.WriteString(m.buf[name].String())
	}
	return out.String()
}

// escapeLabelValue escapes a label value per the Prometheus text format: a
// backslash, a double-quote, and a line feed are the three characters that must
// be escaped (a HELP text additionally escapes backslash and newline — see
// escapeHelp).
func escapeLabelValue(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// escapeHelp escapes a HELP string: backslash and newline only (a double-quote
// is literal in HELP text, unlike a label value).
func escapeHelp(v string) string {
	if !strings.ContainsAny(v, "\\\n") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	return r.Replace(v)
}

// formatFloat renders a metric value the way Prometheus expects: an integer
// value without a decimal point, a fractional value at full precision.
func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
