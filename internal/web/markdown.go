package web

import (
	"bytes"
	"html/template"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// md renders markdown to HTML for VIEW panes. It is built WITHOUT
// html.WithUnsafe(), so goldmark's default safety applies and is the
// sanitization guarantee for this feature: repo files are UNTRUSTED input, so
// any raw HTML embedded in them is dropped (replaced with an HTML comment) and
// dangerous link protocols (javascript:, data:, vbscript:) are stripped from
// hrefs. The GFM extension adds tables / strikethrough / task lists / autolinks
// for real-world docs; it does NOT relax the raw-HTML safety, which is governed
// solely by the renderer's Unsafe flag (left false here). Never reconfigure this
// with WithUnsafe(): a view must never execute markup authored in a repo file.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// mdCache memoizes renderMarkdown output keyed by the EXACT source string.
// Rendering markdown is a PURE, deterministic function of its source, so the
// HTML for a given src never changes; keying on the source content (not the
// repo HEAD) is therefore correct — an unchanged fragment hits forever, and a
// commit that edits a fragment simply changes its source, misses once, and
// renders anew. No invalidation is ever needed: superseded keys are just never
// looked up again.
//
// This is the plan-page hot path fix (pageload-plan-page-budget): the plan view
// renders {{.BodyHTML}} for EVERY task row on EVERY request, and each call ran
// the full task body back through goldmark uncached. On beehive's own ~9.4k-line
// PLAN.md (hundreds of tasks) that per-row goldmark parse dominated the render
// and pushed the warm plan page ~50% over the 50ms per-page budget, while the
// read+parse half was already memoized per HEAD (viewCache). A poll re-renders
// the SAME hundreds of identical bodies, so memoizing by source content turns
// every steady-state render into cache hits.
//
// Bounded by the number of DISTINCT markdown fragments ever rendered (task
// bodies, escalation reasons, change/design docs) — O(total repo markdown),
// which for a human-scale hive fits comfortably in memory (same sizing envelope
// as viewCache). It holds no out-of-repo state and needs no eviction: the set of
// distinct repo-derived fragments is finite and slow-growing.
var mdCache sync.Map // string(src) -> template.HTML

// renderMarkdown converts markdown source to sanitized HTML for a VIEW pane and
// returns it as template.HTML so html/template emits it verbatim (goldmark has
// already escaped text and dropped unsafe markup, so re-escaping would corrupt
// the rendered output). Callers MUST pass only the rendered result to a template
// position that trusts template.HTML, never raw repo text. EDIT surfaces keep
// using the raw string (textarea round-trip), not this.
//
// The result is memoized per source string (mdCache): a hot page that
// re-renders the same fragments on every poll (the plan view's per-row task
// bodies especially) pays goldmark exactly once per distinct fragment.
func renderMarkdown(src string) template.HTML {
	if v, ok := mdCache.Load(src); ok {
		return v.(template.HTML)
	}
	out := renderMarkdownUncached(src)
	mdCache.Store(src, out)
	return out
}

// renderMarkdownUncached is the pure goldmark render backing renderMarkdown's
// memo. goldmark.Convert against an in-memory buffer does not realistically
// fail, but on any error we degrade to the HTML-escaped source wrapped in <pre>
// so a malformed document renders as readable, safe text instead of vanishing.
func renderMarkdownUncached(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>")
	}
	return template.HTML(buf.String())
}
