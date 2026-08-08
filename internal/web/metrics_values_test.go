package web

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// sampleIndex indexes a liveSamples slice by its canonical (sorted-label)
// signature so a test can assert an exact value per family+labelset.
func sampleIndex(ss []promSample) map[string]float64 {
	m := make(map[string]float64, len(ss))
	for _, sp := range ss {
		m[sigKey(sp.Name, sortedLabels(sp.Labels))] = sp.Value
	}
	return m
}

// TestLiveSamplesValues verifies EVERY metric family synthesizes the exact value
// implied by the committed alpha fixture (t1 TODO, t2 NEEDS-HUMAN, t3 DONE; no
// session transcripts, no stranded branches, no fresh claim). This is the
// ground-truth check that the git-derived numbers are the real numbers, not
// plausible-looking ones.
func TestLiveSamplesValues(t *testing.T) {
	s, _ := setup(t)
	idx := sampleIndex(s.liveSamples(context.Background()))

	sig := func(name string, lb labels) string { return sigKey(name, sortedLabels(lb)) }
	al := func(name, status string) string {
		return sig(name, labels{{"submodule", "alpha"}, {"status", status}})
	}
	sub := func(name string) string { return sig(name, labels{{"submodule", "alpha"}}) }

	want := map[string]float64{
		sig("beehive_submodules", nil): 1,

		al("beehive_tasks", "TODO"):              1,
		al("beehive_tasks", "NEEDS-REVIEW"):      0,
		al("beehive_tasks", "NEEDS-ARBITRATION"): 0,
		al("beehive_tasks", "DONE"):              1,
		al("beehive_tasks", "NEEDS-HUMAN"):       1,

		sub("beehive_delivered_tasks"):  1, // only t3 DONE
		sub("beehive_sessions"):         0, // no transcripts
		sub("beehive_active_honeybees"): 0, // t1's claim heartbeat is far stale
		sub("beehive_stranded_tasks"):   0, // no bee-* branch ahead of main
	}
	for k, exp := range want {
		got, ok := idx[k]
		if !ok {
			t.Errorf("missing sample %q (want %v)", k, exp)
			continue
		}
		if got != exp {
			t.Errorf("sample %q = %v, want %v", k, got, exp)
		}
	}

	// The process-local cache counters are always present (monotonic, >= 0).
	if _, ok := idx[sig("beehive_cache_lookups_total", nil)]; !ok {
		t.Error("beehive_cache_lookups_total absent")
	}
	if _, ok := idx[sig("beehive_cache_misses_total", nil)]; !ok {
		t.Error("beehive_cache_misses_total absent")
	}

	// No sample may carry a family name absent from the single-source-of-truth
	// table (a mis-wired metric would break the text exposition's HELP/TYPE
	// grouping and the engine's historical/instant classification).
	for _, sp := range s.liveSamples(context.Background()) {
		if _, ok := famByName(sp.Name); !ok {
			t.Errorf("liveSamples emitted undeclared family %q", sp.Name)
		}
	}
}

// TestMetricFamiliesTable locks the family table the whole surface depends on:
// every family has HELP text and a valid TYPE, and the historical/instant
// classification (which decides git-step-function vs single now-point in the
// query engine) is exactly the intended set. A family silently flipping class
// would make its history either fabricated or missing.
func TestMetricFamiliesTable(t *testing.T) {
	historical := map[string]bool{
		"beehive_tasks":           true,
		"beehive_delivered_tasks": true,
		"beehive_sessions":        true,
	}
	seen := map[string]bool{}
	for _, f := range metricFamilies {
		if seen[f.name] {
			t.Errorf("duplicate family %q", f.name)
		}
		seen[f.name] = true
		if f.help == "" {
			t.Errorf("family %q has empty HELP", f.name)
		}
		if f.typ != "gauge" && f.typ != "counter" {
			t.Errorf("family %q has invalid TYPE %q", f.name, f.typ)
		}
		if f.historical != historical[f.name] {
			t.Errorf("family %q historical=%v, want %v", f.name, f.historical, historical[f.name])
		}
	}
	// Every family we assert as historical must actually be declared.
	for name := range historical {
		if !seen[name] {
			t.Errorf("expected historical family %q missing from table", name)
		}
	}
}

