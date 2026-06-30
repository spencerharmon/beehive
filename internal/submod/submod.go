// Package submod centralizes the mutating submodule operations that the CLI and
// the web frontend must perform identically: registering a target repo as a real
// tracked git submodule, and recording submodule links / task dependencies
// through the cycle-checked links schema. Both cmd/beehive and internal/web call
// these so the frontend stops diverging from the vetted CLI behavior (it used to
// bare-mkdir a submodule dir and append raw `from: [to]` YAML). Deterministic
// git/YAML side effects; no LLM.
package submod

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/links"
	"github.com/spencerharmon/beehive/internal/repo"
)

// Sentinel errors let callers (e.g. the web handlers) map a failure to the right
// HTTP status: the *Invalid*/*Required* and *Cycle* cases are user-correctable
// (400/409); anything else is an I/O or git failure (500).
var (
	// ErrURLRequired is returned by Add when no repo url is given.
	ErrURLRequired = errors.New("submod: repo url required")
	// ErrInvalidName is returned by Add when the (given or derived) name is not a
	// single, safe path segment.
	ErrInvalidName = errors.New("submod: invalid submodule name")
	// ErrExists is returned by Add when a submodule already occupies the name.
	ErrExists = errors.New("submod: submodule already exists")
	// ErrInvalidDep is returned by AddDep when from/to are empty.
	ErrInvalidDep = errors.New("submod: dependency requires from and to")
	// ErrCycle wraps the links-layer rejection when an edge would close a wait
	// cycle (or be a self-dependency), so the frontend can answer 409/400.
	ErrCycle = errors.New("submod: dependency would create a cycle")
	// ErrInvalidLink is returned by LinkSubmodules for empty or self links.
	ErrInvalidLink = errors.New("submod: link requires two distinct submodule names")
)

// ValidName reports whether name is a usable submodule directory name: a single
// non-empty path segment that is not a relative-path token. Rejecting "", ".",
// "..", and any separator keeps submodules/<name> inside the repo.
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	return filepath.Base(name) == name
}

// Add registers url as a tracked git submodule at submodules/<name>/repo on the
// given branch and creates the sibling beehive worktrees dir. name defaults to
// the url basename (sans .git); branch defaults to "main". It runs the same
// `git submodule add` the CLI does (not a bare mkdir), so the entry is a real,
// tracked, cloneable submodule. The staged changes (.gitmodules + the gitlink)
// are left for the caller to commit. Returns the resolved name.
func Add(ctx context.Context, root, url, name, branch string) (string, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", ErrURLRequired
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(url), ".git")
	}
	if !ValidName(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if branch == "" {
		branch = "main"
	}
	subdir := filepath.Join(root, "submodules", name)
	if _, err := os.Stat(filepath.Join(subdir, "repo")); err == nil {
		return "", fmt.Errorf("%w: %q", ErrExists, name)
	}
	// Create the beehive worktrees dir up front (the submodule layer lives
	// alongside repo/); `git submodule add` fills in repo/ itself.
	if err := os.MkdirAll(filepath.Join(subdir, "worktrees"), 0o755); err != nil {
		return "", err
	}
	rel := filepath.Join("submodules", name, "repo")
	if _, err := git.New(root).Run(ctx, "submodule", "add", "-b", branch, url, rel); err != nil {
		return "", err
	}
	return name, nil
}

// LinkSubmodules records an undirected link between submodules a and b in each
// one's SUBMODULE-LINKS.yaml via the links schema (sorted, valid YAML),
// idempotently. This is exactly what `beehive submodule link` does, shared so the
// web matches it. Submodule adjacency authorizes cross-submodule deps and cannot
// itself form a cycle. The writes are left uncommitted for the caller.
func LinkSubmodules(root, a, b string) error {
	if a == "" || b == "" || a == b {
		return ErrInvalidLink
	}
	for _, sm := range []string{a, b} {
		p := filepath.Join(root, "submodules", sm, repo.LinksFile)
		l, err := links.Load(p)
		if err != nil {
			return err
		}
		l.LinkSubmodules(a, b)
		if err := l.Save(p); err != nil {
			return err
		}
	}
	return nil
}

// AddDep records a from->to dependency edge in root's SUBMODULE-LINKS.yaml
// through the cycle-checked links schema, returning ErrCycle (and writing
// nothing) when the edge would close a wait cycle or be a self-dependency. This
// replaces the frontend's raw `from: [to]` append with valid, sorted YAML. The
// write is left uncommitted for the caller.
func AddDep(root, from, to string) error {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" {
		return ErrInvalidDep
	}
	p := filepath.Join(root, repo.LinksFile)
	l, err := links.Load(p)
	if err != nil {
		return err
	}
	if err := l.AddDep(from, to); err != nil {
		return fmt.Errorf("%w: %v", ErrCycle, err)
	}
	return l.Save(p)
}
