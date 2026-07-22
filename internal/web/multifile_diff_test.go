package web

import (
	"html/template"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/editor"
)

// TestDiffFileBoxRendersOneCollapsibleBoxPerFile is the multi-file-diff-boxes
// WIRING guarantee: the real embedded templates (via the same tmplFS the server
// parses) render a multi-file change as N independently collapsible
// <details class="diffbox"> boxes — one per file, each carrying only its own
// file's diff — NOT a single merged blob. This is the end-to-end proof the diff
// data layer is actually wired to the UI (the reason the prior attempt was
// rejected).
func TestDiffFileBoxRendersOneCollapsibleBoxPerFile(t *testing.T) {
	tmpl, err := template.ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	diffs := editor.RenderMultiFileDiff([]editor.FileChange{
		{Path: "submodules/x/ROI.md", Old: "old roi\n", New: "new roi\n"},
		{Path: "submodules/x/INFRASTRUCTURE.md", Old: "infra\n", New: "infra edited\n"},
		{Path: "submodules/x/docs/note.md", Old: "", New: "brand new\n"},
	})
	data := map[string]interface{}{
		"SessID": "s1", "Sub": "x", "TaskID": "t1",
		"Log": nil, "Stat": "", "Diffs": diffs,
		"HasChange": true, "Busy": false, "Published": false, "Error": "",
	}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "human_resolve_panel.html", data); err != nil {
		t.Fatalf("execute panel: %v", err)
	}
	html := b.String()
	if n := strings.Count(html, `<details class="diffbox"`); n != len(diffs) {
		t.Fatalf("expected %d collapsible diff boxes (one per file), got %d\n%s", len(diffs), n, html)
	}
	// Each file's own path must appear as a box summary, and each box must be an
	// independent <details> node (so expand/collapse is per-file).
	for _, d := range diffs {
		if !strings.Contains(html, d.Path) {
			t.Errorf("box for %q not rendered", d.Path)
		}
	}
	// The old merged-blob markup (a single <pre class="diff"> holding a muted
	// path label line per file) must be gone: boxes carry their own <pre>, one
	// per <details>, so the count of <pre class="diff"> equals the box count.
	if n := strings.Count(html, `<pre class="diff"`); n != len(diffs) {
		t.Fatalf("expected one <pre> per box (%d), got %d — not a merged blob", len(diffs), n)
	}
}
