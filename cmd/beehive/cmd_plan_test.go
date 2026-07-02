package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveDocContentDeterministic(t *testing.T) {
	a := archiveDocContent("t1", "Impl: did the thing.\nReview: approved.")
	b := archiveDocContent("t1", "Impl: did the thing.\nReview: approved.")
	if a != b {
		t.Fatal("archiveDocContent not deterministic")
	}
	if !strings.Contains(a, "# t1 — archived PLAN.md closure narrative") {
		t.Fatalf("missing title:\n%s", a)
	}
	if !strings.Contains(a, "Impl: did the thing.\nReview: approved.") {
		t.Fatalf("narrative not embedded verbatim:\n%s", a)
	}
}

// TestWriteArchiveDocFreshAndAppend proves a first archive creates the file with
// the narrative, and a SECOND archive of the same id (a re-opened/re-closed task)
// appends under a divider without dropping the earlier record.
func TestWriteArchiveDocFreshAndAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t1.md")

	if err := writeArchiveDoc(path, "t1", "first narrative body"); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if !strings.Contains(string(first), "first narrative body") {
		t.Fatalf("fresh write lost narrative:\n%s", first)
	}
	if strings.Contains(string(first), "re-archived after re-open") {
		t.Fatal("fresh write should not contain the re-open divider")
	}

	if err := writeArchiveDoc(path, "t1", "second narrative body"); err != nil {
		t.Fatal(err)
	}
	both, _ := os.ReadFile(path)
	s := string(both)
	if !strings.Contains(s, "first narrative body") {
		t.Fatalf("append dropped the prior archive:\n%s", s)
	}
	if !strings.Contains(s, "second narrative body") {
		t.Fatalf("append missing the new archive:\n%s", s)
	}
	if !strings.Contains(s, "re-archived after re-open") {
		t.Fatalf("append missing the divider:\n%s", s)
	}
	if strings.Index(s, "first narrative body") > strings.Index(s, "second narrative body") {
		t.Fatal("append reordered the records")
	}
}
