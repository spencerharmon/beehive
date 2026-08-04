package web

import (
	"html/template"
	"strings"
	"testing"
)

// TestRenderMarkdownMemoEquivalence pins the plan-page hot-path fix
// (pageload-plan-page-budget): the memoized renderMarkdown must return EXACTLY
// what the pure goldmark render (renderMarkdownUncached) produces, and a repeat
// call for the same source must be served from the memo without re-rendering.
// The memo is what drops the plan view — which re-renders every task body on
// every request — back under its per-page budget on the live, hundreds-of-tasks
// hive.
func TestRenderMarkdownMemoEquivalence(t *testing.T) {
	srcs := []string{
		"",
		"plain text",
		"# heading\n\nwith **bold** and a [link](https://example.com).",
		"- a\n- b\n- c\n\n| x | y |\n|---|---|\n| 1 | 2 |",
		"implement feature 7 with care and detail\nFiles: f7.go\nDoc: br-task-7.md\nAccept: works",
		"<script>alert(1)</script> raw html must be dropped",
	}
	for _, src := range srcs {
		want := renderMarkdownUncached(src)
		// First call populates the memo; second call must hit it. Both must
		// equal the uncached render byte-for-byte.
		got1 := renderMarkdown(src)
		got2 := renderMarkdown(src)
		if string(got1) != string(want) {
			t.Fatalf("renderMarkdown(%q) = %q, want (uncached) %q", src, got1, want)
		}
		if string(got2) != string(want) {
			t.Fatalf("renderMarkdown(%q) second call = %q, want %q", src, got2, want)
		}
	}
	// The unsafe-HTML case must still be sanitized through the memo (the memo
	// must never bypass goldmark's safety): raw <script> is dropped.
	if h := renderMarkdown("<script>alert(1)</script>ok"); strings.Contains(string(h), "<script>") {
		t.Fatalf("memoized render leaked raw <script>: %q", h)
	}
}

// TestRenderMarkdownMemoServesFromCache proves the memo actually stores and
// serves the rendered value: once a source is rendered, mdCache holds exactly
// that HTML and a repeat lookup returns it (goldmark is not re-run).
func TestRenderMarkdownMemoServesFromCache(t *testing.T) {
	src := "## unique-memo-probe\n\nbody with detail rendered once, served many times"
	first := renderMarkdown(src)
	v, ok := mdCache.Load(src)
	if !ok {
		t.Fatal("renderMarkdown did not populate mdCache for the source")
	}
	if got := v.(template.HTML); string(got) != string(first) {
		t.Fatalf("mdCache stored %q, want the rendered value %q", got, first)
	}
	if string(renderMarkdown(src)) != string(first) {
		t.Fatal("repeat renderMarkdown returned a different value than the first")
	}
}
