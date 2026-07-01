// Task brief: a compact, runner-PRECOMPUTED handoff injected into a Work
// honeybee's first prompt so the agent does not burn turns rediscovering
// mechanics the runner already resolved when it set the pass up. It carries the
// resolved code-worktree path, the branch, the submodule base commit the
// worktree branched from (the synced tracked tip), the protocol's deterministic
// choices (the change-doc path and the commit stamp — the answers, not the
// formula), the task's own PLAN card, and bounded excerpts of EXACTLY the files
// the task touches (from the card's `Files:` line). It scopes the agent's
// working set to those files instead of the whole submodule tree.
//
// The brief is assembled only from values the Work setup already resolved
// (branch, worktree path, base pointer), so it never adds a second, divergent
// derivation. It is additive to correctness — a floor (no rediscovery), not a
// cage: the agent may still read more if a task needs it, and an install with no
// task Files simply gets a brief with no excerpts.
package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// briefExcerptLines bounds how much of each task file the brief inlines. The
// point is to CUT tokens, so the brief gives the head of each file as a starting
// orientation rather than the whole thing; the agent reads further only if the
// task needs it.
const briefExcerptLines = 40

// taskBrief is the runner-precomputed Work handoff. Every field is sourced from
// what the pass actually got, not re-derived.
type taskBrief struct {
	Submodule   string
	TaskID      string
	Branch      string        // bee-<taskid>
	Worktree    string        // resolved code-worktree path, relative to the repo root
	Pointer     string        // submodule commit the worktree branched from (the synced tracked tip)
	DocPath     string        // submodules/<sm>/docs/<branch>-<taskid>.md
	CommitStamp string        // Beehive: <taskid> <doc-path>
	Card        string        // the task's PLAN.md card (header + body)
	Files       []string      // task-touched paths parsed from the card's Files: line
	Excerpts    []fileExcerpt // one per Files entry, scoped to those files only
}

// fileExcerpt is a bounded view of one task file inside the created worktree.
type fileExcerpt struct {
	Path      string
	Content   string
	Truncated bool // a longer file was cut to briefExcerptLines
	Missing   bool // listed in Files: but absent from the worktree (a file to CREATE)
	Dir       bool // the Files entry is a directory; Content lists its immediate entries
}

// buildTaskBrief assembles the Work brief from the values the Work setup already
// resolved: the submodule, the selected task (with its parsed Body), the branch,
// the resolved worktree path (wtRel relative to the repo root; wtAbs on disk),
// and the base pointer the worktree branched from. It derives the mandated
// doc-path and commit stamp deterministically (so the agent is told the answers)
// and inlines a bounded excerpt of each file named on the card's `Files:` line,
// scoped to those files — never the whole tree. Pure assembly; the only IO is
// reading the already-created worktree's task files.
func buildTaskBrief(sub repo.Submodule, t plan.Task, branch, wtRel, wtAbs, pointer string) taskBrief {
	docPath := fmt.Sprintf("submodules/%s/docs/%s-%s.md", sub.Name, branch, t.ID)
	b := taskBrief{
		Submodule:   sub.Name,
		TaskID:      t.ID,
		Branch:      branch,
		Worktree:    wtRel,
		Pointer:     pointer,
		DocPath:     docPath,
		CommitStamp: "Beehive: " + t.ID + " " + docPath,
		Card:        taskCard(t),
		Files:       parseFilesLine(t.Body),
	}
	for _, f := range b.Files {
		b.Excerpts = append(b.Excerpts, readExcerpt(wtAbs, f))
	}
	return b
}

