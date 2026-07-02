package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// briefMaxFiles / briefMaxLines bound the precomputed file excerpts so the task
// brief stays COMPACT — its whole point is to shrink the working set, not to
// re-inject the tree. Excerpts stop after briefMaxFiles files; a file longer than
// briefMaxLines is head-truncated with a "more lines" marker.
const (
	briefMaxFiles = 12
	briefMaxLines = 80
)

// briefParenRe strips the parenthetical annotations on a task's `Files:` line
// before it is comma-split, so an annotation that carries a comma cannot break
// the split into path tokens.
var briefParenRe = regexp.MustCompile(`\([^)]*\)`)

// workBrief assembles the runner-PRECOMPUTED task brief for a Work pass. It hands
// the agent the mechanics the runner already resolved when it set the pass up —
// the code-worktree path, the branch, the submodule pointer + tracked tip, the
// task's own PLAN card, the deterministic protocol choices (change-doc path +
// commit stamp) as ANSWERS rather than formulae, and bounded excerpts of exactly
// the files the task's `Files:` line names — so the agent never re-runs discovery
// git plumbing or reads the whole submodule tree just to get its bearings.
//
// Every value is sourced from the SAME resolved worktree/branch/pointer the Work
// setup produced (wtAbs, branch, wg), never a second derivation that could drift.
// It is best-effort and additive: a sub-lookup that fails is omitted, never
// fatal, because the brief sets the floor (no rediscovery), not a cage — the
// agent may still read more if a task needs it. Gated by Runner.TaskBrief (off by
// default) so the default injected set stays byte-identical to the historical path.
func (r *Runner) workBrief(ctx context.Context, sel *selectt.Selection, branch, wtAbs, absRoot string, wg *git.Repo) string {
	sm := sel.Submodule.Name
	id := taskID(sel)
	wtRel := filepath.Join("submodules", sm, "worktrees", branch)
	docPath := fmt.Sprintf("submodules/%s/docs/%s-%s.md", sm, branch, id)
	stamp := fmt.Sprintf("Beehive: %s %s", id, docPath)

	pointer, tip, tipRef := r.briefPointers(ctx, sel, absRoot, wg)

	var b strings.Builder
	b.WriteString("# Task brief (precomputed by the runner — these values are RESOLVED for you; do not rediscover them)\n")
	b.WriteString(fmt.Sprintf("Code worktree (edit the CODE here): %s\n", wtAbs))
	b.WriteString(fmt.Sprintf("  (repo-relative: %s/)\n", wtRel))
	b.WriteString(fmt.Sprintf("Branch: %s\n", branch))
	if pointer != "" {
		b.WriteString(fmt.Sprintf("Submodule pointer (the commit your worktree branched from): %s\n", pointer))
	}
	if tip != "" {
		b.WriteString(fmt.Sprintf("Tracked tip (%s): %s\n", tipRef, tip))
	}
	b.WriteString(fmt.Sprintf("Change doc — write it EXACTLY here (the completion check requires this path): %s\n", docPath))
	b.WriteString(fmt.Sprintf("Commit stamp — put this line VERBATIM in the code commit message: %s\n", stamp))

	b.WriteString("\nYour PLAN card:\n")
	b.WriteString(briefCard(sel))

	files := filesFromCard(sel.Task.Body)
	if len(files) > 0 {
		b.WriteString("\nYour files (working set — read these, not the whole submodule tree):\n")
		for _, f := range files {
			b.WriteString("  - " + f + "\n")
		}
		if ex := briefExcerpts(wtAbs, files); ex != "" {
			b.WriteString("\nFile excerpts (from your worktree — the files above):\n")
			b.WriteString(ex)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// briefPointers resolves the submodule pointer (the commit the worktree branched
// from — the checkout HEAD) and the tracked tip (origin/<branch>) for the brief,
// reusing the SAME submodule checkout (wg) and tracked-branch resolution the Work
// setup's syncWorktreeBase uses. A no-remote checkout reports the tip as the
// pointer itself (there is nothing else to track). Best-effort: any git failure
// yields an empty field so workBrief simply omits that line.
func (r *Runner) briefPointers(ctx context.Context, sel *selectt.Selection, absRoot string, wg *git.Repo) (pointer, tip, tipRef string) {
	if wg == nil {
		return "", "", ""
	}
	pointer, err := wg.RevParse(ctx, "HEAD")
	if err != nil {
		return "", "", ""
	}
	rem, err := wg.Remote(ctx)
	if err != nil || rem == "" {
		return pointer, pointer, "no remote — tracked tip == pointer"
	}
	rel, err := filepath.Rel(absRoot, sel.Submodule.RepoDir())
	if err != nil {
		return pointer, "", ""
	}
	tipRef = rem + "/" + r.trackedBranch(ctx, rel)
	tip, err = wg.RevParse(ctx, tipRef)
	if err != nil {
		return pointer, "", ""
	}
	return pointer, tip, tipRef
}

// briefCard renders the task's own PLAN entry from the selection: the id + status
// header carrying the STABLE deps/weight metadata (the volatile session/heartbeat
// claim that re-stamps every turn is deliberately omitted) and the verbatim body.
// The agent gets its card without re-reading PLAN.md.
func briefCard(sel *selectt.Selection) string {
	t := sel.Task
	meta := "deps=" + strings.Join(t.Deps, ",")
	if t.Weight > 1 {
		meta += fmt.Sprintf(" weight=%d", t.Weight)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## %s [%s] <!-- %s -->\n", t.ID, t.Status, meta))
	for _, ln := range t.Body {
		b.WriteString(ln + "\n")
	}
	return b.String()
}

// filesFromCard extracts the concrete path tokens a task declares on its `Files:`
// body line (its working set). Parenthetical annotations are stripped first (they
// may contain commas), the remainder is comma-split, and glob/prose tokens
// (containing '*' or whitespace) are dropped — leaving the concrete file/dir paths
// the runner can resolve in the worktree. Returns nil when there is no Files line.
func filesFromCard(body []string) []string {
	line := ""
	for _, ln := range body {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(ln), "Files:"); ok {
			line = rest
			break
		}
	}
	if strings.TrimSpace(line) == "" {
		return nil
	}
	line = briefParenRe.ReplaceAllString(line, "")
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.Split(line, ",") {
		tok = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(tok), "."))
		if tok == "" || strings.ContainsAny(tok, "* \t") {
			continue // globs (*_test.go) and leftover prose are not concrete paths
		}
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// briefExcerpts returns bounded head excerpts of the task's files, read from the
// agent's own worktree (base). Only tokens that resolve to a regular file inside
// the worktree are excerpted; directories, absent tokens, and any path that would
// escape the worktree are skipped (they are already named in the working-set
// list). Because it reads ONLY the task's declared tokens, a file outside the
// task's Files line is never pulled into the brief. Bounded by briefMaxFiles /
// briefMaxLines to keep the brief compact.
func briefExcerpts(base string, files []string) string {
	var b strings.Builder
	n := 0
	for _, f := range files {
		if n >= briefMaxFiles {
			break
		}
		clean := filepath.Clean(f)
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			continue // never escape the worktree
		}
		full := filepath.Join(base, clean)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		n++
		b.WriteString(briefOneExcerpt(f, string(data)))
	}
	return b.String()
}

// briefOneExcerpt formats one file's head excerpt with a bounded line count.
func briefOneExcerpt(name, content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // drop the trailing-newline empty element
	}
	total := len(lines)
	shown := lines
	truncated := false
	if total > briefMaxLines {
		shown = lines[:briefMaxLines]
		truncated = true
	}
	var b strings.Builder
	if truncated {
		b.WriteString(fmt.Sprintf("----- %s (first %d of %d lines) -----\n", name, len(shown), total))
	} else {
		b.WriteString(fmt.Sprintf("----- %s (%d lines) -----\n", name, total))
	}
	for _, ln := range shown {
		b.WriteString(ln + "\n")
	}
	if truncated {
		b.WriteString(fmt.Sprintf("----- ...%d more lines in %s (read the file for the rest) -----\n", total-len(shown), name))
	}
	return b.String()
}
