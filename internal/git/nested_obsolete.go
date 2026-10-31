package git

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// removeObsoleteNestedCheckouts handles an already committed topology change.
// Only the first parent's removed gitlinks are evidence of derived checkouts;
// unavailable history or ambiguous local state is left to the cleanliness gate.
func (r *Repo) removeObsoleteNestedCheckouts(ctx context.Context) error {
	for _, args := range [][]string{
		{"diff", "--cached", "--name-only", "--ignore-submodules=none"},
		{"diff", "--name-only", "--ignore-submodules=all"},
	} {
		out, err := r.Run(ctx, args...)
		if err != nil || out != "" {
			return err
		}
	}
	previous, err := r.Run(ctx, "ls-tree", "-rz", "HEAD^")
	if err != nil {
		return ctx.Err()
	}
	current, err := r.Run(ctx, "ls-tree", "-rz", "HEAD")
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(previous, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[0] != "160000" {
			continue
		}
		// Reject replacement files/directories and gitlinks above or below the
		// old path. Never delete any part of the current committed topology.
		removed := true
		for _, entry := range strings.Split(current, "\x00") {
			_, now, ok := strings.Cut(entry, "\t")
			if ok && (now == path || strings.HasPrefix(now, path+"/") || strings.HasPrefix(path, now+"/")) {
				removed = false
				break
			}
		}
		if !removed || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, "../") {
			continue
		}
		nested := New(filepath.Join(root, path))
		if !nested.obsoleteNestedCheckoutSafe(ctx, r, fields[2]) {
			continue
		}
		// The entire subtree was validated, not just git status at its root.
		// Remove only working files; retained module stores may serve a rename.
		if err := os.RemoveAll(nested.Dir); err != nil {
			return fmt.Errorf("remove obsolete nested checkout %s: %w", nested.Dir, err)
		}
	}
	return ctx.Err()
}

func (r *Repo) obsoleteNestedCheckoutSafe(ctx context.Context, parent *Repo, pin string) bool {
	resolved, err := filepath.EvalSymlinks(r.Dir)
	if err != nil || resolved != r.Dir {
		return false // Includes symlinked ancestors, not just a symlink at .git.
	}
	info, err := os.Lstat(filepath.Join(r.Dir, ".git"))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	head, err := r.RevParse(ctx, "HEAD")
	if err != nil || head != pin {
		return false
	}
	top, err := r.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil || top != r.Dir {
		return false // A gitfile pointing at another checkout is not ours.
	}
	var stores []string
	for _, arg := range []string{"--absolute-git-dir", "--git-common-dir"} {
		store, err := parent.Run(ctx, "rev-parse", "--path-format=absolute", arg)
		if err != nil {
			return false
		}
		store, err = filepath.EvalSymlinks(store)
		if err != nil {
			return false
		}
		stores = append(stores, filepath.Join(store, "modules"))
	}
	for _, arg := range []string{"--absolute-git-dir", "--git-common-dir"} {
		dir, err := r.Run(ctx, "rev-parse", "--path-format=absolute", arg)
		if err != nil {
			return false
		}
		dir, err = filepath.EvalSymlinks(dir)
		if err != nil {
			return false
		}
		owned := false
		for _, store := range stores {
			rel, err := filepath.Rel(store, dir)
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				owned = true
			}
		}
		if !owned {
			return false
		}
	}
	for _, args := range [][]string{
		{"status", "--porcelain", "--untracked-files=all", "--ignored", "--ignore-submodules=none"},
		{"diff", "--cached", "--name-only", "--ignore-submodules=none"},
		{"diff", "--name-only", "--ignore-submodules=none"},
	} {
		out, err := r.Run(ctx, args...)
		if err != nil || out != "" {
			return false
		}
	}
	// These index flags can hide authored content from status/diff.
	files, err := r.Run(ctx, "ls-files", "-v", "-z")
	if err != nil {
		return false
	}
	for _, file := range strings.Split(files, "\x00") {
		if file != "" && !strings.HasPrefix(file, "H ") {
			return false
		}
	}
	tree, err := r.Run(ctx, "ls-tree", "-rz", "HEAD")
	if err != nil {
		return false
	}
	children := make(map[string]bool)
	for _, entry := range strings.Split(tree, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[0] != "160000" {
			continue
		}
		if filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, "../") {
			return false
		}
		child := filepath.Join(r.Dir, path)
		if !New(child).obsoleteNestedCheckoutSafe(ctx, r, fields[2]) {
			return false
		}
		children[child] = true
	}
	// Git omits .git metadata from status, even an unexpected embedded repo.
	// Permit only the gitfile already inspected here and validated children.
	err = filepath.WalkDir(r.Dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if children[path] {
			return filepath.SkipDir
		}
		if entry.Name() == ".git" && path != filepath.Join(r.Dir, ".git") {
			return fmt.Errorf("unexpected repository metadata at %s", path)
		}
		return nil
	})
	return err == nil && ctx.Err() == nil
}
