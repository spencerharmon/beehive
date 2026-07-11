package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyMetricsHdr is the exact pre-silent_loss (15-col) metrics.tsv schema, as
// already-ledgered rows carry it on disk. Pinned as a literal so a future
// accidental reorder/removal of an existing column (not a clean append) is
// caught here, not silently accepted.
var legacyMetricsHdr = []string{
	"pass", "session_id", "epoch", "submodule", "kind", "branch", "taskid",
	"bytes", "turns", "user_turns", "aborted", "lost_race", "completion_miss",
	"reconcile_loop", "abort_reason",
}

func writeMetricsFile(t *testing.T, dir string, header []string, rows [][]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(strings.Join(header, "\t"))
	b.WriteByte('\n')
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t"))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, MetricsFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLedgerReadsLegacyMetrics is the binding additive-schema Accept criterion:
// a metrics.tsv written before the silent_loss column existed (the 15-col
// schema) still parses, with silent_loss defaulted false and every prior column
// intact.
func TestLedgerReadsLegacyMetrics(t *testing.T) {
	if len(legacyMetricsHdr) != minMetricsCols() {
		t.Fatalf("legacy header width %d != minMetricsCols %d — schema drift", len(legacyMetricsHdr), minMetricsCols())
	}
	dir := t.TempDir()
	rows := [][]string{
		{"1", "bee-foo-100", "100", "beehive", "work", "bee-foo", "foo", "12345", "40", "1", "false", "false", "false", "false", ""},
		{"1", "bee-bar-200", "200", "beehive", "reconcile", "bee-bar", "bar", "6789", "14", "1", "true", "true", "false", "true", "wall cap reached"},
	}
	writeMetricsFile(t, dir, legacyMetricsHdr, rows)

	led, err := LoadLedger(dir)
	if err != nil {
		t.Fatalf("LoadLedger legacy 15-col metrics.tsv: %v", err)
	}
	if len(led.Metrics) != 2 {
		t.Fatalf("parsed %d legacy rows, want 2", len(led.Metrics))
	}
	h0 := led.Metrics[0].Session.Heuristics
	if led.Metrics[0].Session.ID != "bee-foo-100" || led.Metrics[0].Session.Turns != 40 || h0.Aborted {
		t.Errorf("row0 misparsed: %+v", led.Metrics[0].Session)
	}
	if h0.SilentLoss {
		t.Errorf("legacy row0 silent_loss defaulted true, want false")
	}
	h1 := led.Metrics[1].Session.Heuristics
	if !h1.Aborted || !h1.LostRace || !h1.ReconcileLoop {
		t.Errorf("row1 flags misparsed: %+v", h1)
	}
	if h1.AbortReason != "wall cap reached" {
		t.Errorf("row1 abort_reason=%q want %q", h1.AbortReason, "wall cap reached")
	}
	if h1.SilentLoss {
		t.Errorf("legacy row1 silent_loss defaulted true, want false")
	}
}

// TestLedgerReadsCurrentMetricsSilentLoss: a metrics.tsv written at the CURRENT
// 18-col schema round-trips the silent_loss column true/false faithfully (and
// carries the appended tool_calls/tool_fails columns).
func TestLedgerReadsCurrentMetricsSilentLoss(t *testing.T) {
	dir := t.TempDir()
	rows := [][]string{
		{"1", "bee-foo-100", "100", "beehive", "work", "bee-foo", "foo", "1", "1", "1", "false", "false", "false", "false", "", "true", "10", "2"},
		{"1", "bee-foo-200", "200", "beehive", "work", "bee-foo", "foo", "1", "1", "1", "false", "false", "false", "false", "", "false", "5", "0"},
	}
	writeMetricsFile(t, dir, metricsHdr, rows)
	led, err := LoadLedger(dir)
	if err != nil {
		t.Fatalf("LoadLedger 16-col metrics.tsv: %v", err)
	}
	if !led.Metrics[0].Session.Heuristics.SilentLoss {
		t.Errorf("row0 silent_loss=false, want true")
	}
	if led.Metrics[1].Session.Heuristics.SilentLoss {
		t.Errorf("row1 silent_loss=true, want false")
	}
}

// TestLedgerRejectsBadMetricsHeader: a header narrower than the original schema,
// or one that renames an existing column (a changed prefix, not a clean append),
// is rejected — the additive contract permits only appended columns.
func TestLedgerRejectsBadMetricsHeader(t *testing.T) {
	tooNarrow := t.TempDir()
	writeMetricsFile(t, tooNarrow, []string{"pass", "session_id"}, nil)
	if _, err := LoadLedger(tooNarrow); err == nil {
		t.Errorf("a 2-col metrics header must be rejected")
	}

	renamed := t.TempDir()
	bad := append([]string(nil), metricsHdr...)
	bad[13] = "renamed_col" // was reconcile_loop
	writeMetricsFile(t, renamed, bad, nil)
	if _, err := LoadLedger(renamed); err == nil {
		t.Errorf("a renamed-prefix header must be rejected")
	}
}
