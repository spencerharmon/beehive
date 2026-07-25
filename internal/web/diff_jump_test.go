package web

import (
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/editor"
)

// TestDiffJumpToChangesOverlay is diff-jump-to-changes-overlay's acceptance
// test: the diff panel shows a navigation overlay listing each change (hunk)
// in a file, with working jump-to anchors that target each hunk — modeled on
// the session view's TOC/jump navigation (session-transcript-rendered-toc).
// It asserts both the single-file overlay (editor_panel.html, commit_view.html)
// and the multi-file overlay (dance_panel.html/human_resolve_panel.html via
// diff_box.html's "diff-jump-overlay-multi"), against a fixture diff with
// multiple hunks so the overlay must enumerate more than one entry.
func TestDiffJumpToChangesOverlay(t *testing.T) {
	s, _ := setup(t)

	t.Run("single file overlay enumerates every hunk with a working anchor", func(t *testing.T) {
		// Fixture: three separate change hunks (each isolated by unchanged
		// context lines), so a correct overlay must list exactly 3 entries.
		old := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\n"
		new := "line1\nCHANGED2\nline3\nline4\nCHANGED5\nline6\nline7\nline8\nCHANGED9\n"
		rows := editor.RenderDiff(old, new)
		hunks := editor.AssignHunkAnchors(rows, "hunk-")
		if len(hunks) != 3 {
			t.Fatalf("expected 3 hunks in the fixture diff, got %d", len(hunks))
		}

		out := renderTmpl(t, s, "editor_panel.html", map[string]interface{}{
			"ID": "e1", "File": "ROI.md", "Rows": rows, "Hunks": hunks,
		})
		if !strings.Contains(out, `class="diff-toc"`) {
			t.Fatalf("editor_panel.html did not render the jump-to-changes overlay:\n%s", out)
		}
		for _, h := range hunks {
			// The overlay must link to the hunk's anchor...
			if !strings.Contains(out, `data-diff-anchor="`+h.Anchor+`"`) {
				t.Errorf("overlay missing jump link for anchor %q:\n%s", h.Anchor, out)
			}
			// ...and that anchor must actually exist as a DOM id in the
			// rendered diff rows (the jump target), so the link is not dead.
			if !strings.Contains(out, `id="`+h.Anchor+`"`) {
				t.Errorf("rendered diff missing anchor target id=%q (dead jump link):\n%s", h.Anchor, out)
			}
		}
	})

	t.Run("multi-file overlay spans changed files and their hunks", func(t *testing.T) {
		diffs := editor.RenderMultiFileDiff([]editor.FileChange{
			{Path: "submodules/x/ROI.md", Old: "a\nb\nc\n", New: "a\nCHANGED\nc\n"},
			{Path: "submodules/x/PLAN.md", Old: "p\nq\nr\ns\n", New: "p\nCHANGED-Q\nr\nCHANGED-S\n"},
		})
		var wantHunks int
		for _, d := range diffs {
			wantHunks += len(d.Hunks)
		}
		if wantHunks < 3 {
			t.Fatalf("expected at least 3 hunks across the fixture's 2 files, got %d", wantHunks)
		}

		out := renderTmpl(t, s, "human_resolve_panel.html", map[string]interface{}{
			"SessID": "s1", "Sub": "x", "TaskID": "t1",
			"Log": nil, "Stat": "", "Diffs": diffs,
			"HasChange": true, "Busy": false, "Published": false, "Error": "",
		})
		if !strings.Contains(out, `class="diff-toc"`) {
			t.Fatalf("human_resolve_panel.html did not render the multi-file jump-to-changes overlay:\n%s", out)
		}
		for _, d := range diffs {
			if !strings.Contains(out, d.Path) {
				t.Errorf("overlay missing file label %q:\n%s", d.Path, out)
			}
			for _, h := range d.Hunks {
				if !strings.Contains(out, `data-diff-anchor="`+h.Anchor+`"`) {
					t.Errorf("overlay missing jump link for anchor %q (file %q):\n%s", h.Anchor, d.Path, out)
				}
				if !strings.Contains(out, `id="`+h.Anchor+`"`) {
					t.Errorf("rendered diff missing anchor target id=%q (file %q, dead jump link):\n%s", h.Anchor, d.Path, out)
				}
			}
		}
	})
}
