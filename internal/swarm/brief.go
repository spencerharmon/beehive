// Task brief: the runner precomputes a compact, injected preamble for a Work
// dispatch so the honeybee never re-derives the mechanics the runner already
// resolved when it set the pass up. It hands the agent the answers — the resolved
// code-worktree path, the branch, the submodule base pointer + tracked tip, the
// mandated change-doc path and commit stamp (values, not formulas), the task's own
// PLAN card, and bounded excerpts of exactly the files the task's `Files:` line
// names — and scopes the working set to those files rather than the whole tree.
//
// It is additive to correctness: the agent may still open more if a task needs
// it. The brief sets the floor (no rediscovery), not a cage, and it reuses the
// values the Work setup already computed so it can never drift from the real pass.

package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// Brief bounds keep the injected preamble compact: each named file contributes a
// bounded HEAD excerpt (enough to orient, not the whole file) and a directory is
// listed, not walked. Cutting rediscovery must not balloon per-turn tokens.
const (
	briefFileMaxLines  = 40
	briefFileMaxBytes  = 1500
	briefDirMaxEntries = 40
)

// briefParenRe strips the parenthetical notes the free-form `Files:` line carries
// ("internal/swarm (brief assembly ...)") so only the path token survives.
var briefParenRe = regexp.MustCompile(`\([^)]*\)`)

// workBrief is the runner-precomputed handoff for one Work dispatch.
type workBrief struct {
	Worktree   string // resolved code-worktree path (edit here)
	Branch     string // bee-<taskid>
	Pointer    string // submodule pointer / worktree base commit
	TrackedTip string // origin tracked-branch tip; "" when there is no remote
	DocPath    string // submodules/<sm>/docs/<branch>-<taskid>.md (required doc)
	Stamp      string // Beehive: <taskid> <doc-path> (commit stamp, verbatim)
	Card       string // the task's own PLAN.md item (header + body)
	Files      []briefFile
}

// briefFile is one entry in the scoped working set: a path from the task's
// `Files:` line, a bounded excerpt (or directory listing), and an optional note.
type briefFile struct {
	Path    string
	Note    string
	Excerpt string
}

// buildWorkBrief assembles the brief from the runner's ALREADY-resolved
// worktree/branch/pointer/tip (no second, divergent derivation) plus the task
// card and the task-file excerpts read out of the worktree. Best-effort by
// design: an unreadable file is annotated, never fatal.
func buildWorkBrief(sel *selectt.Selection, worktree, branch, pointer, trackedTip string) workBrief {
	doc := fmt.Sprintf("submodules/%s/docs/%s-%s.md", sel.Submodule.Name, branch, sel.Task.ID)
	b := workBrief{
		Worktree:   worktree,
		Branch:     branch,
		Pointer:    pointer,
		TrackedTip: trackedTip,
		DocPath:    doc,
		Stamp:      fmt.Sprintf("Beehive: %s %s", sel.Task.ID, doc),
		Card:       taskCard(sel.Task),
	}
	for _, f := range taskFiles(sel.Task.Body) {
		b.Files = append(b.Files, readBriefFile(worktree, f))
	}
	return b
}

// render turns the brief into the injected preamble text. Empty fields degrade
// gracefully so a bare fixture task (no Files, unresolved pointer) still renders.
func (b workBrief) render() string {
	var s strings.Builder
	s.WriteString("## Task brief (runner-precomputed — start here; do NOT re-derive these)\n")
	s.WriteString("The runner already resolved the mechanics of THIS pass. Use these values directly; " +
		"you need no discovery git plumbing and no tree walk to obtain them.\n")
	fmt.Fprintf(&s, "- Code worktree (edit the submodule's code HERE): %s\n", b.Worktree)
	fmt.Fprintf(&s, "- Branch: %s\n", b.Branch)
	if b.Pointer != "" {
		fmt.Fprintf(&s, "- Submodule pointer / worktree base commit: %s\n", b.Pointer)
	}
	if b.TrackedTip != "" {
		fmt.Fprintf(&s, "- Tracked-branch tip (submodule origin): %s\n", b.TrackedTip)
	} else {
		s.WriteString("- Tracked-branch tip (submodule origin): none (no submodule remote; the recorded pointer stands)\n")
	}
	fmt.Fprintf(&s, "- Change doc — write it EXACTLY at: %s\n", b.DocPath)
	fmt.Fprintf(&s, "- Commit stamp — put this line VERBATIM in your submodule commit message: %s\n", b.Stamp)
	s.WriteString("\n### Your task card (verbatim from PLAN.md — no need to open PLAN.md to read it)\n")
	s.WriteString(b.Card)
	s.WriteString("\n")
	if len(b.Files) > 0 {
		s.WriteString("\n### Task files (your scoped working set — read THESE, not the whole tree)\n")
		for _, f := range b.Files {
			header := f.Path
			if f.Note != "" {
				header += " " + f.Note
			}
			fmt.Fprintf(&s, "\n--- %s ---\n", header)
			if f.Excerpt != "" {
				s.WriteString(f.Excerpt)
				if !strings.HasSuffix(f.Excerpt, "\n") {
					s.WriteString("\n")
				}
			}
		}
	}
	s.WriteString("\n")
	return s.String()
}

