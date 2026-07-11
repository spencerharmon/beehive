package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/audit"
)

// TestPrintWindowModelColumn pins printWindow's rendering directly: the model
// column sits at index 4 (after taskid, before bytes), sourced verbatim from
// the already-parsed audit.Session.Model, and a legacy/unmodeled session (Model
// == "") prints a clean empty field rather than an error or a shifted row.
func TestPrintWindowModelColumn(t *testing.T) {
	sessions := []audit.Session{
		{
			ID: "bee-modeled-task-100", Epoch: 100, Kind: audit.KindWork, TaskID: "modeled-task",
			Model: "github-copilot/claude-opus-4.8", Bytes: 12345, Turns: 7,
		},
		{
			ID: "bee-legacy-task-200", Epoch: 200, Kind: audit.KindReview, TaskID: "legacy-task",
			Model: "", Bytes: 6789, Turns: 3,
		},
	}
	out := captureStdout(t, func() { printWindow(sessions) })
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("printWindow produced %d lines, want 4 (comment+header+2 rows):\n%s", len(lines), out)
	}
	if lines[0] != "# window (N-2, un-audited)" {
		t.Fatalf("first line = %q", lines[0])
	}
	wantHeader := []string{
		"session_id", "epoch", "kind", "taskid", "model", "bytes", "turns",
		"aborted", "lost_race", "completion_miss", "reconcile_loop", "silent_loss",
		"tool_calls", "tool_fails",
	}
	if header := strings.Split(lines[1], "\t"); !reflect.DeepEqual(header, wantHeader) {
		t.Fatalf("header=%v want %v", header, wantHeader)
	}
	modeled := strings.Split(lines[2], "\t")
	legacy := strings.Split(lines[3], "\t")
	if len(modeled) != len(wantHeader) || len(legacy) != len(wantHeader) {
		t.Fatalf("row field counts modeled=%d legacy=%d want %d", len(modeled), len(legacy), len(wantHeader))
	}
	const modelIdx = 4
	if modeled[modelIdx] != "github-copilot/claude-opus-4.8" {
		t.Errorf("modeled row model column = %q, want github-copilot/claude-opus-4.8", modeled[modelIdx])
	}
	if legacy[modelIdx] != "" {
		t.Errorf("legacy row model column = %q, want empty (no model header, not an error)", legacy[modelIdx])
	}
	// The new column must not shift any pre-existing one.
	if modeled[0] != "bee-modeled-task-100" || modeled[3] != "modeled-task" || modeled[5] != "12345" || modeled[6] != "7" {
		t.Errorf("modeled row=%v: pre-existing columns shifted", modeled)
	}
	if legacy[0] != "bee-legacy-task-200" || legacy[3] != "legacy-task" || legacy[5] != "6789" || legacy[6] != "3" {
		t.Errorf("legacy row=%v: pre-existing columns shifted", legacy)
	}
}

// TestAuditCommandModelColumnEndToEnd drives the real `beehive audit
// --submodule <sm>` path (the binding Accept criterion) over four synthetic
// session transcripts and asserts the "# window" section's model column
// round-trips for both a modeled and a legacy/unmodeled fixture session, with
// no change needed to internal/audit and no error on the legacy session.
func TestAuditCommandModelColumnEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessDir := filepath.Join(root, "submodules", "beehive", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Four sessions by ascending epoch; Window is N-2 so the two NEWEST (gamma,
	// delta) are excluded, leaving alpha (legacy) and beta (modeled) as the
	// window this test inspects.
	writeAuditFixture(t, sessDir, "bee-alpha", 100, audit.KindWork, "", 3)
	writeAuditFixture(t, sessDir, "bee-beta", 200, audit.KindWork, "github-copilot/claude-opus-4.8", 4)
	writeAuditFixture(t, sessDir, "bee-gamma", 300, audit.KindWork, "github-copilot/claude-opus-4.8", 5)
	writeAuditFixture(t, sessDir, "bee-delta", 400, audit.KindWork, "", 2)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	out := captureStdout(t, func() {
		cmd := auditCmd()
		cmd.SetArgs([]string{"--submodule", "beehive"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("beehive audit: %v", err)
		}
	})

	header, rows := windowSection(t, out)
	wantHeader := []string{
		"session_id", "epoch", "kind", "taskid", "model", "bytes", "turns",
		"aborted", "lost_race", "completion_miss", "reconcile_loop", "silent_loss",
		"tool_calls", "tool_fails",
	}
	if !reflect.DeepEqual(header, wantHeader) {
		t.Fatalf("window header=%v want %v\nfull output:\n%s", header, wantHeader, out)
	}
	byID := make(map[string][]string, len(rows))
	for _, r := range rows {
		if len(r) != len(wantHeader) {
			t.Fatalf("row %v has %d fields, want %d", r, len(r), len(wantHeader))
		}
		byID[r[0]] = r
	}
	if _, ok := byID["bee-gamma-300"]; ok {
		t.Fatalf("bee-gamma (one of the N-2 newest) must be excluded from the window: rows=%v", rows)
	}
	if _, ok := byID["bee-delta-400"]; ok {
		t.Fatalf("bee-delta (one of the N-2 newest) must be excluded from the window: rows=%v", rows)
	}
	alpha, ok := byID["bee-alpha-100"]
	if !ok {
		t.Fatalf("bee-alpha missing from window: rows=%v", rows)
	}
	if alpha[4] != "" {
		t.Errorf("legacy/unmodeled session model column = %q, want empty (clean, not an error)", alpha[4])
	}
	beta, ok := byID["bee-beta-200"]
	if !ok {
		t.Fatalf("bee-beta missing from window: rows=%v", rows)
	}
	if beta[4] != "github-copilot/claude-opus-4.8" {
		t.Errorf("modeled session model column = %q, want github-copilot/claude-opus-4.8", beta[4])
	}
}

// writeAuditFixture writes a minimal, format-faithful session transcript named
// "<branch>-<epoch>.md": the "submodule · kind · branch[ · model]" header line,
// one user turn, and n assistant turns. model == "" omits the header's
// trailing "· model:" field entirely, reproducing a legacy/pre-248e967
// transcript that must still parse and print cleanly.
func writeAuditFixture(t *testing.T, dir, branch string, epoch int64, kind, model string, turns int) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "# session %s-%d\n\nsubmodule: beehive \u00b7 kind: %s \u00b7 branch: %s", branch, epoch, kind, branch)
	if model != "" {
		fmt.Fprintf(&b, " \u00b7 model: %s", model)
	}
	b.WriteString("\n\n## user\n\nprompt\n")
	for i := 0; i < turns; i++ {
		b.WriteString("\n## assistant\n\nwork\n")
	}
	name := fmt.Sprintf("%s-%d.md", branch, epoch)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// windowSection extracts the header + data rows of `beehive audit`'s "# window
// (N-2, un-audited)" TSV block from its full stdout (which also carries the
// census, aggregate, and trend sections around it).
func windowSection(t *testing.T, out string) (header []string, rows [][]string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	start := -1
	for i, l := range lines {
		if l == "# window (N-2, un-audited)" {
			start = i + 1
			break
		}
	}
	if start < 0 || start >= len(lines) {
		t.Fatalf("no window section found in output:\n%s", out)
	}
	header = strings.Split(lines[start], "\t")
	for _, l := range lines[start+1:] {
		if l == "" || strings.HasPrefix(l, "#") {
			break
		}
		rows = append(rows, strings.Split(l, "\t"))
	}
	return header, rows
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. The reader runs concurrently so fn cannot deadlock
// writing more than a pipe buffer's worth of output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return <-done
}
