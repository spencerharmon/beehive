// Ephemeral data-dir lifecycle: creation with an owner stamp, robust removal, and
// a sweep of dirs leaked by passes that never reached Close (killed by
// RuntimeMaxSec, a stop, OOM, or a crash — deferred cleanup does not run on a
// signal). The data dir is the child's XDG_DATA_HOME, so every tool the agent runs
// inherits it: rootless podman puts its container storage there (files owned by
// sub-UIDs that os.RemoveAll cannot delete) and Go's module cache is read-only.
// Each leaked dir was ~100 MB, and enough of them filled the host's tmpfs /tmp.
package swarm

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spencerharmon/beehive/internal/fsremove"
)

const (
	ephemeralDirPrefix = "beehive-opencode-"
	// ephemeralOwnerFile records the pid of the runner that owns a data dir, so a
	// sweep can tell a live pass's dir from an abandoned one.
	ephemeralOwnerFile = ".beehive-owner"
	// ephemeralLegacyMaxAge bounds how long an unstamped dir (made by a runner
	// that predates the owner stamp) is presumed live. No pass outlives this.
	ephemeralLegacyMaxAge = 24 * time.Hour
)

// newEphemeralDataDir sweeps abandoned sibling data dirs, then creates a fresh
// one stamped with this process's pid.
func newEphemeralDataDir(log io.Writer) (string, error) {
	SweepStaleEphemeralDataDirs(os.TempDir(), log)
	dir, err := os.MkdirTemp("", ephemeralDirPrefix)
	if err != nil {
		return "", fmt.Errorf("ephemeral agent: create temp data dir: %w", err)
	}
	owner := filepath.Join(dir, ephemeralOwnerFile)
	if err := os.WriteFile(owner, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		_ = removeEphemeralDataDir(dir)
		return "", fmt.Errorf("ephemeral agent: stamp %s: %w", owner, err)
	}
	return dir, nil
}

// SweepStaleEphemeralDataDirs removes ephemeral data dirs under base whose owner
// is gone: the stamped pid is no longer running, or (for an unstamped dir) it is
// older than ephemeralLegacyMaxAge. Best effort: failures are logged, never fatal,
// so a stubborn leftover can never block a pass from starting.
func SweepStaleEphemeralDataDirs(base string, log io.Writer) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ephemeralDirPrefix) {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if !ephemeralDirAbandoned(dir, e) {
			continue
		}
		if err := removeEphemeralDataDir(dir); err != nil {
			if log != nil {
				fmt.Fprintf(log, "ephemeral agent: sweep %s: %v\n", dir, err)
			}
			continue
		}
		if log != nil {
			fmt.Fprintf(log, "ephemeral agent: swept abandoned data dir %s\n", dir)
		}
	}
}

func ephemeralDirAbandoned(dir string, e fs.DirEntry) bool {
	b, err := os.ReadFile(filepath.Join(dir, ephemeralOwnerFile))
	if err != nil {
		info, ierr := e.Info()
		return ierr == nil && time.Since(info.ModTime()) > ephemeralLegacyMaxAge
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return true
	}
	return !processAlive(pid)
}

// processAlive reports whether pid exists. EPERM means it exists but belongs to
// someone else, which still counts as alive.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// removeEphemeralDataDir deletes dir, including read-only module caches and
// rootless podman storage (see fsremove).
func removeEphemeralDataDir(dir string) error {
	return fsremove.All(dir)
}