// taskCard reconstructs a readable PLAN card (a synthetic header line plus the
// verbatim body) from the selected task, so the brief carries the task's intent
// without the agent re-reading PLAN.md.
func taskCard(t plan.Task) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s [%s]\n", t.ID, t.Status)
	for _, line := range t.Body {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// parseFilesLine extracts the task-touched paths from a PLAN card's `Files:`
// line. The real format is a comma-separated list that may carry parenthetical
// notes (which can themselves contain commas), e.g.
//
//	Files: internal/swarm (brief assembly, injection), swarm_test.go.
//
// PLAN cards are markdown soft-wrapped, so this value routinely straddles line
// breaks — and a parenthetical note can open on the `Files:` line and close on a
// continuation line. The value is therefore accumulated across the following body
// lines until the next field label (Doc:/Accept:/…) or a blank line, so a wrapped
// list parses whole. Then parentheticals are dropped (so their commas don't split
// entries), the remainder is split on commas, and each entry is trimmed of
// surrounding space and a trailing sentence period. Returns nil when there is no
// Files: line.
func parseFilesLine(body []string) []string {
	for i, line := range body {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Files:")
		if !ok {
			continue
		}
		parts := []string{rest}
		for _, next := range body[i+1:] {
			trimmed := strings.TrimSpace(next)
			if trimmed == "" || fieldLabelRe.MatchString(trimmed) {
				break // blank line or a new "Label:" field ends the wrapped value
			}
			parts = append(parts, next)
		}
		return splitFiles(strings.Join(parts, " "))
	}
	return nil
}

// fieldLabelRe matches a body line that opens a new "Label:" field (e.g. Doc:,
// Accept:), used to bound where a soft-wrapped Files: value ends.
var fieldLabelRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*:`)

func splitFiles(rest string) []string {
	rest = stripParens(rest)
	var out []string
	for _, part := range strings.Split(rest, ",") {
		p := strings.TrimSpace(part)
		p = strings.TrimSpace(strings.TrimRight(p, "."))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stripParens removes parenthetical annotations, which may contain commas, so
// the caller can safely split the remainder on commas. Handles nesting and drops
// an unbalanced trailing "(" span.
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

// readExcerpt reads a bounded head-of-file excerpt for one task file inside the
// created worktree. A directory entry (e.g. `internal/swarm`) is summarized by
// listing its immediate entries rather than dumping the subtree. A path absent
// from the worktree (a file the task will CREATE) is flagged Missing, never an
// error — the brief is best-effort and additive.
func readExcerpt(wtAbs, rel string) fileExcerpt {
	ex := fileExcerpt{Path: rel}
	abs := filepath.Join(wtAbs, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		ex.Missing = true
		return ex
	}
	if info.IsDir() {
		ex.Dir = true
		ents, err := os.ReadDir(abs)
		if err != nil {
			ex.Missing = true
			return ex
		}
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		ex.Content = strings.Join(names, "\n")
		return ex
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		ex.Missing = true
		return ex
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > briefExcerptLines {
		lines = lines[:briefExcerptLines]
		ex.Truncated = true
	}
	ex.Content = strings.Join(lines, "\n")
	return ex
}

// String renders the brief as the text block appended to the Work preamble.
func (b taskBrief) String() string {
	var sb strings.Builder
	sb.WriteString("## Task brief (runner-precomputed; do NOT rediscover these by exploring the tree)\n")
	fmt.Fprintf(&sb, "Submodule: %s\n", b.Submodule)
	fmt.Fprintf(&sb, "Code worktree (edit CODE here): %s  (branch %s)\n", b.Worktree, b.Branch)
	fmt.Fprintf(&sb, "Submodule base commit: %s  (the worktree branched from the synced tracked tip; this is the pointer this pass is based on)\n", b.Pointer)
	fmt.Fprintf(&sb, "Change doc to write (EXACT path — the completion check requires it here): %s\n", b.DocPath)
	fmt.Fprintf(&sb, "Commit stamp for the submodule commit (EXACT): %s\n", b.CommitStamp)
	sb.WriteString("Your working set is SCOPED to this task's Files (below); start there and read more only if the task needs it.\n\n")
	sb.WriteString("Task card:\n")
	sb.WriteString(b.Card)
	if len(b.Excerpts) > 0 {
		sb.WriteString("\nTask files (from the card's Files:):\n")
		for _, ex := range b.Excerpts {
			sb.WriteString(ex.render())
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

func (ex fileExcerpt) render() string {
	var sb strings.Builder
	switch {
	case ex.Missing:
		fmt.Fprintf(&sb, "\n### %s  (not present in the worktree — a new file to create)\n", ex.Path)
		return sb.String()
	case ex.Dir:
		fmt.Fprintf(&sb, "\n### %s/  (directory; immediate entries)\n%s\n", ex.Path, ex.Content)
		return sb.String()
	}
	fmt.Fprintf(&sb, "\n### %s", ex.Path)
	if ex.Truncated {
		fmt.Fprintf(&sb, "  (first %d lines)", briefExcerptLines)
	}
	sb.WriteString("\n```\n")
	sb.WriteString(ex.Content)
	if !strings.HasSuffix(ex.Content, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("```\n")
	return sb.String()
}
