package web

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// mkSeries builds a series with sorted labels for engine tests.
func mkSeries(name string, lb labels, pts ...point) series {
	return series{name: name, labels: sortedLabels(lb), pts: pts}
}

// TestEngineInstant covers selectors, matchers, sum by, arithmetic, comparison
// filtering, and topk against a hand-built series set.
func TestEngineInstant(t *testing.T) {
	now := time.Now().Unix()
	all := []series{
		mkSeries("beehive_tasks", labels{{"submodule", "a"}, {"status", "DONE"}}, point{now, 10}),
		mkSeries("beehive_tasks", labels{{"submodule", "a"}, {"status", "TODO"}}, point{now, 3}),
		mkSeries("beehive_tasks", labels{{"submodule", "b"}, {"status", "DONE"}}, point{now, 5}),
		mkSeries("beehive_tasks", labels{{"submodule", "b"}, {"status", "NEEDS-HUMAN"}}, point{now, 2}),
	}
	eng := &engine{all: all}

	type tc struct {
		q    string
		want map[string]float64 // sigKey(name,labels) -> value ; name "" allowed
	}
	sig := func(name string, lb labels) string { return sigKey(name, sortedLabels(lb)) }

	cases := []tc{
		{`beehive_tasks{status="DONE"}`, map[string]float64{
			sig("beehive_tasks", labels{{"submodule", "a"}, {"status", "DONE"}}): 10,
			sig("beehive_tasks", labels{{"submodule", "b"}, {"status", "DONE"}}): 5,
		}},
		{`sum(beehive_tasks)`, map[string]float64{sig("", nil): 20}},
		{`sum by (submodule) (beehive_tasks)`, map[string]float64{
			sig("", labels{{"submodule", "a"}}): 13,
			sig("", labels{{"submodule", "b"}}): 7,
		}},
		{`sum by (status) (beehive_tasks) > 10`, map[string]float64{
			sig("", labels{{"status", "DONE"}}): 15,
		}},
		{`count by (submodule) (beehive_tasks)`, map[string]float64{
			sig("", labels{{"submodule", "a"}}): 2,
			sig("", labels{{"submodule", "b"}}): 2,
		}},
		{`beehive_tasks{status="DONE"} + beehive_tasks{status="DONE"}`, map[string]float64{
			sig("", labels{{"submodule", "a"}, {"status", "DONE"}}): 20,
			sig("", labels{{"submodule", "b"}, {"status", "DONE"}}): 10,
		}},
		{`topk(1, sum by (submodule) (beehive_tasks))`, map[string]float64{
			sig("", labels{{"submodule", "a"}}): 13,
		}},
	}
	for _, c := range cases {
		val, err := eng.eval(c.q, now)
		if err != nil {
			t.Errorf("%q: eval error: %v", c.q, err)
			continue
		}
		got := map[string]float64{}
		for _, sp := range val.vec {
			got[sigKey(sp.Name, sp.Labels)] = sp.Value
		}
		if len(got) != len(c.want) {
			t.Errorf("%q: got %d series, want %d (%v)", c.q, len(got), len(c.want), got)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%q: series %q = %v, want %v", c.q, k, got[k], v)
			}
		}
	}
}

// TestEngineScalarAndBool checks scalar arithmetic and the bool modifier.
func TestEngineScalarAndBool(t *testing.T) {
	eng := &engine{}
	v, err := eng.eval("2 * (3 + 4)", 0)
	if err != nil || !v.isScalar || v.scalar != 14 {
		t.Fatalf("scalar arithmetic: %+v err=%v", v, err)
	}
	v, err = eng.eval("1 > bool 2", 0)
	if err != nil || !v.isScalar || v.scalar != 0 {
		t.Fatalf("bool compare: %+v err=%v", v, err)
	}
}

// TestEngineRate builds a two-point series and checks increase()/rate() read the
// git-reconstructed history correctly.
func TestEngineRate(t *testing.T) {
	base := int64(1_000_000)
	all := []series{
		mkSeries("beehive_delivered_tasks", labels{{"submodule", "a"}},
			point{base, 10}, point{base + 3600, 40}),
	}
	eng := &engine{all: all}
	// increase over the last hour = 40-10 = 30.
	v, err := eng.eval(`increase(beehive_delivered_tasks[1h])`, base+3600)
	if err != nil || len(v.vec) != 1 || v.vec[0].Value != 30 {
		t.Fatalf("increase: %+v err=%v", v, err)
	}
	// rate = 30 / 3600s.
	v, err = eng.eval(`rate(beehive_delivered_tasks[1h])`, base+3600)
	if err != nil || len(v.vec) != 1 || v.vec[0].Value != 30.0/3600.0 {
		t.Fatalf("rate: %+v err=%v", v, err)
	}
	// __name__ is dropped by rate/increase.
	if v.vec[0].Name != "" {
		t.Errorf("rate should drop __name__, got %q", v.vec[0].Name)
	}
}

// TestValueAtAbsence: a query before a series' first point yields no sample
// (honest absence, never a fabricated zero).
func TestValueAtAbsence(t *testing.T) {
	s := mkSeries("m", nil, point{100, 5}, point{200, 8})
	if _, ok := s.valueAt(99); ok {
		t.Error("valueAt before first point should be absent")
	}
	if v, ok := s.valueAt(150); !ok || v != 5 {
		t.Errorf("valueAt(150) = %v,%v want 5,true", v, ok)
	}
	if v, ok := s.valueAt(999); !ok || v != 8 {
		t.Errorf("valueAt(999) = %v,%v want 8,true", v, ok)
	}
}

// TestPromHTTPInstant drives the real HTTP handler end-to-end against the alpha
// fixture, asserting the Prometheus JSON envelope and a computed value.
func TestPromHTTPInstant(t *testing.T) {
	s, root := setup(t)
	writeTranscript(t, root, "bee-t3-100-1", "work", "github-copilot/claude-sonnet-5")

	w := get(t, s, `/prometheus/api/v1/query?query=`+url.QueryEscape(`sum(beehive_delivered_tasks)`))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("json: %v (%s)", err, w.Body.String())
	}
	if env.Status != "success" || env.Data.ResultType != "vector" {
		t.Fatalf("envelope: %+v", env)
	}
	if len(env.Data.Result) != 1 {
		t.Fatalf("want 1 result, got %d", len(env.Data.Result))
	}
	// alpha fixture has exactly one DONE task (t3).
	if got := env.Data.Result[0].Value[1]; got != "1" {
		t.Errorf("sum(delivered) = %v, want \"1\"", got)
	}
}

// TestPromHTTPBuildInfoAndLabels checks the datasource-probe endpoints Grafana
// relies on to recognize a Prometheus datasource.
func TestPromHTTPBuildInfoAndLabels(t *testing.T) {
	s, _ := setup(t)
	w := get(t, s, "/prometheus/api/v1/status/buildinfo")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"success"`) {
		t.Fatalf("buildinfo: %d %s", w.Code, w.Body.String())
	}
	w = get(t, s, "/prometheus/api/v1/label/__name__/values")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "beehive_tasks") {
		t.Fatalf("label __name__ values: %d %s", w.Code, w.Body.String())
	}
}
