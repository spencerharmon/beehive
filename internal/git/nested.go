package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PrepareNestedSubmodules materializes every gitlink recorded in HEAD, recursively.
// Call only on the private checkout whose revision is about to be tested. Explicit
// paths and --checkout override inactive/update=none settings, never follow remote
// branch tips, and refuse local changes that would be overwritten.
func (r *Repo) PrepareNestedSubmodules(ctx context.Context) error {
	// update uses the index, not HEAD. Reject authored pins/configuration before
	// it can move a checkout, and inspect all initialized descendants first.
	staged, err := r.Run(ctx, "diff", "--cached", "--raw", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(staged, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == ":160000" || fields[1] == "160000") {
			return fmt.Errorf("prepare nested pins in %s: uncommitted gitlink: %s", r.Dir, line)
		}
	}
	config, err := r.Run(ctx, "diff", "HEAD", "--name-only", "--", ".gitmodules")
	if err != nil {
		return err
	}
	dirty, err := r.Run(ctx, "submodule", "foreach", "--quiet", "--recursive",
		"git diff HEAD --name-only --ignore-submodules=all && git diff --cached --name-only --ignore-submodules=none")
	if err != nil {
		return err
	}
	if config != "" || dirty != "" {
		return fmt.Errorf("prepare nested pins in %s: refusing modified tracked nested source: %s %s", r.Dir, config, dirty)
	}
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
		nested := New(filepath.Join(r.Dir, path))
		if _, err := os.Lstat(filepath.Join(nested.Dir, ".git")); err == nil {
			head, err := nested.RevParse(ctx, "HEAD")
			if err != nil {
				return err
			}
			if head != fields[2] {
				// Remove descendants while their old gitlinks are still registered;
				// the new pin may remove or rename them entirely.
				if err := nested.DeinitNestedSubmodules(ctx); err != nil {
					return err
				}
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if _, err := r.Run(ctx, "submodule", "sync", "--", path); err != nil {
			return fmt.Errorf("prepare nested pin %s in %s: %w", path, r.Dir, err)
		}
		if _, err := r.Run(ctx, "submodule", "update", "--init", "--checkout", "--", path); err != nil {
			return fmt.Errorf("prepare nested pin %s at %s in %s: %w", path, fields[2], r.Dir, err)
		}
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

// DeinitNestedSubmodules removes only clean derived checkouts before a topology
// change. Non-force deinit alone does not protect edits in deeper descendants.
func (r *Repo) DeinitNestedSubmodules(ctx context.Context) error {
	out, err := r.Run(ctx, "submodule", "foreach", "--quiet", "--recursive",
		"git status --porcelain --untracked-files=all --ignored --ignore-submodules=all && git diff --cached --name-only --ignore-submodules=none")
	if err != nil {
		return err
	}
	if out != "" {
		return fmt.Errorf("refusing to remove nested checkouts with local changes in %s: %s", r.Dir, out)
	}
	// Linked worktrees share parent config. Retain registration/settings for
	// other checkouts when deinit removes the submodule sections.
	config, err := r.Run(ctx, "config", "--local", "--null", "--list")
	if err != nil {
		return err
	}
	_, deinitErr := r.Run(ctx, "submodule", "deinit", "--all")
	restored := make(map[string]bool)
	for _, entry := range strings.Split(config, "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		if !strings.HasPrefix(key, "submodule.") {
			continue
		}
		flag := "--add"
		if !restored[key] {
			flag = "--replace-all"
		}
		if _, err := r.Run(ctx, "config", "--local", flag, key, value); err != nil {
			deinitErr = errors.Join(deinitErr, fmt.Errorf("restoring shared submodule configuration: %w", err))
		}
		restored[key] = true
	}
	return deinitErr
}

// PrepareCleanNestedCheckout repairs derived pin drift only when the parent has
// no authored changes. It never treats staged gitlinks as disposable state.
func (r *Repo) PrepareCleanNestedCheckout(ctx context.Context) error {
	if err := r.removeObsoleteNestedCheckouts(ctx); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"status", "--porcelain", "--untracked-files=all", "--ignore-submodules=all"},
		{"diff", "--cached", "--name-only", "--ignore-submodules=none"},
	} {
		out, err := r.Run(ctx, args...)
		if err != nil {
			return err
		}
		if out != "" {
			return nil // The caller's ordinary cleanliness gate reports these edits.
		}
	}
	return r.PrepareNestedSubmodules(ctx)
}
