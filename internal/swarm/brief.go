package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// Brief bounds. The whole point of the brief is to CUT tokens, so excerpts are
// capped per file and in aggregate: it orients the agent (the floor), it is not
// a mirror of the tree (a cage).
const (
	briefMaxFiles     = 12
	briefMaxFileLines = 40
	briefMaxFileBytes = 4096
	briefDirEntries   = 40
)

// workBrief precomputes the compact task brief injected into a Work dispatch so
// the honeybee never re-derives what the runner already resolved: the code
// worktree path, its branch, and the submodule base the Work setup produced; the
// deterministic protocol choices the agent would otherwise compute (the change
// doc path and the commit stamp); the task's own PLAN card; and excerpts of
// exactly the files the task touches (its `Files:` line) — scoping the working
// set to those files, not the whole submodule tree.
//
// It reuses the values the Work setup computed (wtAbs, branch) rather than
// re-deriving them, so the brief reflects what the pass actually got. It is
// additive and best-effort: a failed sub-read degrades to an inline note, never
// a run failure — the brief sets a floor, it is not a cage.
func (r *Runner) workBrief(ctx context.Context, sel *selectt.Selection, wtAbs, branch string) string {
	sm := sel.Submodule.Name
	id := sel.Task.ID
	wtRel := filepath.ToSlash(filepath.Join("submodules", sm, "worktrees", branch))
	docPath := fmt.Sprintf("submodules/%s/docs/%s-%s.md", sm, branch, id)
	stamp := fmt.Sprintf("Beehive: %s %s", id, docPath)

	var b strings.Builder
	b.WriteString("# Task brief (PRECOMPUTED by the runner — you need not rediscover any of this)\n")
	fmt.Fprintf(&b, "Worktree: %s (already created; branch %s is checked out — edit the code here, never submodules/%s/repo)\n", wtRel, branch, sm)
	fmt.Fprintf(&b, "Branch: %s\n", branch)
	if base := briefBase(ctx, wtAbs); base != "" {
		fmt.Fprintf(&b, "Submodule base: %s (the commit your worktree branches from — the submodule pointer/tracked tip the runner resolved for this pass)\n", base)
	}
	fmt.Fprintf(&b, "Change doc — write EXACTLY here (the completion check looks only at this path): %s\n", docPath)
	fmt.Fprintf(&b, "Commit stamp — put VERBATIM in your code commit message: %s\n", stamp)

	b.WriteString("\n## Task card (already resolved from PLAN.md — no need to re-read the plan to orient)\n")
	b.WriteString(taskCard(&sel.Task))

	files := taskFiles(sel.Task.Body)
	b.WriteString("\n## Task files (your working set is THESE files — the task's Files:, not the whole tree)\n")
	if len(files) == 0 {
		b.WriteString("(the task card declares no Files: line — read what the task needs)\n")
	} else {
		b.WriteString(fileExcerpts(wtAbs, files))
	}
	b.WriteString("\nThis brief is the floor, not a cage: read more if the task truly needs it, but you should not need git plumbing or a tree scan to get your bearings.\n\n")
	return b.String()
}