// taskCard renders the task's PLAN.md item (a compact header + its verbatim body)
// from the parsed Task, so the agent reads its card from the brief instead of
// reopening PLAN.md. Derived from Task fields (not plan.String internals) so it
// stays valid if the plan serializer changes.
func taskCard(t plan.Task) string {
	var s strings.Builder
	meta := "attempts=" + strconv.Itoa(t.Attempts)
	if len(t.Deps) > 0 {
		meta += " deps=" + strings.Join(t.Deps, ",")
	}
	if t.Weight > 1 {
		meta += " weight=" + strconv.Itoa(t.Weight)
	}
	fmt.Fprintf(&s, "## %s [%s] <!-- %s -->", t.ID, t.Status, meta)
	for _, ln := range t.Body {
		s.WriteString("\n")
		s.WriteString(ln)
	}
	return s.String()
}

// taskFiles extracts the file/dir paths named on the card's `Files:` line. That
// line is free-form prose ("Files: internal/swarm (note), swarm_test.go.") so we
// drop parenthetical notes, split on commas, and reduce each entry to its leading
// path token with trailing punctuation stripped. Non-path prose (e.g.
// "commit-path guard.") simply won't resolve to a real file and is dropped when
// its excerpt is read. Order preserved; duplicates removed.
func taskFiles(body []string) []string {
	for _, ln := range body {
		rest, ok := strings.CutPrefix(strings.TrimSpace(ln), "Files:")
		if !ok {
			continue
		}
		rest = briefParenRe.ReplaceAllString(rest, " ")
		seen := map[string]bool{}
		var out []string
		for _, part := range strings.Split(rest, ",") {
			fields := strings.Fields(part)
			if len(fields) == 0 {
				continue
			}
			p := strings.Trim(fields[0], ".,;:")
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
		return out
	}
	return nil
}

// readBriefFile resolves rel against the worktree and returns a bounded excerpt
// (file) or listing (directory). It never escapes the worktree and never fails:
// a missing/unreadable path is annotated so the agent knows it is new or must be
// opened directly.
func readBriefFile(worktree, rel string) briefFile {
	bf := briefFile{Path: rel}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		bf.Note = "(skipped — outside the worktree)"
		return bf
	}
	abs := filepath.Join(worktree, clean)
	info, err := os.Stat(abs)
	if err != nil {
		bf.Note = "(not present in the worktree — a new file the task creates, or a beehive-layer path)"
		return bf
	}
	if info.IsDir() {
		bf.Note = "(directory)"
		bf.Excerpt = dirListing(abs)
		return bf
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		bf.Note = "(unreadable: " + err.Error() + ")"
		return bf
	}
	excerpt, truncated := headExcerpt(string(data), briefFileMaxLines, briefFileMaxBytes)
	bf.Excerpt = excerpt
	if truncated {
		bf.Note = fmt.Sprintf("(first %d lines / %d bytes — open the file for the rest)", briefFileMaxLines, briefFileMaxBytes)
	}
	return bf
}

// dirListing returns a sorted, bounded listing of dir (names only, trailing "/"
// on subdirs) so a directory named in Files: orients the agent without a walk.
func dirListing(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > briefDirMaxEntries {
		names = append(names[:briefDirMaxEntries:briefDirMaxEntries], fmt.Sprintf("… (%d more)", len(names)-briefDirMaxEntries))
	}
	return strings.Join(names, "\n")
}

// headExcerpt returns the leading maxLines lines of s capped at maxBytes,
// reporting whether it truncated. Line boundaries are preserved.
func headExcerpt(s string, maxLines, maxBytes int) (string, bool) {
	truncated := false
	if len(s) > maxBytes {
		s = s[:maxBytes]
		truncated = true
	}
	lines := strings.SplitAfter(s, "\n")
	// SplitAfter yields a trailing "" element when s ends in "\n"; drop it so it
	// is not counted as a line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	return strings.Join(lines, ""), truncated
}
