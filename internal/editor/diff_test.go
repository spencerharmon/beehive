package editor

import (
	"html/template"
	"strings"
	"testing"
)

func rowsText(rows []DiffRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Kind + ":" + string(r.HTML) + "\n")
	}
	return b.String()
}

func TestRenderDiffAddRemoveChange(t *testing.T) {
	old := "alpha\nbeta\ngamma\n"
	new := "alpha\nbeta changed\ndelta\ngamma\n"
	rows := RenderDiff(old, new)
	got := rowsText(rows)
	if !strings.Contains(got, "eq:alpha") {
		t.Errorf("missing equal line:\n%s", got)
	}
	// "beta" -> "beta changed" is a replace: del+add with a column span.
	if !strings.Contains(got, `del:beta`) || !strings.Contains(got, `add:beta<span class="chg"> changed</span>`) {
		t.Errorf("column highlight wrong:\n%s", got)
	}
	if !strings.Contains(got, "add:delta") {
		t.Errorf("missing inserted line:\n%s", got)
	}
}

func TestRenderDiffNoChange(t *testing.T) {
	rows := RenderDiff("same\ntext\n", "same\ntext\n")
	for _, r := range rows {
		if r.Kind != "eq" {
			t.Fatalf("unexpected non-eq row %+v", r)
		}
	}
}

func TestRenderDiffEscapesHTML(t *testing.T) {
	rows := RenderDiff("", "<script>\n")
	if !strings.Contains(rowsText(rows), "add:&lt;script&gt;") {
		t.Fatalf("html not escaped: %s", rowsText(rows))
	}
}

// TestRenderDiffHTMLUsesPrecomputedLines proves RenderDiffHTML substitutes the
// caller's precomputed per-line HTML (e.g. a syntax-highlighting tokenizer's
// output) by line index instead of the plain escape/char-diff, for every row
// kind (chat-editor-snappy-polish).
func TestRenderDiffHTMLUsesPrecomputedLines(t *testing.T) {
	old := "alpha\nbeta\ngamma\n"
	new := "alpha\nBETA\ngamma\ndelta\n"
	oldHTML := []template.HTML{"<b>alpha</b>", "<b>beta</b>", "<b>gamma</b>"}
	newHTML := []template.HTML{"<b>alpha</b>", "<b>BETA</b>", "<b>gamma</b>", "<b>delta</b>"}
	rows := RenderDiffHTML(old, new, oldHTML, newHTML)
	got := rowsText(rows)
	for _, want := range []string{"eq:<b>alpha</b>", "del:<b>beta</b>", "add:<b>BETA</b>", "eq:<b>gamma</b>", "add:<b>delta</b>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in highlighted rows:\n%s", want, got)
		}
	}
	// No char-diff span should appear: a highlighted replace shows the WHOLE
	// precomputed line, never charDiff's <span class="chg">.
	if strings.Contains(got, `class="chg"`) {
		t.Errorf("highlighted rows must not carry char-diff spans:\n%s", got)
	}
}

// TestRenderDiffHTMLFallsBackWithoutHTML proves RenderDiffHTML(old, new, nil,
// nil) is byte-identical to RenderDiff (the default: no tokenizer available for
// this file falls back to today's plain/char-diff rendering unchanged).
func TestRenderDiffHTMLFallsBackWithoutHTML(t *testing.T) {
	old, new := "alpha\nbeta\ngamma\n", "alpha\nbeta changed\ndelta\ngamma\n"
	got := rowsText(RenderDiffHTML(old, new, nil, nil))
	want := rowsText(RenderDiff(old, new))
	if got != want {
		t.Fatalf("RenderDiffHTML(nil,nil) diverged from RenderDiff:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderDiffHTMLPartialCoverageFallsBackPerLine proves a line with no
// precomputed HTML entry (e.g. the tokenizer's slice is shorter than the file,
// an edge case around trailing lines) degrades to the plain escape for THAT
// line only, never a panic or an empty render.
func TestRenderDiffHTMLPartialCoverageFallsBackPerLine(t *testing.T) {
	old, new := "", "<b>x</b>\nplain\n"
	rows := RenderDiffHTML(old, new, nil, []template.HTML{"<mark>x</mark>"}) // only line 0 covered
	got := rowsText(rows)
	if !strings.Contains(got, "add:<mark>x</mark>") {
		t.Errorf("covered line should use the precomputed HTML:\n%s", got)
	}
	if !strings.Contains(got, "add:plain") {
		t.Errorf("uncovered line should fall back to the plain escape:\n%s", got)
	}
}

func TestValidateFile(t *testing.T) {
	// The allowlist is built from repo.OptionalFiles + repo.RootInstructionFiles
	// (ai-edit-publish-to-main): every file the frontend actually renders an
	// edit-with-AI link for, at both the repo root and inside a submodule.
	ok := []string{
		"submodules/x/ROI.md", "INFRASTRUCTURE.md", "submodules/y/SUBMODULE-LINKS.yaml",
		"submodules/x/AGENTS.md", "AGENTS.md", "HONEYBEE.md", "BOOTSTRAP.md", "LOCALS.md",
		"submodules/x/RULES.md", "submodules/x/ARTIFACTS.md",
	}
	for _, f := range ok {
		if err := ValidateFile(f); err != nil {
			t.Errorf("want ok for %q: %v", f, err)
		}
	}
	bad := []string{"PLAN.md", "submodules/x/PLAN.md", "../etc/passwd", "submodules/x/../../escape/ROI.md.x", "submodules/x/secret.txt"}
	for _, f := range bad {
		if err := ValidateFile(f); err == nil {
			t.Errorf("want error for %q", f)
		}
	}
}
