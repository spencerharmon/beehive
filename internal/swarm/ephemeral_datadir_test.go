package swarm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func mkEphemeralDir(t *testing.T, base, name, owner string) string {
	t.Helper()
	dir := filepath.Join(base, ephemeralDirPrefix+name)
	if err := os.MkdirAll(filepath.Join(dir, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if owner != "" {
		if err := os.WriteFile(filepath.Join(dir, ephemeralOwnerFile), []byte(owner), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// deadPid returns the pid of a process that has already exited and been reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot run true: %v", err)
	}
	return cmd.Process.Pid
}

func TestSweepStaleEphemeralDataDirs(t *testing.T) {
	base := t.TempDir()
	live := mkEphemeralDir(t, base, "live", strconv.Itoa(os.Getpid()))
	dead := mkEphemeralDir(t, base, "dead", strconv.Itoa(deadPid(t)))
	garbage := mkEphemeralDir(t, base, "garbage", "not-a-pid")
	legacyNew := mkEphemeralDir(t, base, "legacy-new", "")
	legacyOld := mkEphemeralDir(t, base, "legacy-old", "")
	old := time.Now().Add(-2 * ephemeralLegacyMaxAge)
	if err := os.Chtimes(legacyOld, old, old); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(base, "other-app-dir")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unrelated, old, old); err != nil {
		t.Fatal(err)
	}

	SweepStaleEphemeralDataDirs(base, nil)

	for _, keep := range []string{live, legacyNew, unrelated} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s removed, want kept: %v", filepath.Base(keep), err)
		}
	}
	for _, gone := range []string{dead, garbage, legacyOld} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still present, want swept (stat err %v)", filepath.Base(gone), err)
		}
	}
}

func TestRemoveEphemeralDataDirReadOnlyTree(t *testing.T) {
	dir := mkEphemeralDir(t, t.TempDir(), "ro", "")
	mod := filepath.Join(dir, "go", "pkg", "mod", "example.com", "m@v1.0.0")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	// Go's module cache marks directories read-only, which defeats os.RemoveAll.
	if err := os.Chmod(mod, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := removeEphemeralDataDir(dir); err != nil {
		t.Fatalf("removeEphemeralDataDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir still present: %v", err)
	}
}

func TestNewEphemeralDataDirStampsOwner(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	stale := mkEphemeralDir(t, os.TempDir(), "stale", strconv.Itoa(deadPid(t)))
	dir, err := newEphemeralDataDir(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ephemeralOwnerFile))
	if err != nil || string(b) != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("owner stamp = %q, %v; want our pid", b, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale sibling not swept: %v", err)
	}
	if err := removeEphemeralDataDir(dir); err != nil {
		t.Fatal(err)
	}
}
