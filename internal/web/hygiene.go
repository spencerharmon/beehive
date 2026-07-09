package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
)

// hygieneSkill is the cleanup skill the panel points the operator at as the
// explicit remediation action. The panel only SURFACES cruft; running this skill
// (the routine sweep in skills/cleanup.md) is how the operator clears it. The
// panel never invokes it — diagnostic only.
const hygieneSkill = "beehive-hygiene"

// CruftItem is one flagged piece of git cruft within a class: its identifier
// (a worktree dir name, a gitlink path, a remote name) plus a short read-only
// reason it was flagged. Purely descriptive — nothing here is ever mutated.
type CruftItem struct {
	Name   string // the thing: worktree basename, gitlink/submodule path, or remote name
	Detail string // why it was flagged, for the drill-down (no action implied)
}

// CruftClass is one hygiene category with its drill-down list.
type CruftClass struct {
	Key   string // stable slug: "worktrees" | "gitlinks" | "checkouts" | "remotes"
	Title string // human label for the widget
	Items []CruftItem
}

// Count is the class's flagged-item count (the badge number).
func (c CruftClass) Count() int { return len(c.Items) }

// Hygiene is the whole read-only scan result: the four cruft classes plus the
// skill to remediate them, and an optional scan error (surfaced, never swallowed)
// so the dashboard widget can degrade without failing the whole page.
type Hygiene struct {
	Classes []CruftClass
	Skill   string
	Err     string
}

// Total is the sum of every class's flagged items.
func (h Hygiene) Total() int {
	n := 0
	for _, c := range h.Classes {
		n += len(c.Items)
	}
	return n
}

// Clean reports whether the scan found no cruft at all (and did not error).
func (h Hygiene) Clean() bool { return h.Err == "" && h.Total() == 0 }

// CacheStats is the /hygiene page's view-cache widget data: a live,
// process-lifetime snapshot of the frontend's parse-once cache (see
// viewCache in cache.go) — lookups, hits, and misses — plus the derived
// hit-rate a bare miss count can't express on its own. It is a process gauge,
// not a stored metric: it resets to zero on every restart, same as the cache
// counters it reads, and the widget labels it as such rather than implying
// history. Additive and read-only — building it never touches the cache, only
// reads its counters.
type CacheStats struct {
	Lookups int
	Hits    int
	Misses  int
}

// HitRatePct is the derived hit-rate as a 0-100 percentage. It reports 0 before
// any lookup has happened rather than dividing 0/0, so an idle-since-restart
// process shows "0%" instead of a misleading NaN or 100%.
func (c CacheStats) HitRatePct() float64 {
	if c.Lookups == 0 {
		return 0
	}
	return 100 * float64(c.Hits) / float64(c.Lookups)
}

// cacheStats snapshots cache's Lookups/Hits/Misses counters into the page-data
// shape the hygiene template renders. Read-only: it only calls the cache's
// existing read accessors, never mutates it.
func cacheStats(cache *viewCache) CacheStats {
	return CacheStats{Lookups: cache.Lookups(), Hits: cache.Hits(), Misses: cache.Misses()}
}

// scanHygiene performs a READ-ONLY sweep of the beehive repo for the git cruft
// that accumulates under updateInstead, returning a per-class drill-down. It
// MUTATES NOTHING: every git invocation is a query (worktree list, ls-files,
// config --get, remote, rev-parse) — never a write, prune, remove, or config
// change. root is the beehive repo root; g is a git.Repo rooted there.
func scanHygiene(ctx context.Context, root string, g *git.Repo) (Hygiene, error) {
	worktrees, err := staleWorktrees(ctx, root, g)
	if err != nil {
		return Hygiene{}, err
	}
	declared, err := declaredGitlinkPaths(ctx, g)
	if err != nil {
		return Hygiene{}, err
	}
	gitlinks, err := trackedGitlinks(ctx, g)
	if err != nil {
		return Hygiene{}, err
	}
	orphans := orphanGitlinks(gitlinks, declared)
	checkouts, err := staleCheckouts(ctx, root, g, gitlinks, declared)
	if err != nil {
		return Hygiene{}, err
	}
	remotes, err := unexpectedRemotes(ctx, g)
	if err != nil {
		return Hygiene{}, err
	}
	return Hygiene{
		Skill: hygieneSkill,
		Classes: []CruftClass{
			{Key: "worktrees", Title: "Stale worktrees", Items: worktrees},
			{Key: "gitlinks", Title: "Orphan submodule gitlinks", Items: orphans},
			{Key: "checkouts", Title: "Stale submodule checkouts", Items: checkouts},
			{Key: "remotes", Title: "Unexpected remotes", Items: remotes},
		},
	}, nil
}

