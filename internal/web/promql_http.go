package web

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"time"

	"github.com/spencerharmon/beehive/internal/version"
)

// ============================================================================
// Prometheus HTTP API v1 surface, mounted under /prometheus/. Enough of the API
// for a Grafana Prometheus datasource: instant + range query, label discovery,
// series, metadata, buildinfo, and health. All answers are computed live from
// the git-reconstructed series set (hiveSeries) — no external store.
// ============================================================================

const maxRangeSteps = 11000 // Prometheus' own cap; guards abusive step counts

// promResult is the standard Prometheus API envelope.
type promResult struct {
	Status    string      `json:"status"`
	Data      interface{} `json:"data,omitempty"`
	ErrorType string      `json:"errorType,omitempty"`
	Error     string      `json:"error,omitempty"`
}

func writeProm(w http.ResponseWriter, code int, r promResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(r)
}

func promErr(w http.ResponseWriter, code int, errType, msg string) {
	writeProm(w, code, promResult{Status: "error", ErrorType: errType, Error: msg})
}

// metricMap renders a sample's identity as Prometheus' {__name__:.., labels..}.
func metricMap(name string, lb labels) map[string]string {
	m := make(map[string]string, len(lb)+1)
	if name != "" {
		m["__name__"] = name
	}
	for _, l := range lb {
		m[l.name] = l.value
	}
	return m
}

// fmtVal encodes a float the way the Prometheus API does (string), handling the
// non-finite specials PromQL can produce.
func fmtVal(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// queryParam reads a parameter from either the query string or (for POST) the
// form body, matching Prometheus' accept-both behavior so Grafana's POST queries
// work.
func queryParam(r *http.Request, key string) string {
	if v := r.FormValue(key); v != "" {
		return v
	}
	return r.URL.Query().Get(key)
}

func parseTimeParam(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, nsec), nil
	}
	return time.Parse(time.RFC3339, s)
}

func parseStepParam(s string) (time.Duration, error) {
	if s == "" {
		return 0, evalError{"missing step"}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(f * float64(time.Second)), nil
	}
	return parsePromDuration(s)
}

// promInstantQuery handles GET/POST /prometheus/api/v1/query.
func (s *Server) promInstantQuery(w http.ResponseWriter, r *http.Request) {
	q := queryParam(r, "query")
	if q == "" {
		promErr(w, http.StatusBadRequest, "bad_data", "missing query")
		return
	}
	ts, err := parseTimeParam(queryParam(r, "time"), time.Now())
	if err != nil {
		promErr(w, http.StatusBadRequest, "bad_data", "bad time: "+err.Error())
		return
	}
	eng := &engine{all: s.hiveSeries(r.Context())}
	val, err := eng.eval(q, ts.Unix())
	if err != nil {
		promErr(w, http.StatusUnprocessableEntity, "execution", err.Error())
		return
	}
	if val.isScalar {
		writeProm(w, http.StatusOK, promResult{Status: "success", Data: map[string]interface{}{
			"resultType": "scalar",
			"result":     []interface{}{float64(ts.Unix()), fmtVal(val.scalar)},
		}})
		return
	}
	result := make([]map[string]interface{}, 0, len(val.vec))
	for _, sp := range val.vec {
		result = append(result, map[string]interface{}{
			"metric": metricMap(sp.Name, sp.Labels),
			"value":  []interface{}{float64(ts.Unix()), fmtVal(sp.Value)},
		})
	}
	writeProm(w, http.StatusOK, promResult{Status: "success", Data: map[string]interface{}{
		"resultType": "vector",
		"result":     result,
	}})
}

// promRangeQuery handles GET/POST /prometheus/api/v1/query_range — the engine is
// evaluated at each step against the git-reconstructed series (so the time axis
// is real git history).
func (s *Server) promRangeQuery(w http.ResponseWriter, r *http.Request) {
	q := queryParam(r, "query")
	if q == "" {
		promErr(w, http.StatusBadRequest, "bad_data", "missing query")
		return
	}
	start, err := parseTimeParam(queryParam(r, "start"), time.Time{})
	if err != nil || queryParam(r, "start") == "" {
		promErr(w, http.StatusBadRequest, "bad_data", "bad or missing start")
		return
	}
	end, err := parseTimeParam(queryParam(r, "end"), time.Time{})
	if err != nil || queryParam(r, "end") == "" {
		promErr(w, http.StatusBadRequest, "bad_data", "bad or missing end")
		return
	}
	step, err := parseStepParam(queryParam(r, "step"))
	if err != nil || step <= 0 {
		promErr(w, http.StatusBadRequest, "bad_data", "bad or missing step")
		return
	}
	if end.Before(start) {
		promErr(w, http.StatusBadRequest, "bad_data", "end before start")
		return
	}
	if end.Sub(start)/step > maxRangeSteps {
		promErr(w, http.StatusBadRequest, "bad_data", "too many steps (exceeds "+strconv.Itoa(maxRangeSteps)+")")
		return
	}
	eng := &engine{all: s.hiveSeries(r.Context())}

	// Accumulate a matrix: signature -> {metric, ordered [ts,val] points}.
	type matrixSeries struct {
		metric map[string]string
		values [][]interface{}
	}
	acc := map[string]*matrixSeries{}
	var order []string
	stepSec := int64(step.Seconds())
	if stepSec <= 0 {
		stepSec = 1
	}
	for t := start.Unix(); t <= end.Unix(); t += stepSec {
		val, err := eng.eval(q, t)
		if err != nil {
			promErr(w, http.StatusUnprocessableEntity, "execution", err.Error())
			return
		}
		if val.isScalar {
			key := "__scalar__"
			ms := acc[key]
			if ms == nil {
				ms = &matrixSeries{metric: map[string]string{}}
				acc[key] = ms
				order = append(order, key)
			}
			ms.values = append(ms.values, []interface{}{float64(t), fmtVal(val.scalar)})
			continue
		}
		for _, sp := range val.vec {
			key := sigKey(sp.Name, sp.Labels)
			ms := acc[key]
			if ms == nil {
				ms = &matrixSeries{metric: metricMap(sp.Name, sp.Labels)}
				acc[key] = ms
				order = append(order, key)
			}
			ms.values = append(ms.values, []interface{}{float64(t), fmtVal(sp.Value)})
		}
	}
	result := make([]map[string]interface{}, 0, len(order))
	for _, key := range order {
		ms := acc[key]
		result = append(result, map[string]interface{}{
			"metric": ms.metric,
			"values": ms.values,
		})
	}
	writeProm(w, http.StatusOK, promResult{Status: "success", Data: map[string]interface{}{
		"resultType": "matrix",
		"result":     result,
	}})
}

