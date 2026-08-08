package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
)

// famMeta describes one metric family: its name, Prometheus TYPE, HELP text, and
// whether beehive can synthesize a real HISTORICAL time series for it from git
// (Historical) or it is only meaningful at the present instant (a live claim /
// branch / process-cache reading that git history does not carry). The query API
// (promapi.go) uses Historical to decide which families get a git-derived step
// function across time versus a single now-point.
type famMeta struct {
	name       string
	typ        string
	help       string
	historical bool
}

// metricFamilies is the single source of truth for the beehive metric surface —
// consumed by both the /prometheus/metrics text view and the /prometheus/api/v1
// PromQL query engine. Order is the text-exposition emission order.
var metricFamilies = []famMeta{
	{"beehive_submodules", "gauge", "Number of submodules registered in the hive.", false},
	{"beehive_tasks", "gauge", "PLAN.md task count per submodule, by status.", true},
	{"beehive_delivered_tasks", "gauge", "Tasks at PLAN [DONE] per submodule (delivered work).", true},
	{"beehive_sessions", "gauge", "Recorded honeybee session transcripts per submodule (all-time pass count).", true},
	{"beehive_active_honeybees", "gauge", "Honeybees actively working a submodule right now (fresh claim within the TTL).", false},
	{"beehive_stranded_tasks", "gauge", "Tasks with a stamped bee-<task> branch ahead of main that never merged (wedge indicator).", false},
	{"beehive_sessions_by_model", "gauge", "Honeybee session transcripts per submodule, split by agent model.", false},
	{"beehive_delivered_tasks_by_model", "gauge", "Delivered (DONE) tasks per submodule, attributed to the model of each task's most-recent session.", false},
	{"beehive_cache_lookups_total", "counter", "View-cache participating reads over the process's life (hits + misses).", false},
	{"beehive_cache_misses_total", "counter", "View-cache loader invocations (misses) over the process's life.", false},
}

func famByName(name string) (famMeta, bool) {
	for _, f := range metricFamilies {
		if f.name == name {
			return f, true
		}
	}
	return famMeta{}, false
}

// promSample is one metric sample: a family name, its label set (NOT including
// __name__), and a float value. It is the common currency between the git-derived
// synthesis (liveSamples / the history builder) and the PromQL engine.
type promSample struct {
	Name   string
	Labels labels // sorted by name; excludes __name__
	Value  float64
}

// liveSamples synthesizes the CURRENT value of every metric family from committed
// git state at HEAD — the same figures /stats renders (via computeStats + the
// HEAD-keyed viewCache), plus per-submodule task-status counts from PLAN.md and
// the process-local view-cache counters. This is the now-snapshot: the text
// endpoint renders it verbatim, and the query engine uses it as each historical
// family's latest point and as the ONLY point for the instant-only families.
func (s *Server) liveSamples(ctx context.Context) []promSample {
	head := s.headSHA(ctx)
	now, ttl := time.Now(), s.ttl()

	sms, err := s.repo.Submodules()
	if err != nil {
		return nil
	}
	subs, _, err := s.computeStats(ctx)
	if err != nil {
		return nil
	}
	byName := make(map[string]subStat, len(subs))
	for _, st := range subs {
		byName[st.Name] = st
	}

	var out []promSample
	add := func(name string, lb labels, v float64) {
		out = append(out, promSample{Name: name, Labels: lb, Value: v})
	}

	add("beehive_submodules", nil, float64(len(sms)))

	statusOrder := []plan.Status{
		plan.StatusTODO, plan.StatusReview, plan.StatusArb,
		plan.StatusDone, plan.StatusHuman,
	}
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
			add("beehive_tasks", labels{{"submodule", sm.Name}, {"status", string(st)}}, float64(counts[st]))
		}
	}
	for _, sm := range sms {
		st := byName[sm.Name]
		add("beehive_delivered_tasks", labels{{"submodule", sm.Name}}, float64(st.DeliveredTasks))
		add("beehive_sessions", labels{{"submodule", sm.Name}}, float64(st.Honeybees))
		add("beehive_active_honeybees", labels{{"submodule", sm.Name}}, float64(st.ActiveNow))
		add("beehive_stranded_tasks", labels{{"submodule", sm.Name}}, float64(st.Stranded))
		for _, ms := range st.Models {
			lb := labels{{"submodule", sm.Name}, {"model", ms.Model}}
			add("beehive_sessions_by_model", lb, float64(ms.Honeybees))
			add("beehive_delivered_tasks_by_model", lb, float64(ms.DeliveredTasks))
		}
	}
	add("beehive_cache_lookups_total", nil, float64(s.cache.Lookups()))
	add("beehive_cache_misses_total", nil, float64(s.cache.Misses()))
	return out
}

// metrics serves the Prometheus text-exposition (v0.0.4) snapshot at
// /prometheus/metrics — a human/debug view of liveSamples. The real query
// surface is /prometheus/api/v1 (promapi.go); this endpoint is NOT scraped by any
// external Prometheus (beehive's design is a direct PromQL API over git, no TSDB).
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	samples := s.liveSamples(r.Context())
	byFam := map[string][]promSample{}
	for _, sp := range samples {
		byFam[sp.Name] = append(byFam[sp.Name], sp)
	}

	var mw metricWriter
	for _, f := range metricFamilies {
		mw.family(f.name, f.typ, f.help)
		for _, sp := range byFam[f.name] {
			mw.sample(sp.Name, sp.Labels, sp.Value)
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(mw.String()))
}

// label is one name/value pair; labels is an ordered set kept sorted by name so a
// label signature is canonical (used for vector matching in the query engine) and
// the text exposition is deterministic.
type label struct{ name, value string }
type labels []label

// metricWriter accumulates a Prometheus text-exposition document, enforcing the
// format's grouping rule: every family emits its `# HELP`/`# TYPE` header exactly
// once before any of its samples. sample() panics on an undeclared family so a
// mis-wired metric fails loudly in tests rather than emitting a malformed document.
type metricWriter struct {
	order []string
	seen  map[string]bool
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

// escapeLabelValue escapes a label value per the Prometheus text format:
// backslash, double-quote, and line feed.
func escapeLabelValue(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// escapeHelp escapes a HELP string: backslash and newline only (a double-quote is
// literal in HELP text, unlike a label value).
func escapeHelp(v string) string {
	if !strings.ContainsAny(v, "\\\n") {
		return v
	}
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(v)
}

// formatFloat renders a metric value the text format's way: an integer without a
// decimal point, a fraction at full precision.
func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
