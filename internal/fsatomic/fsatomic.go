// Package fsatomic writes a file atomically and durably, so a process (or host)
// killed mid-write can never leave a truncated or NUL-holed file behind.
//
// A plain os.WriteFile opens the target O_TRUNC and streams the new bytes in
// place. Two failure modes follow from that when the writer dies partway:
//
//   - Truncation: the file is left as a prefix of the intended content.
//   - NUL hole: under a delayed-allocation filesystem (ext4 default), a crash
//     after the metadata length is journaled but before the data blocks are
//     flushed leaves those blocks reading back as NUL bytes.
//
// PLAN.md is the runner's single most safety-critical mutable file, and a
// corrupt PLAN.md (observed live: 8 NUL bytes plus a duplicated task heading in
// a submodule's PLAN.md after a pass was killed mid-write) silently wedges the
// swarm — the parser tolerated the bytes, task selection kept re-picking the
// stranded twin, and every pass exited with zero turns. WriteFile defeats both
// modes: it writes a sibling temp file, fsyncs it, atomically renames it over
// the target, then fsyncs the parent directory so the rename itself is durable.
// A reader (or the next runner) therefore observes either the whole old file or
// the whole new file, never a partial one, across a crash.
package fsatomic

import (
	"os"
	"path/filepath"
)

// WriteFile atomically and durably writes data to path with the given mode. The
// temp file is created in the same directory as path (so the rename stays on one
// filesystem and is atomic) and is removed if any step before the rename fails.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Clean up the temp file on any pre-rename failure. Cleared to "" once the
	// rename succeeds so we never remove the live target.
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// Flush the data blocks before the rename; without this the rename can be
	// durable while the data is not (the NUL-hole mode above).
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	tmp = "" // renamed into place; the defer must not remove it now
	// Persist the directory entry so the rename survives a crash.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
