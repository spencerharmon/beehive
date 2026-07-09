package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
)

// delivery-traceability: link every DONE task to BOTH (a) the hive superproject
// commit that flipped its PLAN.md status to DONE, and (b) the submodule commit
// carrying its code — so "why is this DONE" is one click across the two repos
// instead of a cross-repo hunt (surfaced on /stats and the branches view).
// Half (b) reuses branch-graph-sectioned's stamp machinery verbatim
// (changeDocsByTask + resolveDocHref, branches.go); this file adds half (a),
// the hive-side locator, plus DeliveryLink, the per-task record zipping both
// halves together, and the commitView page a flip link resolves to.

// DeliveryLink is one DONE task's traceability record. Every field degrades to
// "" when it can't be located (never an error, never a dead link): a task
// whose flip commit or code stamp isn't found simply renders with that half
// blank instead of failing the page.
type DeliveryLink struct {
	TaskID   string
	FlipSHA  string // short hive commit sha that first shows this task DONE, "" if unlocated
	FlipHref string // link to VIEW that hive commit (commitView), "" if unlocated
	DocPath  string // change-doc path from the submodule's Beehive stamp, "" if none
	DocHref  string // link to view the change doc (branch-graph-sectioned), "" if it doesn't resolve
}

// flipHeaderRe matches an ADDED PLAN.md task-header line (a unified-diff "+"
// line) whose status is DONE. DONE is a terminal status — internal/plan's
// state machine has no transition out of it, and ArchiveDone only ever trims a
// DONE task's body narrative, never its header — so a task's
// "## <id> ... [DONE]" line is introduced by a "+" exactly once, ever, in a
// PLAN.md's history. The FIRST match for a task id, scanning oldest-first, is
// therefore ITS flip commit, unambiguously.
var flipHeaderRe = regexp.MustCompile(`^\+##\s+(\S+)\s+\[DONE\]`)

// planRelPath is the beehive-repo-relative path of sm's PLAN.md
// ("submodules/<name>/PLAN.md") — the pathspec every hive-history git call
// below scopes to. Mirrors internal/claim's Claimer.planRel(); repeated here
// (rather than imported — it is unexported) since every caller here already
// holds a git.Repo rooted at the hive root and just needs the relative path.
func planRelPath(sm repo.Submodule) string {
	return filepath.Join("submodules", sm.Name, repo.PlanFile)
}

// hiveDoneFlips scans the HIVE superproject's OWN history (g rooted at the
// beehive repo root — NEVER a submodule checkout) for the commit that FIRST
// introduced each wanted task id's "[DONE]" PLAN.md header line: the commit
// that flipped that task's status to DONE (delivery-traceability's half (a);
// half (b) is changeDocsByTask/resolveDocHref below, unchanged). planRel is
// the beehive-repo-relative PLAN.md path (see planRelPath).
//
// One process spawn regardless of how many ids are wanted or how long the
// history is: -G'\[DONE\]' is a pickaxe pre-filter, so git itself skips
// emitting (or even walking the full patch of) the many claim/heartbeat
// commits that never touch a DONE marker — only commits that actually add or
// remove one are considered, which for a real PLAN.md (heartbeats every turn,
// a DONE flip once per task) is a small fraction of the file's total history.
//
// Best-effort throughout, matching every other file-derived view in this
// package: a git error, or a task whose flip can't be found (already DONE at
// the earliest reachable commit, an empty/absent history, ...), is simply
// absent from the result — never an error.
func hiveDoneFlips(ctx context.Context, g *git.Repo, planRel string, want map[string]bool) map[string]string {
	out := map[string]string{}
	if len(want) == 0 {
		return out
	}
	// %x1e delimits records (a sha marker can never collide with diff text);
	// --reverse walks oldest-first so the FIRST matching "+" line per task id
	// is its flip.
	log, err := g.Run(ctx, "log", "--reverse", "-p", `-G\[DONE\]`, "--format=%x1e%H", "--", planRel)
	if err != nil {
		return out
	}
	for _, rec := range strings.Split(log, "\x1e") {
		nl := strings.IndexByte(rec, '\n')
		if nl < 0 {
			continue
		}
		sha := rec[:nl]
		for _, line := range strings.Split(rec[nl+1:], "\n") {
			m := flipHeaderRe.FindStringSubmatch(line)
			if m == nil || !want[m[1]] {
				continue
			}
			if _, seen := out[m[1]]; !seen {
				out[m[1]] = sha[:min(12, len(sha))]
			}
		}
	}
	return out
}

// hiveCommitHref links to sm's hive PLAN-flip commit view (commitView below),
// "" when sha is empty — callers only ever pass a sha hiveDoneFlips actually
// found, but empty-safety keeps this composable without a redundant guard at
// every call site.
func hiveCommitHref(smName, sha string) string {
	if sha == "" {
		return ""
	}
	return "/submodule/" + smName + "/commit/" + sha
}

