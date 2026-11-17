package fsremove

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAllReadOnlyTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	mod := filepath.Join(root, "go", "pkg", "mod", "example.com", "m@v1.0.0")
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
	if err := os.RemoveAll(root); err == nil {
		t.Skip("os.RemoveAll succeeded on a read-only tree (running as root?)")
	}
	if err := All(root); err != nil {
		t.Fatalf("All: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("tree still present: %v", err)
	}
}

func TestAllMissingPath(t *testing.T) {
	if err := All(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("All on missing path: %v", err)
	}
}
