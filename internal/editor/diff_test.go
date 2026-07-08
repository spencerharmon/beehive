package editor

import (
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

// TestRenderDiffFileHighlightsRecognizedLanguage proves a recognized language
// (by filename) gets per-token `tok-*` coloring layered over the ordinary add/
// del/eq line classes, while the underlying line-level diff (which lines are
// eq/add/del) is unaffected.
func TestRenderDiffFileHighlightsRecognizedLanguage(t *testing.T) {
	old := "package main\n"
	new := "package main\n\nfunc main() {}\n"
	rows := RenderDiffFile(old, new, "example.go")
	got := rowsText(rows)
	if !strings.Contains(got, `eq:<span class="tok-kn">package</span>`) {
		t.Fatalf("expected the recognized-language row to carry a tok- keyword span:\n%s", got)
	}
	if !strings.Contains(got, `add:<span class="tok-kd">func</span>`) {
		t.Fatalf("expected the added line to carry a tok- keyword span:\n%s", got)
	}
	// The line-level classification (which rows are eq/add) must match the plain
	// renderer's — only the HTML content differs.
	plainKinds := kindsOf(RenderDiff(old, new))
	langKinds := kindsOf(rows)
	if strings.Join(plainKinds, ",") != strings.Join(langKinds, ",") {
		t.Fatalf("row kinds differ between RenderDiff and RenderDiffFile: %v vs %v", plainKinds, langKinds)
	}
}

// TestRenderDiffFileMarkdown proves markdown source (by ".md" filename) is
// tokenized too — a heading line gets a generic-heading span — matching the
// "syntax-highlight ... markdown" half of the accept criterion.
func TestRenderDiffFileMarkdown(t *testing.T) {
	rows := RenderDiffFile("", "# Title\n", "notes.md")
	got := rowsText(rows)
	if !strings.Contains(got, `tok-gh`) || !strings.Contains(got, "# Title") {
		t.Fatalf("expected a heading token for markdown source:\n%s", got)
	}
}

// TestRenderDiffFileFallsBackWithoutFilename proves an unrecognized/empty
// filename renders IDENTICALLY to the plain RenderDiff (including its "chg"
// intra-line highlighting) — every pre-existing caller (and every chat-edit
// file without a matched extension) is unaffected byte-for-byte.
func TestRenderDiffFileFallsBackWithoutFilename(t *testing.T) {
	old, new := "alpha\nbeta\ngamma\n", "alpha\nbeta changed\ndelta\ngamma\n"
	plain := rowsText(RenderDiff(old, new))
	for _, name := range []string{"", "notes.this-extension-does-not-exist-anywhere"} {
		got := rowsText(RenderDiffFile(old, new, name))
		if got != plain {
			t.Fatalf("RenderDiffFile(%q) should fall back to RenderDiff verbatim:\ngot:  %s\nwant: %s", name, got, plain)
		}
	}
}

func kindsOf(rows []DiffRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Kind
	}
	return out
}

// TestRenderDiffFileEscapesHTML proves the syntax-highlighted path escapes
// content exactly like the plain renderer (TestRenderDiffEscapesHTML): a raw
// "<script>" must never reach the page unescaped just because a language
// matched, regardless of what token type(s) the lexer assigns it (markdown, in
// particular, tokenizes "<"/"script"/">" as separate HTML-tag tokens rather
// than one literal run — each must still come out escaped).
func TestRenderDiffFileEscapesHTML(t *testing.T) {
	for _, name := range []string{"notes.md", "example.go", "unrecognized.zzz"} {
		rows := RenderDiffFile("", "<script>\n", name)
		got := rowsText(rows)
		if !strings.Contains(got, "add:") {
			t.Fatalf("RenderDiffFile(%q): missing the added row: %s", name, got)
		}
		if strings.Contains(got, "<script>") {
			t.Fatalf("RenderDiffFile(%q) let raw HTML through unescaped: %s", name, got)
		}
		if !strings.Contains(got, "&lt;") || !strings.Contains(got, "&gt;") {
			t.Fatalf("RenderDiffFile(%q) did not escape < / >: %s", name, got)
		}
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
