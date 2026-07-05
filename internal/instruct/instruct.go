// Package instruct manages the beehive-shipped instruction files that live at a
// repo's root (AGENTS.md, HONEYBEE.md, BOOTSTRAP.md). The binary embeds a default
// copy of each; Install lays down any that are missing, and Update refreshes them
// to the current defaults, backing up an operator-customized file before replacing
// it. Site-specific files (LOCALS.md) and per-repo content are never managed here.
package instruct

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/prompts"
)

// File is one managed instruction file: the on-disk name and the binary's current
// default body.
type File struct {
	Name    string
	Default string
}

// Files returns the managed instruction-file set, in install order: the root docs
// first, then every skill under skills/. Names may contain a directory (skills/...);
// callers create parent dirs as needed.
func Files() []File {
	files := []File{
		{Name: "AGENTS.md", Default: prompts.Agents},
		{Name: "HONEYBEE.md", Default: prompts.Honeybee},
		{Name: "BOOTSTRAP.md", Default: prompts.BootstrapGuide},
	}
	for _, s := range prompts.Skills() {
		files = append(files, File{Name: filepath.ToSlash(filepath.Join("skills", s.Name)), Default: s.Body})
	}
	return files
}

// writeFile writes body to <root>/<name>, creating parent directories (skills/).
func writeFile(root, name string, body []byte) error {
	dst := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o644)
}

// Status is the per-file outcome of a scan or update.
type Status int

const (
	// Missing: the file does not exist on disk.
	Missing Status = iota
	// Clean: the file matches the current default.
	Clean
	// Modified: the file exists but differs from the current default.
	Modified
)

func (s Status) String() string {
	switch s {
	case Missing:
		return "missing"
	case Clean:
		return "clean"
	case Modified:
		return "modified"
	default:
		return "unknown"
	}
}

// Scan reports the status of every managed file under root without changing it.
func Scan(root string) (map[string]Status, error) {
	out := map[string]Status{}
	for _, f := range Files() {
		st, _, err := stat(root, f)
		if err != nil {
			return nil, err
		}
		out[f.Name] = st
	}
	return out, nil
}

// StatusOf reports the drift status of the single managed file named name under
// root: Clean when it is byte-identical to the binary's embedded default, Modified
// when it exists but differs, Missing when it is absent. ok is false when name is
// not a managed file, in which case the caller must show no drift for it — this is
// what keeps a site-authored file (LOCALS.md, which is never in the managed set)
// out of the drift check. It is the per-file form of Scan for a caller (the
// frontend) that iterates its OWN declared set and asks the drift status one file
// at a time, reusing this package's single embedded-default source (no second copy).
func StatusOf(root, name string) (st Status, ok bool, err error) {
	for _, f := range Files() {
		if f.Name == name {
			st, _, err = stat(root, f)
			return st, true, err
		}
	}
	return Missing, false, nil
}

func stat(root string, f File) (Status, []byte, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Name)))
	if os.IsNotExist(err) {
		return Missing, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	if string(b) == f.Default {
		return Clean, b, nil
	}
	return Modified, b, nil
}

// Result records what Update did to one file.
type Result struct {
	Name   string
	Action string // "created", "up-to-date", "updated", "backed-up", "skipped"
	Backup string // path of the .bak written, when Action == "backed-up"
}

// Confirm is asked, per modified file, whether to overwrite it (true) or leave it
// (false). Update calls it only when clobber is false and a file is Modified. A nil
// Confirm means "do not overwrite modified files" (they are skipped).
type Confirm func(name string) bool

// Update brings every managed file under root to the current default and commits
// the changes. Missing files are created; clean files are left; a modified file is
// replaced only when clobber is true OR confirm(name) returns true, in which case
// the prior contents are first written to <name>.<epoch>.bak. Both the backup and
// the refreshed file are committed. Returns the per-file results.
func Update(ctx context.Context, root string, clobber bool, confirm Confirm) ([]Result, error) {
	g := git.New(root)
	var results []Result
	var commitPaths []string
	for _, f := range Files() {
		st, cur, err := stat(root, f)
		if err != nil {
			return nil, err
		}
		switch st {
		case Clean:
			results = append(results, Result{Name: f.Name, Action: "up-to-date"})
		case Missing:
			if err := writeFile(root, f.Name, []byte(f.Default)); err != nil {
				return nil, err
			}
			commitPaths = append(commitPaths, f.Name)
			results = append(results, Result{Name: f.Name, Action: "created"})
		case Modified:
			overwrite := clobber
			if !overwrite && confirm != nil {
				overwrite = confirm(f.Name)
			}
			if !overwrite {
				results = append(results, Result{Name: f.Name, Action: "skipped"})
				continue
			}
			bak := fmt.Sprintf("%s.%d.bak", f.Name, time.Now().Unix())
			if err := writeFile(root, bak, cur); err != nil {
				return nil, err
			}
			if err := writeFile(root, f.Name, []byte(f.Default)); err != nil {
				return nil, err
			}
			commitPaths = append(commitPaths, bak, f.Name)
			results = append(results, Result{Name: f.Name, Action: "backed-up", Backup: bak})
		}
	}
	if len(commitPaths) > 0 {
		if err := g.CommitPaths(ctx, "beehive instruction update", commitPaths...); err != nil && err != git.ErrNothing {
			return results, fmt.Errorf("commit instruction update: %w", err)
		}
	}
	return results, nil
}

// Install writes any managed files that are missing under root (used by
// `beehive init`). It never overwrites an existing file and does not commit; the
// caller owns the initial commit. Returns the names it created.
func Install(root string) ([]string, error) {
	var created []string
	for _, f := range Files() {
		dst := filepath.Join(root, filepath.FromSlash(f.Name))
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return created, err
		}
		if err := writeFile(root, f.Name, []byte(f.Default)); err != nil {
			return created, err
		}
		created = append(created, f.Name)
	}
	return created, nil
}
