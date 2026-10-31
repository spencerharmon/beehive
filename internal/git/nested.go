package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PrepareNestedSubmodules materializes the gitlinks of a private source checkout.
// Unlike the hive's tracked-tip synchronization, nested dependencies follow their
// recorded pins, not remote branch tips. Do not call this on the shared checkout.
func (r *Repo) PrepareNestedSubmodules(ctx context.Context) error {
	// Do not let git ascend from an empty gitlink (or a non-repository check
	// directory) and accidentally initialize the containing hive.
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("prepare nested submodules in %s: %w", r.Dir, err)
	}
	entries, err := r.Run(ctx, "ls-files", "--stage", "-z")
	if err != nil {
		return fmt.Errorf("list nested submodules in %s: %w", r.Dir, err)
	}
	var paths []string
	for _, entry := range strings.Split(entries, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok || !strings.HasPrefix(meta, "160000 ") {
			continue
		}
		if !strings.HasSuffix(meta, " 0") {
			return fmt.Errorf("prepare nested submodules in %s: unresolved gitlink %s", r.Dir, path)
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(r.Dir, ".gitmodules")); err != nil {
		return fmt.Errorf("prepare nested submodules in %s: gitlinks require .gitmodules: %w", r.Dir, err)
	}
	// Explicit paths bypass submodule.active filtering. Traverse one level at a
	// time so a changed parent pin exposes its new child URLs BEFORE child sync.
	if _, err := r.Run(ctx, append([]string{"submodule", "sync", "--"}, paths...)...); err != nil {
		return fmt.Errorf("sync nested submodules in %s: %w", r.Dir, err)
	}
	if _, err := r.Run(ctx, append([]string{"-c", "submodule.recurse=false", "submodule", "update", "--init", "--checkout", "--"}, paths...)...); err != nil {
		return fmt.Errorf("prepare nested submodules in %s: %w", r.Dir, err)
	}
	for _, path := range paths {
		child := New(filepath.Join(r.Dir, path))
		pin, err := r.RevParse(ctx, ":"+path)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(child.Dir, ".git")); err != nil {
			return fmt.Errorf("prepare nested submodule %s: checkout missing: %w", child.Dir, err)
		}
		head, err := child.RevParse(ctx, "HEAD")
		if err != nil {
			return err
		}
		if head != pin {
			return fmt.Errorf("prepare nested submodule %s: HEAD %s does not match pin %s", child.Dir, head, pin)
		}
		if err := child.PrepareNestedSubmodules(ctx); err != nil {
			return err
		}
	}
	return nil
}
