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
)

// diffOp is one step of the line-level edit script. ai/bi are the source line
// indices this op came from (into the a/b slices diffLines was called with),
// -1 when not applicable (an insert has no ai, a delete has no bi) — chat-
// editor-snappy-polish's RenderDiffHTML uses them to look up a precomputed
// syntax-highlighted rendering of the SAME line instead of the plain escape.
type diffOp struct {
	kind byte // 'e' equal, '-' delete (old only), '+' insert (new only)
	text string
	ai   int
	bi   int
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
			ops = append(ops, diffOp{'e', a[i], i, j})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', a[i], i, -1})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j], -1, j})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i], i, -1})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j], -1, j})
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
// Equivalent to RenderDiffHTML(old, new, nil, nil) — every existing caller (the
// single-file editor, the skills diff view) keeps this exact character-diff
// rendering.
func RenderDiff(old, new string) []DiffRow {
	return RenderDiffHTML(old, new, nil, nil)
}

// RenderDiffHTML is RenderDiff plus OPTIONAL precomputed per-line HTML (e.g. from
// a syntax-highlighting tokenizer): oldHTML[i] backs old's line i, newHTML[j]
// backs new's line j (see splitLines for the line-index convention). Chat-
// editor-snappy-polish uses it so the chat-diff view can show colorized/
// language-highlighted lines; when a language/tokenizer is not available for a
// given line, or oldHTML/newHTML are both nil (RenderDiff's case), the row falls
// back to today's plain-escaped / intra-line character-diff rendering unchanged.
//
// Highlighted lines are shown WHOLE (no intra-line character-diff span) — mixing
// per-token syntax spans with the rune-level replace highlighting below is not
// attempted, so a highlighted row always renders its own precomputed HTML
// verbatim, add/del/eq context coming from the row's CSS background alone.
func RenderDiffHTML(old, new string, oldHTML, newHTML []template.HTML) []DiffRow {
	ops := diffLines(splitLines(old), splitLines(new))
	highlighted := oldHTML != nil || newHTML != nil
	lineHTML := func(ai, bi int) (template.HTML, bool) {
		if ai >= 0 && ai < len(oldHTML) {
			return oldHTML[ai], true
		}
		if bi >= 0 && bi < len(newHTML) {
			return newHTML[bi], true
		}
		return "", false
	}
	var rows []DiffRow
	for k := 0; k < len(ops); k++ {
		op := ops[k]
		if highlighted {
			if h, ok := lineHTML(op.ai, op.bi); ok {
				rows = append(rows, DiffRow{diffRowKind(op.kind), h})
				continue
			}
		}
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

// diffRowKind maps a diffOp's kind byte to DiffRow's Kind string.
func diffRowKind(kind byte) string {
	switch kind {
	case 'e':
		return "eq"
	case '-':
		return "del"
	default:
		return "add"
	}
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
