package fsatomic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileCreatesAndReadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	want := []byte("hello world\n")
	if err := WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("content mismatch: got %q want %q", got, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("perm = %v want 0644", fi.Mode().Perm())
	}
}

func TestWriteFileOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := WriteFile(path, []byte("v1-longer-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2" {
		t.Fatalf("overwrite: got %q want v2", got)
	}
	// No temp files left behind after either write.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	if len(ents) != 1 {
		t.Fatalf("want exactly 1 file in dir, got %d", len(ents))
	}
}

func TestWriteFileErrorsOnBadDir(t *testing.T) {
	// Parent directory does not exist -> temp create fails, no target written.
	path := filepath.Join(t.TempDir(), "nope", "f.txt")
	if err := WriteFile(path, []byte("x"), 0o644); err == nil {
		t.Fatal("want error writing into a nonexistent directory")
	}
}