// currentSamples returns the instant vector at now — the basis for label/series
// discovery endpoints.
func (s *Server) currentSamples(ctx context.Context) []promSample {
	eng := &engine{all: s.hiveSeries(ctx)}
	return eng.instantVector(time.Now().Unix())
}

// promLabelValues handles /prometheus/api/v1/label/{name}/values.
func (s *Server) promLabelValues(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	set := map[string]bool{}
	for _, sp := range s.currentSamples(r.Context()) {
		if name == "__name__" {
			set[sp.Name] = true
			continue
		}
		if v := labelVal(sp.Labels, name); v != "" {
			set[v] = true
		}
	}
	vals := make([]string, 0, len(set))
	for v := range set {
		vals = append(vals, v)
	}
	sort.Strings(vals)
	writeProm(w, http.StatusOK, promResult{Status: "success", Data: vals})
}

// promLabels handles /prometheus/api/v1/labels.
func (s *Server) promLabels(w http.ResponseWriter, r *http.Request) {
	set := map[string]bool{"__name__": true}
	for _, sp := range s.currentSamples(r.Context()) {
		for _, l := range sp.Labels {
			set[l.name] = true
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	writeProm(w, http.StatusOK, promResult{Status: "success", Data: names})
}

// promSeries handles /prometheus/api/v1/series?match[]=<selector> — returns the
// label sets of series matching any selector.
func (s *Server) promSeries(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	matches := r.Form["match[]"]
	samples := s.currentSamples(r.Context())
	var out []map[string]string
	seen := map[string]bool{}
	emit := func(sp promSample) {
		k := sigKey(sp.Name, sp.Labels)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, metricMap(sp.Name, sp.Labels))
	}
	if len(matches) == 0 {
		for _, sp := range samples {
			emit(sp)
		}
	} else {
		for _, mexpr := range matches {
			toks, err := lex(mexpr)
			if err != nil {
				promErr(w, http.StatusBadRequest, "bad_data", err.Error())
				return
			}
			pp := &parser{toks: toks}
			nd, err := pp.parseSelector()
			if err != nil {
				promErr(w, http.StatusBadRequest, "bad_data", err.Error())
				return
			}
			for _, sp := range samples {
				if nd.name != "" && sp.Name != nd.name {
					continue
				}
				if matchAll(nd.matchers, sp) {
					emit(sp)
				}
			}
		}
	}
	writeProm(w, http.StatusOK, promResult{Status: "success", Data: out})
}

// promMetadata handles /prometheus/api/v1/metadata — the family help/type table.
func (s *Server) promMetadata(w http.ResponseWriter, r *http.Request) {
	data := map[string][]map[string]string{}
	for _, f := range metricFamilies {
		typ := f.typ
		data[f.name] = []map[string]string{{"type": typ, "help": f.help, "unit": ""}}
	}
	writeProm(w, http.StatusOK, promResult{Status: "success", Data: data})
}

// promBuildInfo handles /prometheus/api/v1/status/buildinfo — Grafana probes this
// to detect a Prometheus datasource and its feature level.
func (s *Server) promBuildInfo(w http.ResponseWriter, r *http.Request) {
	rev, _ := version.Build()
	writeProm(w, http.StatusOK, promResult{Status: "success", Data: map[string]string{
		"version":   "2.54.1",
		"revision":  rev,
		"branch":    "beehive",
		"buildUser": "beehive",
		"buildDate": "",
		"goVersion": runtime.Version(),
	}})
}

// promHealth handles /prometheus/-/healthy and /-/ready.
func (s *Server) promHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("beehive promql api ok\n"))
}
