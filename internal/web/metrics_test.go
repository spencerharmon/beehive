package web

import (
	"strings"
	"testing"
)

// metricLine finds the single exposition line for a metric name + exact label
// substring and returns its value field. Fails the test if absent or duplicated
// so a malformed/duplicated series is caught, not silently passed.
func metricLine(t *testing.T, body, name, labelSubstr string) string {
	t.Helper()
	var hits []string
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, "#") || ln == "" {
			continue
		}
		if !strings.HasPrefix(ln, name) {
			continue
		}
		// Require a name boundary so "beehive_delivered_tasks" does not also
		// match "beehive_delivered_tasks_by_model".
		rest := ln[len(name):]
		if rest == "" || (rest[0] != '{' && rest[0] != ' ') {
			continue
		}
		if labelSubstr != "" && !strings.Contains(ln, labelSubstr) {
			continue
		}
		hits = append(hits, ln)
	}
	if len(hits) != 1 {
		t.Fatalf("metric %s{%s}: want exactly 1 line, got %d: %v", name, labelSubstr, len(hits), hits)
	}
	fields := strings.Fields(hits[0])
	return fields[len(fields)-1]
}

// TestMetricsEndpoint drives GET /metrics against the standard alpha fixture
// (t1 TODO, t2 NEEDS-HUMAN, t3 DONE) plus two session transcripts on distinct
// models, and asserts the git-derived Prometheus samples match the committed
// state exactly — task-status counts, delivered/session totals, per-model
// breakdown, and the exposition format's HELP/TYPE headers.
func TestMetricsEndpoint(t *testing.T) {
	s, root := setup(t)
	// Two honeybee passes on alpha: t3 delivered on sonnet, one pass on opus.
	writeTranscript(t, root, "bee-t3-100-1", "work", "github-copilot/claude-sonnet-5")
	writeTranscript(t, root, "bee-t1-101-1", "work", "")

	w := get(t, s, "/prometheus/metrics")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("Content-Type = %q, want prometheus 0.0.4", ct)
	}
	body := w.Body.String()

	// Exposition-format headers present and correctly typed.
	for _, want := range []string{
		"# HELP beehive_tasks ",
		"# TYPE beehive_tasks gauge",
		"# TYPE beehive_cache_lookups_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing exposition header %q", want)
		}
	}

	// Task-status gauge matches the fixture PLAN exactly.
	if v := metricLine(t, body, "beehive_tasks", `submodule="alpha",status="TODO"`); v != "1" {
		t.Errorf("TODO tasks = %s, want 1", v)
	}
	if v := metricLine(t, body, "beehive_tasks", `submodule="alpha",status="NEEDS-HUMAN"`); v != "1" {
		t.Errorf("NEEDS-HUMAN tasks = %s, want 1", v)
	}
	if v := metricLine(t, body, "beehive_tasks", `submodule="alpha",status="DONE"`); v != "1" {
		t.Errorf("DONE tasks = %s, want 1", v)
	}
	// A status with no tasks is still emitted as 0 (so alerts have a series).
	if v := metricLine(t, body, "beehive_tasks", `submodule="alpha",status="NEEDS-REVIEW"`); v != "0" {
		t.Errorf("NEEDS-REVIEW tasks = %s, want 0", v)
	}

	// Aggregates.
	if v := metricLine(t, body, "beehive_delivered_tasks", `submodule="alpha"`); v != "1" {
		t.Errorf("delivered = %s, want 1", v)
	}
	if v := metricLine(t, body, "beehive_sessions", `submodule="alpha"`); v != "2" {
		t.Errorf("sessions = %s, want 2", v)
	}
	if v := metricLine(t, body, "beehive_submodules", ""); v != "1" {
		t.Errorf("submodules = %s, want 1", v)
	}

	// Per-model split: sonnet ran t3's session (delivered), unstamped defaults to
	// opus. Both models should carry a session sample.
	if v := metricLine(t, body, "beehive_sessions_by_model", `model="github-copilot/claude-sonnet-5"`); v != "1" {
		t.Errorf("sonnet sessions = %s, want 1", v)
	}
	if v := metricLine(t, body, "beehive_delivered_tasks_by_model", `model="github-copilot/claude-sonnet-5"`); v != "1" {
		t.Errorf("sonnet delivered = %s, want 1", v)
	}
	if v := metricLine(t, body, "beehive_sessions_by_model", `model="`+defaultModel+`"`); v != "1" {
		t.Errorf("opus (default) sessions = %s, want 1", v)
	}
}

// TestMetricWriterEscaping locks the exposition escaping rules: a label value's
// backslash/quote/newline are escaped, a HELP's quote is left literal.
func TestMetricWriterEscaping(t *testing.T) {
	if got := escapeLabelValue(`a"b\c` + "\n"); got != `a\"b\\c\n` {
		t.Errorf("label escape = %q", got)
	}
	if got := escapeHelp(`quote " stays`); got != `quote " stays` {
		t.Errorf("help should keep literal quote, got %q", got)
	}
	if got := formatFloat(3); got != "3" {
		t.Errorf("int format = %q, want 3", got)
	}
	if got := formatFloat(1.5); got != "1.5" {
		t.Errorf("frac format = %q, want 1.5", got)
	}
}
