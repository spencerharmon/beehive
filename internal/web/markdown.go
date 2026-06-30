package web

import (
	"bytes"
	"html/template"

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

// renderMarkdown converts markdown source to sanitized HTML for a VIEW pane and
// returns it as template.HTML so html/template emits it verbatim (goldmark has
// already escaped text and dropped unsafe markup, so re-escaping would corrupt
// the rendered output). Callers MUST pass only the rendered result to a template
// position that trusts template.HTML, never raw repo text. EDIT surfaces keep
// using the raw string (textarea round-trip), not this.
//
// goldmark.Convert against an in-memory buffer does not realistically fail, but
// on any error we degrade to the HTML-escaped source wrapped in <pre> so a
// malformed document renders as readable, safe text instead of vanishing.
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>")
	}
	return template.HTML(buf.String())
}
