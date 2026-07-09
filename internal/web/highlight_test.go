package web

import (
	"html/template"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

// toStrings converts a []template.HTML to []string for test-side string
// matching/joining.
func toStrings(hs []template.HTML) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = string(h)
	}
	return out
}

// splitLinesLike mirrors internal/editor's splitLines line-count semantics
// exactly (see diff.go), so tests can assert highlightLines stays index-aligned
// with the diff algorithm without importing an unexported helper cross-package.
func splitLinesLike(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// TestLexerForMatchesCommonLanguages proves the extension/name-based lookup
// recognizes the common languages + markdown the acceptance criteria names, and
// returns nil (no highlighting, not an error) for an unrecognized/no-extension
// path.
func TestLexerForMatchesCommonLanguages(t *testing.T) {
	for _, path := range []string{
		"main.go", "script.py", "app.js", "app.ts", "data.json", "config.yaml",
		"config.yml", "run.sh", "README.md", "notes.markdown", "submodules/x/PLAN.md",
	} {
		if lexerFor(path) == nil {
			t.Errorf("lexerFor(%q) = nil, want a recognized lexer", path)
		}
	}
	for _, path := range []string{"noext", "weird.zzzzz-not-a-lang"} {
		if l := lexerFor(path); l != nil {
			t.Errorf("lexerFor(%q) = %v, want nil (unrecognized)", path, l)
		}
	}
}

// TestHighlightLinesLineCountMatchesSplitLines locks the index-alignment
// invariant RenderDiffHTML depends on: highlightLines must return exactly one
// entry per line, in the SAME order/count as internal/editor's splitLines,
// across the trailing-newline edge cases that function documents.
func TestHighlightLinesLineCountMatchesSplitLines(t *testing.T) {
	lexer := lexerFor("main.go")
	for _, src := range []string{
		"",
		"\n",
		"a",
		"a\nb",
		"a\nb\n",
		"a\nb\n\n",
		"package main\n\nfunc main() {}\n",
	} {
		want := len(splitLinesLike(src))
		got := len(highlightLines(src, lexer))
		if got != want {
			t.Errorf("highlightLines(%q) returned %d lines, want %d (splitLines-aligned)", src, got, want)
		}
	}
}

// TestHighlightLinesNilLexerOrEmpty proves the graceful no-highlight fallback:
// a nil lexer (unrecognized file type) or empty source both return nil, the
// exact signal RenderDiffHTML treats as "no precomputed HTML for this side".
func TestHighlightLinesNilLexerOrEmpty(t *testing.T) {
	if got := highlightLines("package main\n", nil); got != nil {
		t.Errorf("nil lexer should yield nil, got %v", got)
	}
	if got := highlightLines("", lexerFor("main.go")); got != nil {
		t.Errorf("empty source should yield nil, got %v", got)
	}
}

// TestHighlightLinesEscapesUntrustedContent proves repo content that looks like
// markup is ALWAYS HTML-escaped, even inside a syntax-highlighted span: the
// tokenizer must never become an XSS hole for arbitrary repo file content.
func TestHighlightLinesEscapesUntrustedContent(t *testing.T) {
	lexer := lexerFor("notes.md")
	lines := highlightLines("<script>alert(1)</script>\n", lexer)
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %v", len(lines), lines)
	}
	got := string(lines[0])
	if strings.Contains(got, "<script>") {
		t.Fatalf("raw <script> leaked into highlighted HTML: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag in highlighted HTML: %q", got)
	}
}

// TestHighlightLinesGoKeywordsAndStrings proves a recognized language actually
// gets classed spans (not just plain escaped text) — the "syntax-highlight
// common languages" acceptance, exercised end to end through the tokenizer.
func TestHighlightLinesGoKeywordsAndStrings(t *testing.T) {
	src := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	lines := highlightLines(src, lexerFor("main.go"))
	joined := strings.Join(toStrings(lines), "\n")
	if !strings.Contains(joined, `class="hl-kw"`) {
		t.Errorf("expected a keyword span (package/func) in:\n%s", joined)
	}
	if !strings.Contains(joined, `class="hl-str"`) {
		t.Errorf("expected a string span (\"hi\") in:\n%s", joined)
	}
}

// TestHighlightLinesMarkdownHeading proves the markdown lexer is wired and
// produces a distinct heading class, covering the "...and markdown" half of the
// acceptance criteria.
func TestHighlightLinesMarkdownHeading(t *testing.T) {
	lines := highlightLines("# Title\n\nSome *text*.\n", lexerFor("README.md"))
	joined := strings.Join(toStrings(lines), "\n")
	if !strings.Contains(joined, `class="hl-head"`) {
		t.Errorf("expected a heading span for '# Title' in:\n%s", joined)
	}
}

// panicLexer is a chroma.Lexer whose Tokenise panics, simulating the failure
// mode chroma's own Iterator doc warns about ("If an error occurs within an
// Iterator, it may propagate this in a panic. Formatters should recover.").
// highlightLines renders arbitrary repo file content on every panel poll, so
// this proves a lexer-level panic degrades to "no highlighting" instead of
// crashing the request.
type panicLexer struct{}

func (panicLexer) Config() *chroma.Config { return &chroma.Config{Name: "panic-test"} }
func (panicLexer) Tokenise(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
	panic("simulated lexer panic")
}
func (l panicLexer) SetRegistry(*chroma.LexerRegistry) chroma.Lexer { return l }
func (l panicLexer) SetAnalyser(func(string) float32) chroma.Lexer  { return l }
func (panicLexer) AnalyseText(string) float32                       { return 0 }

func TestHighlightLinesRecoversFromLexerPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("highlightLines must recover a lexer panic itself, not propagate it: %v", r)
		}
	}()
	got := highlightLines("anything\n", panicLexer{})
	if got != nil {
		t.Fatalf("expected nil (no highlighting) after a recovered panic, got %v", got)
	}
}
