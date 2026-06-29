package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallROIHook(t *testing.T) {
	root := t.TempDir()
	if err := InstallROIHook(root); err == nil {
		t.Fatal("want error: not a git repo")
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallROIHook(root); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".git", "hooks", "pre-commit")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o100 == 0 {
		t.Fatal("hook not executable")
	}
}
