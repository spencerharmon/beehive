// Package editor runs collaborative, single-file chat editing sessions. Each
// session is a branch on a throwaway worktree of the beehive repo: an opencode
// agent edits exactly one file there while the user chats, beehived renders the
// live diff (changed lines and columns highlighted), and a merge publishes the
// branch to main. The same agent is reachable over HTTP (browser UI) and JSON
// (API clients).
package editor

import (
	"html/template"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// diffOp is one step of the line-level edit script.
type diffOp struct {
	kind byte // 'e' equal, '-' delete (old only), '+' insert (new only)
	text string
}

// diffLines returns the line edit script transforming a into b, computed by a
// longest-common-subsequence DP. Inputs are small coordination files, so O(n*m)
// is fine.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// lcs[i][j] = LCS length of a[i:], b[j:]
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{'e', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// charDiff returns HTML for an old/new line pair where the runes that differ are
// wrapped in <span class="chg">…</span>, so column-level edits stand out within
// a changed line. Computed with a rune LCS.
func charDiff(old, new string) (oldHTML, newHTML template.HTML) {
	ar, br := []rune(old), []rune(new)
	n, m := len(ar), len(br)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if ar[i] == br[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ob, nb strings.Builder
	i, j := 0, 0
	emit := func(sb *strings.Builder, inSpan *bool, want bool, r rune) {
		if want != *inSpan {
			if want {
				sb.WriteString(`<span class="chg">`)
			} else {
				sb.WriteString(`</span>`)
			}
			*inSpan = want
		}
		sb.WriteString(template.HTMLEscapeString(string(r)))
	}
	oSpan, nSpan := false, false
	for i < n && j < m {
		switch {
		case ar[i] == br[j]:
			emit(&ob, &oSpan, false, ar[i])
			emit(&nb, &nSpan, false, br[j])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			emit(&ob, &oSpan, true, ar[i])
			i++
		default:
			emit(&nb, &nSpan, true, br[j])
			j++
		}
	}
	for ; i < n; i++ {
		emit(&ob, &oSpan, true, ar[i])
	}
	for ; j < m; j++ {
		emit(&nb, &nSpan, true, br[j])
	}
	if oSpan {
		ob.WriteString(`</span>`)
	}
	if nSpan {
		nb.WriteString(`</span>`)
	}
	return template.HTML(ob.String()), template.HTML(nb.String())
}

// DiffRow is one rendered line of the unified diff view.
type DiffRow struct {
	Kind string        // eq | add | del
	HTML template.HTML // escaped content, intra-line changes span-wrapped
}

// RenderDiff produces a unified-diff rendering of the change from old to new:
// equal lines as context, deletions and additions highlighted, and adjacent
// delete/insert runs paired so the changed columns within a line are marked.
func RenderDiff(old, new string) []DiffRow {
	ops := diffLines(splitLines(old), splitLines(new))
	var rows []DiffRow
	for k := 0; k < len(ops); k++ {
		op := ops[k]
		switch op.kind {
		case 'e':
			rows = append(rows, DiffRow{"eq", template.HTML(template.HTMLEscapeString(op.text))})
		case '-':
			// Gather the contiguous delete run and any insert run that follows it;
			// pair them index-wise for column-level highlighting (a "replace").
			var dels, adds []string
			for k < len(ops) && ops[k].kind == '-' {
				dels = append(dels, ops[k].text)
				k++
			}
			for k < len(ops) && ops[k].kind == '+' {
				adds = append(adds, ops[k].text)
				k++
			}
			k-- // outer loop will k++
			paired := len(dels)
			if len(adds) < paired {
				paired = len(adds)
			}
			for p := 0; p < paired; p++ {
				oh, nh := charDiff(dels[p], adds[p])
				rows = append(rows, DiffRow{"del", oh})
				rows = append(rows, DiffRow{"add", nh})
			}
			for p := paired; p < len(dels); p++ {
				rows = append(rows, DiffRow{"del", template.HTML(template.HTMLEscapeString(dels[p]))})
			}
			for p := paired; p < len(adds); p++ {
				rows = append(rows, DiffRow{"add", template.HTML(template.HTMLEscapeString(adds[p]))})
			}
		case '+':
			rows = append(rows, DiffRow{"add", template.HTML(template.HTMLEscapeString(op.text))})
		}
	}
	return rows
}

// RenderDiffFile is RenderDiff plus per-line syntax highlighting for a language
// recognized from filename (matched by extension/basename against chroma's
// lexer set, e.g. "x.go", "x.py", "README.md", "Makefile" — chat-editor-snappy-
// polish). Each row's text is colorized by tokenizing the WHOLE old/new source
// (so multi-line constructs like block comments lex correctly) and slicing the
// result back into per-line HTML fragments that line up with the same
// line-level diff (diffLines) RenderDiff uses; the add/del/eq line coloring is
// unchanged. A recognized language replaces the intra-line "chg" replace-column
// highlighting with token colors instead of layering both (avoids overlapping
// span nesting). Falls back to RenderDiff's plain rendering — unchanged,
// including its "chg" spans — whenever no lexer matches filename, or the lexer's
// line count cannot be lined up 1:1 with the plain split (a defensive fallback
// so a lexer quirk on unusual input degrades to today's rendering instead of
// ever mis-rendering or panicking).
func RenderDiffFile(old, new, filename string) []DiffRow {
	lex := lexers.Match(filename)
	if lex == nil {
		return RenderDiff(old, new)
	}
	oldLines := splitLines(old)
	newLines := splitLines(new)
	oldColored, ok := tokenizeLines(lex, old, len(oldLines))
	if !ok {
		return RenderDiff(old, new)
	}
	newColored, ok := tokenizeLines(lex, new, len(newLines))
	if !ok {
		return RenderDiff(old, new)
	}
	ops := diffLines(oldLines, newLines)
	var rows []DiffRow
	oi, ni := 0, 0
	for _, op := range ops {
		switch op.kind {
		case 'e':
			rows = append(rows, DiffRow{"eq", oldColored[oi]})
			oi++
			ni++
		case '-':
			rows = append(rows, DiffRow{"del", oldColored[oi]})
			oi++
		case '+':
			rows = append(rows, DiffRow{"add", newColored[ni]})
			ni++
		}
	}
	return rows
}

// tokenizeLines tokenizes src with lex and renders each source line as an HTML
// fragment (escaped text, with a `tok-<class>` span per non-trivial token; see
// chroma.StandardTypes for the short class names). ok is false whenever the
// tokenizer errors or its line count disagrees with wantLines, so the caller can
// safely fall back to the plain renderer instead of indexing out of range or
// silently misaligning rows with the wrong source line.
func tokenizeLines(lex chroma.Lexer, src string, wantLines int) (out []template.HTML, ok bool) {
	it, err := chroma.Coalesce(lex).Tokenise(nil, src)
	if err != nil {
		return nil, false
	}
	lines := chroma.SplitTokensIntoLines(it.Tokens())
	if len(lines) != wantLines {
		return nil, false
	}
	out = make([]template.HTML, len(lines))
	for i, toks := range lines {
		var b strings.Builder
		for _, t := range toks {
			v := strings.TrimSuffix(t.Value, "\n")
			if v == "" {
				continue
			}
			esc := template.HTMLEscapeString(v)
			cls := chroma.StandardTypes[t.Type]
			if cls == "" {
				b.WriteString(esc)
				continue
			}
			b.WriteString(`<span class="tok-`)
			b.WriteString(cls)
			b.WriteString(`">`)
			b.WriteString(esc)
			b.WriteString(`</span>`)
		}
		out[i] = template.HTML(b.String())
	}
	return out, true
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
