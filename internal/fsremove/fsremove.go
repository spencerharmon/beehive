// Package fsremove deletes directory trees that os.RemoveAll alone cannot.
//
// Honeybee worktrees and per-pass data dirs routinely end up holding two kinds of
// content a plain recursive delete fails on:
//
//   - read-only directories (Go's module cache marks every module dir 0555), and
//   - rootless-podman storage and bind-mounted container data, owned by sub-UIDs
//     of the user namespace, which only `podman unshare` can delete.
//
// A failed delete used to be silently ignored, leaving multi-GB trees behind under
// .worktrees/ and the temp dir. All leaked as dead weight on the host disk.
package fsremove

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// All removes path and everything below it. It tries os.RemoveAll, then makes
// every directory owner-writable and retries, then falls back to
// `podman unshare rm -rf` when podman is installed. A path that does not exist
// is not an error.
func All(path string) error {
	err := os.RemoveAll(path)
	if err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, werr error) error {
		if werr == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	if err = os.RemoveAll(path); err == nil {
		return nil
	}
	podman, lerr := exec.LookPath("podman")
	if lerr != nil {
		return err
	}
	out, uerr := exec.Command(podman, "unshare", "rm", "-rf", "--", path).CombinedOutput()
	if _, serr := os.Lstat(path); errors.Is(serr, fs.ErrNotExist) {
		return nil
	}
	if uerr != nil {
		return fmt.Errorf("%v; podman unshare rm: %v: %s", err, uerr, strings.TrimSpace(string(out)))
	}
	return err
}
