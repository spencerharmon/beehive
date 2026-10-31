package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// PrepareNestedSubmodules materializes every gitlink recorded in HEAD, recursively.
// Call only on the private checkout whose revision is about to be tested. Explicit
// paths and --checkout override inactive/update=none settings, never follow remote
// branch tips, and refuse local changes that would be overwritten.
func (r *Repo) PrepareNestedSubmodules(ctx context.Context) error {
	out, err := r.Run(ctx, "ls-tree", "-rz", "HEAD")
	if err != nil {
		return fmt.Errorf("prepare nested pins in %s: %w", r.Dir, err)
	}
	for _, entry := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[0] != "160000" {
			continue
		}
		if _, err := r.Run(ctx, "submodule", "sync", "--", path); err != nil {
			return fmt.Errorf("prepare nested pin %s in %s: %w", path, r.Dir, err)
		}
		if _, err := r.Run(ctx, "submodule", "update", "--init", "--checkout", "--", path); err != nil {
			return fmt.Errorf("prepare nested pin %s at %s in %s: %w", path, fields[2], r.Dir, err)
		}
		nested := New(filepath.Join(r.Dir, path))
		head, err := nested.RevParse(ctx, "HEAD")
		if err != nil {
			return err
		}
		if head != fields[2] {
			return fmt.Errorf("prepare nested pin %s in %s: got %s, want %s", path, r.Dir, head, fields[2])
		}
		if err := nested.PrepareNestedSubmodules(ctx); err != nil {
			return err
		}
	}
	return nil
}