// staleWorktrees flags directories under <root>/.worktrees that look like editor
// or pass worktrees (edit-* / beehive-*) but are NOT registered with git (no live
// `git worktree` entry) — the abandoned trees dead editor sessions and capped
// passes leave behind. Registration is compared by basename, which equals the
// branch/dir name and is immune to the symlinked vs canonical path differences
// `git worktree list` can report.
func staleWorktrees(ctx context.Context, root string, g *git.Repo) ([]CruftItem, error) {
	dir := filepath.Join(root, ".worktrees")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	wts, err := g.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, w := range wts {
		registered[filepath.Base(w.Path)] = true
	}
	var out []CruftItem
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "edit-") && !strings.HasPrefix(name, "beehive-") {
			continue
		}
		if registered[name] {
			continue
		}
		out = append(out, CruftItem{Name: name, Detail: "unregistered worktree dir (no live git worktree)"})
	}
	return out, nil
}

// declaredGitlinkPaths returns the set of submodule paths declared in .gitmodules
// (empty when there is no .gitmodules or no entries). A tracked gitlink at a path
// outside this set is an orphan; a gitlink inside it is a real submodule whose
// checkout can drift.
func declaredGitlinkPaths(ctx context.Context, g *git.Repo) (map[string]bool, error) {
	declared := map[string]bool{}
	if _, err := os.Stat(filepath.Join(g.Dir, ".gitmodules")); err != nil {
		return declared, nil
	}
	out, err := g.Run(ctx, "config", "-f", ".gitmodules", "--get-regexp", `\.path$`)
	if err != nil {
		// --get-regexp exits non-zero when nothing matches; that is "none", not a
		// failure for a read-only scan.
		return declared, nil
	}
	for _, line := range strings.Split(out, "\n") {
		// "submodule.<name>.path <path>"
		if i := strings.LastIndex(line, " "); i >= 0 {
			if p := strings.TrimSpace(line[i+1:]); p != "" {
				declared[p] = true
			}
		}
	}
	return declared, nil
}

// gitlink is one tracked submodule index entry (mode 160000): the repo-relative
// path and the recorded commit SHA the gitlink points at.
type gitlink struct {
	Path string
	SHA  string
}

// trackedGitlinks lists every mode-160000 entry in the index (the recorded
// submodule gitlinks), parsed from `git ls-files -s`.
func trackedGitlinks(ctx context.Context, g *git.Repo) ([]gitlink, error) {
	out, err := g.Run(ctx, "ls-files", "-s")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var links []gitlink
	for _, line := range strings.Split(out, "\n") {
		// "<mode> <sha> <stage>\t<path>"; gitlinks are mode 160000.
		if !strings.HasPrefix(line, "160000 ") {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(line[:tab])
		if len(fields) < 2 {
			continue
		}
		links = append(links, gitlink{Path: line[tab+1:], SHA: fields[1]})
	}
	return links, nil
}

// orphanGitlinks flags tracked gitlinks whose path is not declared in .gitmodules
// — the committed honeybee worktrees (submodules/*/worktrees/*) with no submodule
// URL that wedge `git submodule update`.
func orphanGitlinks(links []gitlink, declared map[string]bool) []CruftItem {
	var out []CruftItem
	for _, l := range links {
		if declared[l.Path] {
			continue
		}
		out = append(out, CruftItem{Name: l.Path, Detail: "tracked gitlink with no .gitmodules entry"})
	}
	return out
}

// staleCheckouts flags a declared submodule whose working checkout HEAD has
// drifted off its recorded gitlink SHA. A submodule whose checkout is missing or
// has no HEAD is not flagged here: that is a different (un-materialized) condition
// the runner heals, and this scan only asserts a concrete divergence it can read.
func staleCheckouts(ctx context.Context, root string, g *git.Repo, links []gitlink, declared map[string]bool) ([]CruftItem, error) {
	var out []CruftItem
	for _, l := range links {
		if !declared[l.Path] {
			continue // orphan, surfaced as its own class
		}
		sub := git.New(filepath.Join(root, l.Path))
		head, err := sub.RevParse(ctx, "HEAD")
		if err != nil {
			continue // not checked out / no HEAD: nothing to assert
		}
		if head != l.SHA {
			out = append(out, CruftItem{
				Name:   l.Path,
				Detail: "checkout " + short(head) + " != recorded gitlink " + short(l.SHA),
			})
		}
	}
	return out, nil
}

// unexpectedRemotes flags every configured remote other than origin. git config
// is shared across all worktrees of a repo, so a stray `git remote add` an agent
// ran in its worktree leaks into the live repo; surfacing it lets the operator
// revert the drift. Zero remotes (a single-host install) flags nothing.
func unexpectedRemotes(ctx context.Context, g *git.Repo) ([]CruftItem, error) {
	out, err := g.Run(ctx, "remote")
	if err != nil {
		return nil, err
	}
	var items []CruftItem
	for _, name := range strings.Fields(out) {
		if name == "origin" {
			continue
		}
		items = append(items, CruftItem{Name: name, Detail: "unexpected remote (only origin is expected)"})
	}
	return items, nil
}

// short trims a 40-hex SHA to its 7-char prefix for display.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
