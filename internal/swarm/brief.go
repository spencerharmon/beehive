package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// Bounds on the injected brief so precompute SHRINKS the working set rather than
// re-bloating it: each file excerpt is capped, and the number of files pulled in
// is capped. The agent may still read more from the worktree if a task needs it
// (the brief sets the floor, not a cage).
const (
	maxExcerptLines = 80
	maxExcerptBytes = 4000
	maxBriefFiles   = 12
	maxDirEntries   = 60
)

// workBrief assembles the precomputed task brief the runner injects for a Work
// dispatch, so the agent never re-runs discovery git plumbing or reads the whole
// submodule tree to get its bearings. Every value is one the Work setup ALREADY
// resolved — the code-worktree path, the branch, the submodule pointer the
// worktree branched from (synced to the tracked tip), the task's PLAN card — plus
// the protocol's deterministic choices turned into concrete answers (the exact
// change-doc path and commit stamp, not a formula) and excerpts of ONLY the files
// the task's `Files:` line names. Nothing here re-derives the setup: callers pass
// the same resolved worktree/branch/pointer the pass actually got, so the brief
// can never drift from reality.
func (r *Runner) workBrief(sel *selectt.Selection, branch, worktreeAbs, worktreeRel, pointer, trackedBranch string) string {
	id := sel.Task.ID
	sm := sel.Submodule.Name
	docPath := fmt.Sprintf("submodules/%s/docs/%s-%s.md", sm, branch, id)
	stamp := fmt.Sprintf("Beehive: %s %s", id, docPath)

	var b strings.Builder
	b.WriteString("# Task brief (precomputed by the runner — these are resolved for you; do not re-derive them)\n")
	fmt.Fprintf(&b, "Code worktree (edit the submodule's CODE here, cwd-relative): %s\n", worktreeRel)
	fmt.Fprintf(&b, "Branch: %s\n", branch)
	fmt.Fprintf(&b, "Submodule pointer / tracked tip (your worktree branches from this commit): %s\n", pointer)
	fmt.Fprintf(&b, "Tracked branch (work converges here): %s\n", trackedBranch)
	fmt.Fprintf(&b, "Change-doc path (write it EXACTLY here, in the beehive layer): %s\n", docPath)
	fmt.Fprintf(&b, "Commit stamp (include this line verbatim in the submodule commit): %s\n", stamp)

	b.WriteString("\n## Task card\n")
	b.WriteString(taskCard(sel.Task))

	files := taskFiles(sel.Task)
	if len(files) > 0 {
		b.WriteString("\n## Task files (your working set — read these, not the whole tree)\n")
		if len(files) > maxBriefFiles {
			files = files[:maxBriefFiles]
		}
		for _, f := range files {
			b.WriteString(fileExcerpt(worktreeAbs, f))
		}
	}
	return b.String() + "\n"
}

