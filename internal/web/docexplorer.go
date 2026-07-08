package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spencerharmon/beehive/internal/repo"
)

// DocEntry is one file under a submodule's docs/ tree, as surfaced by the doc
// explorer (submodule-doc-explorer). Unlike resolveDocHref (branches.go, which
// only maps a change-doc BASENAME reachable from a commit's Beehive stamp),
// docTree walks the whole tree, so an audit report or a task design doc lands
// in the listing even when no commit's stamp points at it.
type DocEntry struct {
	Path string // slash-separated path relative to the submodule's docs/ dir, e.g. "audit/session-audit-001.md"
	Name string // basename, e.g. "session-audit-001.md" — for display
	Dir  string // parent directory relative to docs/, "" for a top-level file
	Href string // link to view it via the existing doc viewer (doc/{file...})
}

// DocSection groups a submodule's doc entries by their parent directory ("" for
// files directly under docs/, else "audit", "tasks", ...), so the doc explorer
// renders one clearly labeled group per directory instead of a flat list that
// interleaves change docs with audit/task docs.
type DocSection struct {
	Dir   string
	Files []DocEntry
}

// docTree walks sm's docs/ directory recursively — including docs/audit/,
// docs/tasks/, and any other subdirectory — and returns every regular file it
// finds, sorted by path, each carrying a link to the existing doc viewer
// route. It reads only sm.Path/docs, so results can never include another
// submodule's docs, and it is read-only: no directory is created, no file is
// written. A missing or empty docs/ dir yields (nil, nil), matching
// resolveDocHref/changeDocsByTask's "absence is a safe no-op" contract rather
// than surfacing an error for what is simply an unbootstrapped target.
func docTree(sm repo.Submodule) ([]DocEntry, error) {
	root := filepath.Join(sm.Path, "docs")
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, nil
	}
	var entries []DocEntry
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil // unreachable in practice (p is always under root); skip defensively
		}
		dir := filepath.Dir(rel)
		if dir == "." {
			dir = ""
		} else {
			dir = filepath.ToSlash(dir)
		}
		entries = append(entries, DocEntry{
			Path: filepath.ToSlash(rel),
			Name: filepath.Base(rel),
			Dir:  dir,
			Href: "/submodule/" + sm.Name + "/doc/" + filepath.ToSlash(rel),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// sectionDocs groups docTree's (already Path-sorted) entries by Dir. The root
// group ("") always leads when present, then remaining directories in
// alphabetical order — built from a map rather than a linear scan over the
// Path-sorted slice, because a top-level file can sort ALPHABETICALLY between
// two directory names (e.g. a root "bee-x.md" falls between "audit/" and
// "tasks/"), so directory membership is not contiguous in Path order.
func sectionDocs(entries []DocEntry) []DocSection {
	byDir := map[string][]DocEntry{}
	var dirs []string
	for _, e := range entries {
		if _, ok := byDir[e.Dir]; !ok {
			dirs = append(dirs, e.Dir)
		}
		byDir[e.Dir] = append(byDir[e.Dir], e)
	}
	sort.Strings(dirs)
	var secs []DocSection
	if files, ok := byDir[""]; ok {
		secs = append(secs, DocSection{Dir: "", Files: files})
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		secs = append(secs, DocSection{Dir: d, Files: byDir[d]})
	}
	return secs
}

// safeDocPath guards a docs/-relative path against traversal while allowing
// nested segments (docs/audit/*, docs/tasks/*, ...). Unlike safeBranch (a
// single path segment, e.g. a branch name), the doc viewer's {file...}
// wildcard can capture a multi-segment path, so every "/"-separated segment is
// checked individually against safeBranch's charset/length rule, and the path
// as a whole is rejected outright if it is empty, absolute, or contains "..".
// The whole-path ".." check matters even though safeBranch checks it too: a
// percent-encoded traversal segment (e.g. "..%2Fetc%2Fpasswd") reaches the
// handler as an already-decoded string containing a literal "/" BEFORE
// net/http's own literal-dot-segment redirect can clean it, so relying on
// stdlib request sanitizing alone is not enough.
func safeDocPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if !safeBranch(seg) {
			return false
		}
	}
	return true
}