// doneTaskIDs reads sm's PLAN.md and returns its DONE task ids in file order.
// Best-effort: a missing or unparsable plan yields nil, never an error — the
// same convention computeStats and every other PLAN.md reader in this package
// already follows.
func doneTaskIDs(sm repo.Submodule) []string {
	b, err := os.ReadFile(sm.PlanPath())
	if err != nil {
		return nil
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return nil
	}
	var ids []string
	for _, t := range p.Tasks {
		if t.Status == plan.StatusDone {
			ids = append(ids, t.ID)
		}
	}
	return ids
}

// buildDeliveries returns one DeliveryLink per id (order preserved), zipping
// half (a) (hiveDoneFlips, over the BEEHIVE repo's own PLAN.md history) with
// half (b) (changeDocsByTask/resolveDocHref, over sm's own code history —
// branch-graph-sectioned's unchanged machinery). head is Server.headSHA,
// memoizing both history scans per HEAD generation via viewCache (each is a
// history git-log walk, too expensive to redo every request); an empty head
// (no commits yet) bypasses the cache and loads fresh.
func (s *Server) buildDeliveries(ctx context.Context, head string, sm repo.Submodule, ids []string) []DeliveryLink {
	if len(ids) == 0 {
		return nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	planRel := planRelPath(sm)
	flips, _ := cachedView(head, s.cache, "delivery-flips:"+sm.Name, func() (map[string]string, error) {
		return hiveDoneFlips(ctx, s.git, planRel, want), nil
	})
	docs, _ := cachedView(head, s.cache, "delivery-docs:"+sm.Name, func() (map[string]string, error) {
		return changeDocsByTask(ctx, sm.RepoDir()), nil
	})
	out := make([]DeliveryLink, 0, len(ids))
	for _, id := range ids {
		sha := flips[id]
		doc := docs[id]
		out = append(out, DeliveryLink{
			TaskID:   id,
			FlipSHA:  sha,
			FlipHref: hiveCommitHref(sm.Name, sha),
			DocPath:  doc,
			DocHref:  resolveDocHref(sm, doc),
		})
	}
	return out
}

// indexDeliveries re-keys a DeliveryLink slice by task id, so the branches view
// can annotate each commit ROW (keyed by its Beehive-stamp DocTask) in O(1)
// without recomputing buildDeliveries per row.
func indexDeliveries(ds []DeliveryLink) map[string]DeliveryLink {
	m := make(map[string]DeliveryLink, len(ds))
	for _, d := range ds {
		m[d.TaskID] = d
	}
	return m
}

// safeSHA guards the {sha} path param: a commit id is lowercase hex, which
// also rules out option-injection (no leading "-") before it ever reaches a
// git subprocess argv. commitView additionally rev-parses it, so a
// well-formed but unresolvable sha still 404s rather than ever being trusted.
func safeSHA(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// commitView renders one hive superproject commit's PLAN.md diff, scoped to a
// single submodule — the destination a delivery-traceability FlipHref points
// at ("why is this DONE"). Read-only: it only ever runs git show/rev-parse
// against the beehive repo's OWN history, never a mutation. sha is validated
// as hex and rev-parsed before use, so an invalid or unresolvable sha 404s
// instead of ever reaching a shell-unsafe or non-existent ref.
//
// The diff renders through the shared editor.RenderDiff/RenderDiffHTML row
// renderer (diff-view-colorize-consistency) — the same per-line add/del
// coloring (and, where chroma has a lexer for the path, syntax highlighting)
// as the editor/chat/skill diff panels, instead of a raw unified patch.
func (s *Server) commitView(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sha := r.PathValue("sha")
	if !safeSHA(sha) {
		http.NotFound(w, r)
		return
	}
	full, err := s.git.RevParse(r.Context(), sha)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	meta, err := s.git.Run(r.Context(), "show", "-s", "--date=short", "--format=%an%x1f%ad%x1f%s", full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f := strings.SplitN(meta, "\x1f", 3)
	for len(f) < 3 {
		f = append(f, "")
	}
	// Scoped to this submodule's PLAN.md only — never a whole-commit diff —
	// so the page can never leak an unrelated file from elsewhere in the hive.
	// before/after each ignore their own error and fall back to "": a root
	// commit (no "^" parent to resolve) or a commit that ADDED the file both
	// leave before "" (pure add); a commit that DELETED the file leaves after
	// "" (pure delete) — the same root/add/delete edge RenderDiffHTML already
	// tolerates for every other caller (chatedit/skills), never a 404.
	planPath := planRelPath(sm)
	before, _ := s.git.Show(r.Context(), full+"^", planPath)
	after, _ := s.git.Show(r.Context(), full, planPath)
	lexer := lexerFor(planPath)
	rows := editor.RenderDiffHTML(before, after, highlightLines(before, lexer), highlightLines(after, lexer))
	shortSHA := full[:min(12, len(full))]
	s.render(w, "commit_view.html", map[string]interface{}{
		"Name":    sm.Name,
		"SHA":     shortSHA,
		"Author":  f[0],
		"Date":    f[1],
		"Subject": f[2],
		"Rows":    rows,
		"Title":   pageTitle("commit", shortSHA, sm.Name),
		"Crumbs":  commitCrumbs(sm.Name, shortSHA),
	})
}
