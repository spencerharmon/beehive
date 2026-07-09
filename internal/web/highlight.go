package web

import (
	"html/template"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// chat-editor-snappy-polish: syntax-highlight the chat-diff view for common
// languages and markdown. Highlighting is computed HERE (server-side, this
// package only) rather than in internal/editor/diff.go so the shared diff
// algorithm stays free of the chroma dependency and every OTHER caller of
// editor.RenderDiff (the single-file coordination editor, the skills diff view)
// is byte-for-byte unaffected; only the chat-diff editor opts in, via
// editor.RenderDiffHTML.

// lexerFor returns the chroma lexer matching path's name/extension (by its
// registered filename globs, e.g. "*.go", "*.md"), or nil when no lexer
// recognizes it — an uncommon/unknown file type keeps the plain character-diff
// rendering RenderDiff already provides (never a hard failure). Coalesce merges
// adjacent same-type tokens, which keeps highlightLines' span count sane.
func lexerFor(path string) chroma.Lexer {
	l := lexers.Match(path)
	if l == nil {
		return nil
	}
	return chroma.Coalesce(l)
}

// highlightLines tokenizes src with lexer and returns one syntax-highlighted
// HTML fragment per source line, aligned index-for-index with splitLines' line
// count/semantics (internal/editor/diff.go): "\r\n" normalized to "\n", and the
// same one-stripped-trailing-newline convention, so RenderDiffHTML can index
// straight into the result with a diffOp's line index. A nil lexer or an empty
// source returns nil (no highlighting; the caller falls back to the plain
// diff), matching RenderDiff's RenderDiffHTML(old, new, nil, nil) no-op case.
//
// chroma's own Iterator doc warns a malformed/adversarial input can surface as
// a PANIC from the iterator rather than an error (chroma/v2's Iterator type:
// "If an error occurs within an Iterator, it may propagate this in a panic.
// Formatters should recover."). This renders arbitrary repo file content on
// every panel poll, so a lexer edge case must degrade to "no highlighting" —
// never crash the request — hence the recover.
func highlightLines(src string, lexer chroma.Lexer) (lines []template.HTML) {
	if lexer == nil {
		return nil
	}
	norm := strings.ReplaceAll(src, "\r\n", "\n")
	if norm == "" {
		return nil // splitLines("") == nil: zero lines, nothing to highlight
	}
	body := strings.TrimSuffix(norm, "\n")
	defer func() {
		if recover() != nil {
			lines = nil // degrade to the plain diff rather than crash the request
		}
	}()
	it, err := lexer.Tokenise(nil, body)
	if err != nil {
		return nil // degrade to the plain diff rather than fail the render
	}
	return tokensToLines(it.Tokens())
}

// tokensToLines assembles chroma's token stream into per-line HTML, splitting
// each token's value on embedded newlines (a multi-line token, e.g. a block
// comment or fenced code body, spans several output lines) and wrapping each
// non-empty chunk in a <span class="hl-X"> for the token's class (tokenClass);
// an unclassified chunk (e.g. plain text/punctuation) is emitted un-wrapped, but
// always HTML-escaped — untrusted repo content must never reach the page raw.
// The final accumulated line is ALWAYS appended once, unconditionally (even
// when empty), so the result has exactly as many entries as
// strings.Split(body, "\n") — the same invariant splitLines relies on.
func tokensToLines(toks []chroma.Token) []template.HTML {
	var lines []template.HTML
	var cur strings.Builder
	for _, tok := range toks {
		cls := tokenClass(tok.Type)
		parts := strings.Split(tok.Value, "\n")
		for i, part := range parts {
			if part != "" {
				escaped := template.HTMLEscapeString(part)
				if cls != "" {
					cur.WriteString(`<span class="hl-`)
					cur.WriteString(cls)
					cur.WriteString(`">`)
					cur.WriteString(escaped)
					cur.WriteString(`</span>`)
				} else {
					cur.WriteString(escaped)
				}
			}
			if i < len(parts)-1 {
				lines = append(lines, template.HTML(cur.String()))
				cur.Reset()
			}
		}
	}
	lines = append(lines, template.HTML(cur.String()))
	return lines
}

// tokenClass buckets a chroma token type into one of a small set of CSS
// classes this stylesheet themes (see assets/style.css's "Syntax highlighting"
// section), rather than emitting one of chroma's ~90 lexer-specific subtype
// codes verbatim: every subtype still colors sensibly (a token type not called
// out below falls through via chroma's Category/SubCategory grouping to its
// nearest styled ancestor, e.g. any Keyword* becomes "kw"), while the CSS stays
// small and readable. "" (plain text, punctuation, whitespace) means no span —
// the run inherits the surrounding text color.
func tokenClass(t chroma.TokenType) string {
	switch {
	case t.InCategory(chroma.Keyword):
		return "kw"
	case t.InSubCategory(chroma.NameFunction): // NameFunction, NameFunctionMagic
		return "fn"
	case t.InSubCategory(chroma.NameBuiltin): // NameBuiltin, NameBuiltinPseudo
		return "bi"
	case t.InSubCategory(chroma.NameVariable): // NameVariable and its subtypes
		return "var"
	case t == chroma.NameClass:
		return "cl"
	case t == chroma.NameDecorator:
		return "dec"
	case t == chroma.NameTag:
		return "tag"
	case t.InCategory(chroma.Name):
		return "nm"
	case t.InSubCategory(chroma.LiteralString):
		return "str"
	case t.InSubCategory(chroma.LiteralNumber):
		return "num"
	case t.InCategory(chroma.Comment):
		return "com"
	case t.InCategory(chroma.Operator):
		return "op"
	case t == chroma.GenericHeading, t == chroma.GenericSubheading:
		return "head"
	case t == chroma.GenericStrong:
		return "strong"
	case t == chroma.GenericEmph:
		return "emph"
	case t == chroma.GenericDeleted:
		return "del"
	case t == chroma.GenericInserted:
		return "ins"
	case t == chroma.Error, t == chroma.GenericError:
		return "err"
	default:
		return ""
	}
}