// rangeResult is the query_range JSON envelope's matrix result.
type rangeResult struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]interface{}   `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func queryRange(t *testing.T, s *Server, expr string, start, end, step int64) rangeResult {
	t.Helper()
	q := url.Values{}
	q.Set("query", expr)
	q.Set("start", strconv.FormatInt(start, 10))
	q.Set("end", strconv.FormatInt(end, 10))
	q.Set("step", strconv.FormatInt(step, 10))
	w := get(t, s, "/prometheus/api/v1/query_range?"+q.Encode())
	if w.Code != 200 {
		t.Fatalf("query_range %q: status %d: %s", expr, w.Code, w.Body.String())
	}
	var rr rangeResult
	if err := json.Unmarshal(w.Body.Bytes(), &rr); err != nil {
		t.Fatalf("query_range %q: json: %v (%s)", expr, err, w.Body.String())
	}
	if rr.Status != "success" || rr.Data.ResultType != "matrix" {
		t.Fatalf("query_range %q: bad envelope %+v", expr, rr)
	}
	return rr
}

// lastValue returns the value field of a matrix series' final point.
func lastValue(t *testing.T, r rangeResult, wantSeries int) string {
	t.Helper()
	if len(r.Data.Result) != wantSeries {
		t.Fatalf("want %d series, got %d: %+v", wantSeries, len(r.Data.Result), r.Data.Result)
	}
	vals := r.Data.Result[0].Values
	if len(vals) == 0 {
		t.Fatalf("series has no points: %+v", r.Data.Result[0])
	}
	return vals[len(vals)-1][1].(string)
}

// TestRangeRightEdgeInstantOnly is the regression for the dashboard "No data"
// bug: an instant-only family (beehive_submodules) has a single now-point near
// `end`. When the step grid's last stamp lands BEFORE that point (Grafana aligns
// `end` down to a step boundary; here the coarse step leaves the last grid stamp
// well before `end`), the family must still resolve at the range's right edge.
// Without the end-point evaluation the matrix would be empty here.
func TestRangeRightEdgeInstantOnly(t *testing.T) {
	now := time.Now().Unix()
	// end a few seconds ahead of `now` so it is safely >= the now-point the
	// build stamps at time.Now() during this request (mirrors production's warm
	// cache, whose now-point predates the query's end). The last grid stamp is
	// start (=now-100); the next (start+step=now+900) exceeds end, so only the
	// right-edge evaluation at `end` can surface the instant-only family.
	start, end, step := now-100, now+5, int64(1000)
	rr := queryRange(t, s0(t), "beehive_submodules", start, end, step)
	if got := lastValue(t, rr, 1); got != "1" {
		t.Fatalf("beehive_submodules right-edge value = %q, want \"1\"", got)
	}
	// The final point must be stamped at exactly `end` (the right-edge eval).
	vals := rr.Data.Result[0].Values
	if ts := int64(vals[len(vals)-1][0].(float64)); ts != end {
		t.Fatalf("final point ts = %d, want end=%d", ts, end)
	}
}

// TestRangeRightEdgeHistorical guards that the right-edge evaluation does not
// regress historical families: sum(beehive_delivered_tasks) still returns the
// current total (alpha has one DONE task) at the right edge.
func TestRangeRightEdgeHistorical(t *testing.T) {
	now := time.Now().Unix()
	start, end, step := now-100, now+5, int64(1000)
	rr := queryRange(t, s0(t), "sum(beehive_delivered_tasks)", start, end, step)
	if got := lastValue(t, rr, 1); got != "1" {
		t.Fatalf("sum(delivered) right-edge value = %q, want \"1\"", got)
	}
}

// s0 is a setup() that discards the root path for tests that only need the server.
func s0(t *testing.T) *Server { s, _ := setup(t); return s }
