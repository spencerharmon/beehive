package swarm

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// Precomputed task brief for the "cut tokens per honeybee" lever (ROI Priority 1
// — "Precompute in the runner (take work off the agent): resolve the worktree,
// branch, and pointers; hand the agent the file excerpts it needs; make the
// deterministic choices for it").
//
// Every pass the naive honeybee re-derives what the runner ALREADY resolved when
// it set the pass up: it runs git plumbing to find its worktree/branch, reads the
// submodule pointer, explores the tree to orient, and recomputes the protocol's
// deterministic choices (the change-doc path, the commit stamp). Those are turns
// of pure setup — "time on unrelated tasks" — spent rediscovering values the
// runner computed. The brief hands the agent those answers up front and scopes
// its reading to the task's own files instead of the whole submodule tree.
//
// It is ADDITIVE: the brief sets the floor (no rediscovery), not a cage — a task
// may still read more if it needs to. It ships INERT: the runner assembles and
// injects it only under LeanBrief (BEEHIVE_LEAN_BRIEF=1), so the default injected
// preamble stays byte-identical to the historical path. The brief reuses the SAME
// resolved worktree/branch/pointer the Work setup computed (it reads the live
// checkout state via the passed-in git.Repo), so it never adds a second code path
// that could drift from what the pass actually got.

// briefExcerptLines bounds how many leading lines of each task file are surfaced.
// The excerpt orients the agent (package decl, imports, the top of the file it
// will edit) without re-sending the whole file — it is a floor, not the file; the
// agent reads on from the given path when it needs more.
const briefExcerptLines = 40

// briefFileCap bounds how many task files are excerpted, so a card naming many
// files cannot blow the very budget the brief exists to cut.
const briefFileCap = 12

// briefFile is one file from the task card's Files: line, resolved against the
// code worktree. Exactly one of Excerpt / Note is set: Excerpt is a bounded head
// slice of an existing regular file; Note explains a path we cannot excerpt (a
// package directory, a not-yet-created file, an unreadable path).
type briefFile struct {
	Path    string
	Excerpt string
	Note    string
}

// taskBrief carries the runner-precomputed values injected for a Work pass. Every
// field is resolved from what the Work setup already computed (worktree, branch,
// checkout pointers) or deterministically derived by the protocol (doc path,
// commit stamp) — never left for the agent to rediscover.
type taskBrief struct {
	Submodule     string
	Branch        string
	WorktreeAbs   string
	WorktreeRel   string
	BaseSHA       string // submodule pointer the code worktree branched from
	TrackedBranch string
	TrackedTip    string // origin/<TrackedBranch> tip (== BaseSHA post-sync; "" single-host)
	DocPath       string // REQUIRED change-doc path (beehive layer)
	CommitStamp   string // Beehive: <taskid> <doc-path>
	Card          string // the task's own PLAN card (header + body)
	Files         []briefFile
}

// buildTaskBrief assembles the brief from the runner's already-resolved values.
// wg is the submodule checkout (for the pointer/tracked tip), wtAbs the resolved
// code worktree, branch the worktree branch, absRoot the beehive repo root. It is
// only called for a Work selection (sel.Task is valid). It reads the LIVE checkout
// state rather than re-deriving it, so the brief reflects exactly what the pass
// got.
func (r *Runner) buildTaskBrief(ctx context.Context, sel *selectt.Selection, wg *git.Repo, wtAbs, absRoot, branch string) taskBrief {
	sm := sel.Submodule
	// The change-doc path and commit stamp are the protocol's deterministic
	// choices; construct them the SAME way the preamble/completion check do so the
	// agent is told the answer, not the formula. filepath.ToSlash keeps the path
	// stable across OSes (the layer is slash-pathed).
	docPath := path.Join("submodules", sm.Name, "docs", branch+"-"+sel.Task.ID+".md")
	b := taskBrief{
		Submodule:   sm.Name,
		Branch:      branch,
		WorktreeAbs: wtAbs,
		DocPath:     docPath,
		CommitStamp: fmt.Sprintf("Beehive: %s %s", sel.Task.ID, docPath),
		Card:        sel.Task.Card(),
	}
	if wtRel, err := filepath.Rel(absRoot, wtAbs); err == nil {
		b.WorktreeRel = filepath.ToSlash(wtRel)
	} else {
		b.WorktreeRel = filepath.ToSlash(wtAbs)
	}

	// Pointer + tracked tip: read from the resolved checkout the Work setup synced,
	// not a second derivation. HEAD is the base the worktree branched from (== the
	// bumped pointer after syncWorktreeBase); origin/<branch> is the tracked tip
	// (absent on a single-host/no-remote checkout).
	if wg != nil {
		if head, err := wg.RevParse(ctx, "HEAD"); err == nil {
			b.BaseSHA = head
		}
		b.TrackedBranch = "main"
		if rel, err := filepath.Rel(absRoot, sm.RepoDir()); err == nil {
			b.TrackedBranch = r.trackedBranch(ctx, rel)
		}
		if tip, err := wg.RevParse(ctx, "origin/"+b.TrackedBranch); err == nil {
			b.TrackedTip = tip
		}
	}

	// Task files: scoped to the card's Files: line, read from the code worktree —
	// the agent's working set, not the whole tree. A path outside the Files: line
	// is never pulled in.
	for _, f := range filesFromCard(sel.Task.Body) {
		if len(b.Files) >= briefFileCap {
			break
		}
		bf := briefFile{Path: f}
		abs := filepath.Join(wtAbs, filepath.FromSlash(f))
		switch info, err := os.Stat(abs); {
		case err != nil:
			bf.Note = "not present in the worktree (a new file to create, or a package path)"
		case info.IsDir():
			bf.Note = "package directory (part of your working set — read the specific file you touch, not the whole tree)"
		default:
			if ex, ok := fileExcerpt(abs, briefExcerptLines); ok {
				bf.Excerpt = ex
			} else {
				bf.Note = "present but unreadable"
			}
		}
		b.Files = append(b.Files, bf)
	}
	return b
}

