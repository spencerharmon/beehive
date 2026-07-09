package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
)

// hygieneSkill is the cleanup skill the panel points the operator at as the
// explicit remediation action. The panel only SURFACES cruft; running this skill
// (the routine sweep in skills/cleanup.md) is how the operator clears it. The
// panel never invokes it — diagnostic only.
const hygieneSkill = "beehive-hygiene"

// packCountWarn is the live pack-*.pack count at or above which a repo's object
// store is flagged abnormal. A healthy repo holds a single pack right after gc and
// only a handful of packs between runs; auto-gc is disabled swarm-wide
// (git.DisableAutoGC), so nothing self-consolidates a growing pile — the runner's
// deterministic 6-hourly gc (git.MaybeGC) is what compacts it. The 2026-07-08 storm
// left DOZENS of orphan pack-*.pack on disk for hours; this threshold sits above
// normal churn yet well under git's own gc.autoPackLimit default (50) so a genuine
// pile is surfaced early. The panel only REPORTS the count — it never repacks.
const packCountWarn = 24

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

// Hygiene is the whole read-only scan result: the four cruft classes, the
// per-managed-repo object-store census (pack-dir size / live pack count / leftover
// repack-temp count), the skill to remediate them, and an optional scan error
// (surfaced, never swallowed) so the dashboard widget can degrade without failing
// the whole page.
type Hygiene struct {
	Classes []CruftClass
	Packs   []PackStat
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

// PackWarn reports whether any managed repo's object store looks abnormal (a
// leftover repack temp file or an abnormal live-pack count — see PackStat.Warn).
func (h Hygiene) PackWarn() bool {
	for _, p := range h.Packs {
		if p.Warn() {
			return true
		}
	}
	return false
}

// Clean reports whether the scan found nothing worth surfacing at all (no cruft in
// any class AND no object-store storm) and did not error.
func (h Hygiene) Clean() bool { return h.Err == "" && h.Total() == 0 && !h.PackWarn() }

// scanHygiene performs a READ-ONLY sweep of the beehive repo for the git cruft
// that accumulates under updateInstead, returning a per-class drill-down plus a
// per-managed-repo object-store census (pack-dir size / live pack count / leftover
// repack-temp count, for the hive and each declared submodule checkout). It
// MUTATES NOTHING: every git invocation is a query (worktree list, ls-files,
// config --get, remote, rev-parse) and the pack census only stat()s each repo's
// own .git/objects/pack — never a write, prune, remove, repack, or config change.
// root is the beehive repo root; g is a git.Repo rooted there.
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
		Packs: scanPackStores(ctx, root, g, declared),
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

// PackStat is the read-only object-store census for ONE managed repo (the hive
// itself or a submodule's repo/ checkout): the total on-disk size of its pack
// directory, the count of live pack-*.pack files, and the count of leftover repack
// temp files (tmp_pack_*/tmp_idx_*/tmp_rev_*). Reading .git/objects/pack is an
// IN-repo read (the object store IS the repo) — it stays entirely within the
// managed repo, so it does NOT violate the submodule "repo is the only data source"
// invariant, and it is purely descriptive: NOTHING here is ever mutated and this
// path NEVER repacks (the runner's deterministic git gc owns that).
type PackStat struct {
	Name    string // "hive" for the superproject, else the submodule name
	Path    string // display path: "." for the hive, else submodules/<name>/repo
	Packs   int    // live pack-*.pack count
	Temp    int    // leftover tmp_pack_*/tmp_idx_*/tmp_rev_* count (killed-repack residue)
	Bytes   int64  // total pack-dir size in bytes
	Missing bool   // repo not materialized (no resolvable object store to stat)
}

// Warn reports whether this repo's object store looks abnormal: any leftover
// repack temp file (the 2026-07-08 storm signature — a repack killed mid-flight
// leaves tmp_* debris) or an abnormally high live-pack count (dozens of orphan
// packs piling up because nothing consolidated them). A materialized, healthy
// store warns on neither; a Missing repo never warns (there is nothing to stat).
func (p PackStat) Warn() bool { return !p.Missing && (p.Temp > 0 || p.Packs >= packCountWarn) }

// TempWarn reports the leftover-repack-temp signal specifically (the strongest
// mid-flight-kill indicator), so the panel can badge it distinctly.
func (p PackStat) TempWarn() bool { return p.Temp > 0 }

// PackAbnormal reports the abnormal-live-pack-count signal specifically.
func (p PackStat) PackAbnormal() bool { return p.Packs >= packCountWarn }

// Size renders Bytes human-readably (B / KiB / MiB / GiB …) for the panel.
func (p PackStat) Size() string { return humanBytes(p.Bytes) }

// scanPackStores builds the per-repo object-store census for the hive and every
// declared submodule checkout (submodules/<name>/repo, the same set the runner's
// deterministic gc maintains), in a stable order: hive first, then submodules by
// path. Purely read-only; a repo that is not materialized is reported as Missing
// rather than dropped, so the operator sees the full managed-repo list. declared
// is the .gitmodules submodule-path set already computed by the caller.
func scanPackStores(ctx context.Context, root string, g *git.Repo, declared map[string]bool) []PackStat {
	stats := []PackStat{packStoreStat(ctx, g, "hive", ".")}
	paths := make([]string, 0, len(declared))
	for p := range declared {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		stats = append(stats, packStoreStat(ctx, git.New(filepath.Join(root, p)), submoduleName(p), p))
	}
	return stats
}

// submoduleName derives the submodule name from a declared gitlink path
// (submodules/<name>/repo -> <name>); it falls back to the raw path if the shape
// is unexpected, so the census is never left with an empty label.
func submoduleName(path string) string {
	if n := filepath.Base(filepath.Dir(path)); n != "" && n != "." && n != string(filepath.Separator) {
		return n
	}
	return path
}

// packStoreStat resolves ONE repo's object-store pack directory and stat()s it,
// read-only. It resolves the pack dir through git (`rev-parse --git-path
// objects/pack`) so a submodule's `.git`-file gitdir pointer is followed to the
// real store under the superproject's .git/modules/<name>; the same idiom the
// runner's gc sweep uses. A repo with no own .git (an un-materialized submodule
// checkout) is reported Missing WITHOUT running rev-parse, so git's upward .git
// search can never misattribute the parent hive's store to it.
func packStoreStat(ctx context.Context, g *git.Repo, name, path string) PackStat {
	ps := PackStat{Name: name, Path: path}
	if _, err := os.Stat(filepath.Join(g.Dir, ".git")); err != nil {
		ps.Missing = true // no own git dir: not materialized, nothing to stat
		return ps
	}
	out, err := g.Run(ctx, "rev-parse", "--git-path", "objects/pack")
	if err != nil || strings.TrimSpace(out) == "" {
		ps.Missing = true
		return ps
	}
	packDir := strings.TrimSpace(out)
	if !filepath.IsAbs(packDir) {
		packDir = filepath.Join(g.Dir, packDir)
	}
	packs, temp, size, serr := statPackDir(packDir)
	if serr != nil {
		ps.Missing = true // pack dir present but unreadable — cannot assert a census
		return ps
	}
	ps.Packs, ps.Temp, ps.Bytes = packs, temp, size
	return ps
}

// statPackDir counts the object-store census in an ALREADY-RESOLVED pack directory,
// read-only: live pack-*.pack files, leftover repack temp files (tmp_pack_*/
// tmp_idx_*/tmp_rev_*), and the total size of every file in the dir. A pack dir
// that does not exist yet (a fresh repo with only loose objects, nothing repacked)
// is not an error — it reports a clean zero census. Split out so the counting logic
// is unit-testable against a fabricated .git/objects/pack without a git repo.
func statPackDir(packDir string) (packs, temp int, size int64, err error) {
	ents, err := os.ReadDir(packDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if info, ierr := e.Info(); ierr == nil {
			size += info.Size()
		}
		switch {
		case strings.HasPrefix(name, "pack-") && strings.HasSuffix(name, ".pack"):
			packs++
		case strings.HasPrefix(name, "tmp_pack_"),
			strings.HasPrefix(name, "tmp_idx_"),
			strings.HasPrefix(name, "tmp_rev_"):
			temp++
		}
	}
	return packs, temp, size, nil
}

// humanBytes renders a byte count in binary units (B, KiB, MiB, GiB, …) with one
// decimal place above 1 KiB, for compact display in the pack-store census.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