// taskCard renders the task's own PLAN.md entry (a concise header + its body) so
// the agent has the task verbatim without re-reading PLAN.md. The claim metadata
// (session/heartbeat) is intentionally omitted: it is runner-managed noise the
// agent must not touch.
func taskCard(t plan.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s [%s]", t.ID, t.Status)
	if len(t.Deps) > 0 {
		fmt.Fprintf(&b, " (deps: %s)", strings.Join(t.Deps, ", "))
	}
	b.WriteString("\n")
	if len(t.Body) > 0 {
		b.WriteString(strings.Join(t.Body, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

var (
	filesLabelRe = regexp.MustCompile(`^\s*Files:\s*(.*)$`)
	// A subsequent PLAN-card field label (Doc:, Accept:, Impl:, Review:, …) ends
	// the wrapped Files: value. Wrapped continuation prose does not begin with a
	// "Capitalized:" label, so requiring the trailing colon keeps genuine path
	// continuations (which start lowercase / with a path) from terminating early.
	fieldLabelRe = regexp.MustCompile(`^\s*[A-Z][A-Za-z-]*:`)
)

// taskFiles returns the file/dir/glob paths named on the task card's `Files:`
// line (the convention PLAN cards use to scope a task's working set). The value
// may wrap across body lines and carry parenthetical annotations; both are
// handled. Returns nil when the card has no Files: line.
func taskFiles(t plan.Task) []string {
	var raw string
	collecting := false
	for _, line := range t.Body {
		if !collecting {
			if m := filesLabelRe.FindStringSubmatch(line); m != nil {
				raw = m[1]
				collecting = true
			}
			continue
		}
		if strings.TrimSpace(line) == "" || fieldLabelRe.MatchString(line) {
			break
		}
		raw += " " + strings.TrimSpace(line)
	}
	if !collecting {
		return nil
	}
	return parseFileList(raw)
}

// parseFileList extracts path tokens from a raw Files: value: parenthetical
// annotations are dropped (they may contain commas), the remainder is split on
// commas, and each token is trimmed of surrounding space and a trailing period,
// keeping only its leading whitespace-delimited path word. Non-path tokens
// (prose fragments) are discarded. Order-preserving and de-duplicated.
func parseFileList(s string) []string {
	s = stripParens(s)
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimRight(part, ". \t")
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		p := fields[0]
		if !looksLikePath(p) || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// stripParens removes parenthetical spans (handling nesting) so an annotation's
// internal punctuation never confuses comma-splitting.
func stripParens(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// looksLikePath reports whether a token is a plausible repo path (a path
// separator, a filename extension, or a glob metacharacter), filtering out prose
// words that survived comma-splitting.
func looksLikePath(p string) bool {
	if p == "" {
		return false
	}
	return strings.ContainsAny(p, "/.*?[")
}

// fileExcerpt renders a bounded excerpt of one task file resolved against the
// code worktree. A glob expands to its matches; a directory lists its entries; a
// missing path is reported (it may be a file the task will CREATE) rather than
// swallowed. Reads are confined to the worktree — a path escaping it is skipped.
func fileExcerpt(worktreeAbs, rel string) string {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Sprintf("### %s\n(skipped: path is outside the worktree)\n", rel)
	}
	abs := filepath.Join(worktreeAbs, clean)
	if strings.ContainsAny(rel, "*?[") {
		matches, _ := filepath.Glob(abs)
		if len(matches) == 0 {
			return fmt.Sprintf("### %s\n(no matches at the worktree base)\n", rel)
		}
		var b strings.Builder
		for _, m := range matches {
			mrel, err := filepath.Rel(worktreeAbs, m)
			if err != nil {
				continue
			}
			b.WriteString(fileExcerpt(worktreeAbs, mrel))
		}
		return b.String()
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Sprintf("### %s\n(not present at the worktree base — create it if the task adds it)\n", rel)
	}
	if fi.IsDir() {
		return dirListing(worktreeAbs, clean)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("### %s\n(unreadable: %v)\n", rel, err)
	}
	return renderExcerpt(filepath.ToSlash(clean), data)
}

// renderExcerpt formats a file's contents as a fenced, line-and-byte-bounded
// block, noting the true length and any truncation so the agent knows to read
// the rest from the worktree when it needs more.
func renderExcerpt(rel string, data []byte) string {
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // drop the empty element from a trailing newline
	}
	total := len(lines)
	truncated := false
	if len(lines) > maxExcerptLines {
		lines = lines[:maxExcerptLines]
		truncated = true
	}
	body := strings.Join(lines, "\n")
	if len(body) > maxExcerptBytes {
		body = body[:maxExcerptBytes]
		truncated = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### %s (%d lines)\n", rel, total)
	b.WriteString("```\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	if truncated {
		fmt.Fprintf(&b, "(excerpt truncated to %d lines / %d bytes — read the file in the worktree for the rest)\n", maxExcerptLines, maxExcerptBytes)
	}
	return b.String()
}

// dirListing lists a directory's immediate entries (names only, bounded) so a
// Files: entry naming a package directory orients the agent to its members
// without dumping their contents.
func dirListing(worktreeAbs, rel string) string {
	ents, err := os.ReadDir(filepath.Join(worktreeAbs, rel))
	if err != nil {
		return fmt.Sprintf("### %s/\n(unreadable directory: %v)\n", filepath.ToSlash(rel), err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### %s/ (directory — %d entries)\n", filepath.ToSlash(rel), len(ents))
	for i, e := range ents {
		if i >= maxDirEntries {
			b.WriteString("- …\n")
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		b.WriteString("- " + name + "\n")
	}
	return b.String()
}