// render produces the injected brief text. Deterministic given the struct.
func (b taskBrief) render() string {
	var sb strings.Builder
	sb.WriteString("# Task brief (precomputed by the runner — do NOT re-derive these; read your task's files, not the whole tree)\n")
	sb.WriteString("The runner already resolved your worktree, branch, pointers, and the protocol's deterministic\n")
	sb.WriteString("choices below. Use these values directly — you do not need git plumbing or a tree scan to orient.\n")
	sb.WriteString("(This is a floor, not a cage: read further if the task genuinely needs it.)\n\n")

	fmt.Fprintf(&sb, "- Submodule: %s\n", b.Submodule)
	fmt.Fprintf(&sb, "- Branch: %s\n", b.Branch)
	fmt.Fprintf(&sb, "- Code worktree (edit CODE here): %s\n", b.WorktreeRel)
	fmt.Fprintf(&sb, "- Code worktree (absolute): %s\n", b.WorktreeAbs)
	if b.BaseSHA != "" {
		fmt.Fprintf(&sb, "- Submodule pointer (the worktree branched from this commit): %s\n", b.BaseSHA)
	}
	if b.TrackedTip != "" {
		fmt.Fprintf(&sb, "- Tracked tip (origin/%s): %s\n", b.TrackedBranch, b.TrackedTip)
	} else if b.TrackedBranch != "" {
		fmt.Fprintf(&sb, "- Tracked branch: %s (single-host: no separate origin tip)\n", b.TrackedBranch)
	}
	fmt.Fprintf(&sb, "- REQUIRED change-doc path (write it EXACTLY here): %s\n", b.DocPath)
	fmt.Fprintf(&sb, "- Commit stamp (put this line on your submodule commit): %s\n", b.CommitStamp)

	sb.WriteString("\n## Your PLAN card\n")
	sb.WriteString(b.Card)
	if !strings.HasSuffix(b.Card, "\n") {
		sb.WriteByte('\n')
	}

	if len(b.Files) > 0 {
		sb.WriteString("\n## Task files (from your card's `Files:` line — your working set; read these, not the tree)\n")
		for _, f := range b.Files {
			if f.Excerpt != "" {
				fmt.Fprintf(&sb, "\n### %s (first %d lines)\n", f.Path, briefExcerptLines)
				sb.WriteString(f.Excerpt)
				if !strings.HasSuffix(f.Excerpt, "\n") {
					sb.WriteByte('\n')
				}
			} else {
				fmt.Fprintf(&sb, "\n### %s — %s\n", f.Path, f.Note)
			}
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// filesFromCard extracts candidate repo-relative paths from a task card's Files:
// body line. That line is free-form prose — parenthetical asides, `,`/`+`
// separators, a trailing period, leading descriptor words ("new
// internal/artifacts/*"), and non-path phrases ("shared helper pkg") all appear —
// so parsing is deliberately conservative: strip parenthetical groups, split on
// `,` and `+`, then from each piece take the FIRST whitespace token that LOOKS
// like a path (it has a slash or a file extension), trimming trailing sentence
// punctuation. Order-preserving and de-duplicated. Returns nil when the card has
// no Files: line.
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
	line = stripParens(line)
	var out []string
	seen := map[string]bool{}
	pieces := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == '+' })
	for _, piece := range pieces {
		for _, tok := range strings.Fields(piece) {
			// Trim trailing sentence punctuation but NOT a leading dot (hidden files
			// like .gitmodules must survive).
			tok = strings.TrimRight(tok, ".,;:")
			if tok == "" {
				continue
			}
			if strings.Contains(tok, "/") || path.Ext(tok) != "" {
				if !seen[tok] {
					seen[tok] = true
					out = append(out, tok)
				}
				break // the first path-like token is this piece's file
			}
		}
	}
	return out
}

// stripParens removes every parenthetical group from s (nesting-aware), so a
// Files: line's inline annotations do not leak into the parsed path tokens.
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

// fileExcerpt returns the first maxLines lines of the file at abs, with a marker
// when the file is longer. The bool is false when the file cannot be read.
func fileExcerpt(abs string, maxLines int) (string, bool) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > maxLines {
		head := strings.Join(lines[:maxLines], "\n")
		return head + "\n\u2026 [truncated; open " + filepath.Base(abs) + " for the rest] \u2026\n", true
	}
	return string(data), true
}