// briefBase returns the full SHA the code worktree is on (the base the Work setup
// branched it from). "" when it cannot be resolved, so the brief simply omits the
// line rather than failing the pass.
func briefBase(ctx context.Context, wtAbs string) string {
	sha, err := git.New(wtAbs).RevParse(ctx, "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// taskCard renders the task's PLAN identity + verbatim body, so the agent has its
// card without re-reading PLAN.md. Session/heartbeat are deliberately omitted
// (claim internals, covered by the claim instructions).
func taskCard(t *plan.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] attempts=%d deps=%s", t.ID, t.Status, t.Attempts, strings.Join(t.Deps, ","))
	if t.Weight > 1 {
		fmt.Fprintf(&b, " weight=%d", t.Weight)
	}
	b.WriteString("\n")
	for _, line := range t.Body {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

var fieldLabelRe = regexp.MustCompile(`^[A-Za-z][A-Za-z-]*:`)

// taskFiles extracts the path tokens a task declares in its `Files:` line. The
// line is comma-separated, may wrap across continuation body lines, and carries
// human annotations the parser strips: parenthetical notes ("(Run loop)"), a
// leading "new " marker for a to-be-created file, a trailing period, and non-path
// prose ("tests", "commit-path guard") that is dropped. Splitting is paren-aware
// so a comma inside "(a, b)" never breaks an entry apart.
func taskFiles(body []string) []string {
	raw := filesLine(body)
	if raw == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, seg := range splitTopLevel(raw, ',') {
		p := leadingPath(seg)
		if p == "" || !pathLike(p) || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// filesLine returns the text after "Files:" in a task body, joining any wrapped
// continuation lines (a following body line that is neither blank nor a new
// "Label:" field) so a Files entry split by line-wrapping stays whole.
func filesLine(body []string) string {
	for i, line := range body {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Files:")
		if !ok {
			continue
		}
		parts := []string{strings.TrimSpace(rest)}
		for _, cont := range body[i+1:] {
			c := strings.TrimSpace(cont)
			if c == "" || fieldLabelRe.MatchString(c) {
				break
			}
			parts = append(parts, c)
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

// splitTopLevel splits s on sep at parenthesis depth 0, so a separator inside a
// "(...)" annotation does not break an entry apart.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// leadingPath reduces one Files segment to the path token it names: drop any
// "(...)" annotation, a leading "new " marker, then take the first
// whitespace-delimited token and trim trailing sentence punctuation. "" when the
// segment carries no token.
func leadingPath(seg string) string {
	seg = strings.TrimSpace(stripParens(seg))
	if rest, ok := cutWordPrefix(seg, "new"); ok {
		seg = strings.TrimSpace(rest)
	}
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimRight(fields[0], ".,;:")
}

// stripParens removes every "(...)" group (annotations) from s, leaving the
// surrounding text.
func stripParens(s string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
				continue
			}
			b.WriteByte(s[i])
		default:
			if depth == 0 {
				b.WriteByte(s[i])
			}
		}
	}
	return b.String()
}

// cutWordPrefix strips a leading whole word (case-insensitive) plus its trailing
// space — e.g. the "new " marker on a to-be-created file. (rest, true) on a match.
func cutWordPrefix(s, word string) (string, bool) {
	if len(s) > len(word) && strings.EqualFold(s[:len(word)], word) && s[len(word)] == ' ' {
		return s[len(word)+1:], true
	}
	return "", false
}

// pathLike reports whether a token looks like a file path / glob rather than
// prose, so bare words ("tests", "guard") are dropped from the working set.
func pathLike(tok string) bool { return strings.ContainsAny(tok, "/.*?[") }

// fileExcerpts renders the task's files, capped in aggregate so the brief stays
// compact.
func fileExcerpts(wtAbs string, paths []string) string {
	var b strings.Builder
	shown := 0
	for _, p := range paths {
		if shown >= briefMaxFiles {
			fmt.Fprintf(&b, "… (%d more task file(s) not shown; open them in the worktree as needed)\n", len(paths)-shown)
			break
		}
		b.WriteString(renderPath(wtAbs, p))
		shown++
	}
	return b.String()
}

// renderPath renders one task-file entry resolved against the worktree: a glob
// expands to its matches, a directory to a bounded listing, a file to a bounded
// head excerpt, and an absent path to a "create it" note.
func renderPath(wtAbs, rel string) string {
	full := filepath.Join(wtAbs, filepath.FromSlash(rel))
	if strings.ContainsAny(rel, "*?[") {
		matches, err := filepath.Glob(full)
		if err != nil || len(matches) == 0 {
			return fmt.Sprintf("### %s\n(no files match this pattern in the worktree)\n", rel)
		}
		sort.Strings(matches)
		var b strings.Builder
		for i, m := range matches {
			if i >= briefMaxFiles {
				fmt.Fprintf(&b, "… (%d more match(es) not shown)\n", len(matches)-i)
				break
			}
			mr, err := filepath.Rel(wtAbs, m)
			if err != nil {
				mr = m
			}
			b.WriteString(renderEntry(filepath.ToSlash(mr), m))
		}
		return b.String()
	}
	return renderEntry(rel, full)
}

// renderEntry renders a single resolved path (dir listing, file head, or absent).
func renderEntry(rel, full string) string {
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("### %s\n(not present in the worktree — create it if the task calls for it)\n", rel)
		}
		return fmt.Sprintf("### %s\n(unreadable: %v)\n", rel, err)
	}
	if info.IsDir() {
		return fmt.Sprintf("### %s/ (directory)\n%s", rel, dirListing(full))
	}
	return renderFile(rel, full)
}

// renderFile emits a bounded head excerpt of a file (line- and byte-capped),
// noting when it was truncated.
func renderFile(rel, full string) string {
	data, err := os.ReadFile(full)
	if err != nil {
		return fmt.Sprintf("### %s\n(unreadable: %v)\n", rel, err)
	}
	total := strings.Count(string(data), "\n") + 1
	truncated := false
	if len(data) > briefMaxFileBytes {
		data = data[:briefMaxFileBytes]
		truncated = true
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > briefMaxFileLines {
		lines = lines[:briefMaxFileLines]
		truncated = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### %s (%d lines)\n```\n", rel, total)
	b.WriteString(strings.Join(lines, "\n"))
	if len(lines) == 0 || lines[len(lines)-1] != "" {
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	if truncated {
		fmt.Fprintf(&b, "(excerpt truncated — open %s in the worktree for the rest)\n", rel)
	}
	return b.String()
}

// dirListing lists a directory's entries (bounded), marking subdirectories.
func dirListing(full string) string {
	ents, err := os.ReadDir(full)
	if err != nil {
		return fmt.Sprintf("(unreadable: %v)\n", err)
	}
	var b strings.Builder
	b.WriteString("```\n")
	for i, e := range ents {
		if i >= briefDirEntries {
			fmt.Fprintf(&b, "… (%d more)\n", len(ents)-i)
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		b.WriteString(name)
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	return b.String()
}
