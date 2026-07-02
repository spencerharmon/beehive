package swarm

import "strings"

// lineDiff returns a compact line diff of oldText -> newText: removed lines are
// prefixed "-", added lines "+", and unchanged lines are dropped entirely. It is
// the "feed a diff, not a full re-read" primitive for the working-set cut — when
// a file the agent already saw changes, the runner sends only the changed lines
// instead of the whole file.
//
// It trims the common leading/trailing lines first (the common case is a small
// localized edit) and runs a longest-common-subsequence backtrace only over the
// differing middle, so a one-line change in a large file yields a one-line diff
// and the LCS table stays small. Identical inputs yield "".
func lineDiff(oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	old := splitLines(oldText)
	neu := splitLines(newText)

	// Common prefix / suffix: unchanged surrounding lines are not part of the diff.
	start := 0
	for start < len(old) && start < len(neu) && old[start] == neu[start] {
		start++
	}
	endOld, endNew := len(old), len(neu)
	for endOld > start && endNew > start && old[endOld-1] == neu[endNew-1] {
		endOld--
		endNew--
	}
	midOld := old[start:endOld]
	midNew := neu[start:endNew]

	var b strings.Builder
	emitMiddleDiff(&b, midOld, midNew)
	return b.String()
}

// emitMiddleDiff writes the +/- lines for the differing region. It uses an LCS
// backtrace so only genuinely changed lines are emitted; when the region is huge
// (a near-total rewrite) it falls back to a straight replacement to bound memory
// — still a correct diff, just not minimal.
func emitMiddleDiff(b *strings.Builder, old, neu []string) {
	n, m := len(old), len(neu)
	if n == 0 && m == 0 {
		return
	}
	// Bound the LCS table; a genuine full rewrite is rare and does not need the
	// minimal edit script.
	const maxCells = 1 << 20 // 1M cells
	if n == 0 || m == 0 || n*m > maxCells {
		for _, l := range old {
			b.WriteString("-" + l + "\n")
		}
		for _, l := range neu {
			b.WriteString("+" + l + "\n")
		}
		return
	}
	// lcs[i][j] = length of the LCS of old[i:] and neu[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if old[i] == neu[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case old[i] == neu[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			b.WriteString("-" + old[i] + "\n")
			i++
		default:
			b.WriteString("+" + neu[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		b.WriteString("-" + old[i] + "\n")
	}
	for ; j < m; j++ {
		b.WriteString("+" + neu[j] + "\n")
	}
}

// splitLines splits s into lines, ignoring a single trailing newline so that
// "a\nb\n" and "a\nb" both yield ["a","b"]. Empty (or newline-only) content
// yields no lines.
func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
