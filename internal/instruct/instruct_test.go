package instruct

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
}

// TestInstallThenUpdate covers the lifecycle: Install lays down missing files;
// Update is a no-op when clean; a modified file is left alone without clobber and
// backed-up+replaced with clobber, committing both.
func TestInstallThenUpdate(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	ctx := context.Background()

	created, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != len(Files()) {
		t.Fatalf("Install created %v, want all %d managed files", created, len(Files()))
	}
	for _, f := range Files() {
		if _, err := os.Stat(filepath.Join(root, f.Name)); err != nil {
			t.Fatalf("%s not installed: %v", f.Name, err)
		}
	}
	// The managed set must include the skills/ directory files, not just the three
	// root docs, and they must land under skills/.
	var skillCount int
	for _, f := range Files() {
		if strings.HasPrefix(f.Name, "skills/") {
			skillCount++
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f.Name))); err != nil {
				t.Fatalf("skill %s not installed under skills/: %v", f.Name, err)
			}
		}
	}
	if skillCount == 0 {
		t.Fatal("managed set has no skills/ files; skills must be individually tracked")
	}
	// Install does not commit (the init caller owns that); track them so a later
	// clobber commit can leave a clean tree.
	commitAll(t, root, "init instructions")

	// Clean tree: Update reports everything up-to-date and writes nothing new.
	res, err := Update(ctx, root, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Action != "up-to-date" {
			t.Fatalf("clean file %s: got %q want up-to-date", r.Name, r.Action)
		}
	}

	// Operator customizes HONEYBEE.md.
	hb := filepath.Join(root, "HONEYBEE.md")
	if err := os.WriteFile(hb, []byte("# my custom protocol\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without clobber and a nil confirm, the modified file is skipped (untouched).
	res, _ = Update(ctx, root, false, nil)
	if a := actionFor(res, "HONEYBEE.md"); a != "skipped" {
		t.Fatalf("modified file without clobber: got %q want skipped", a)
	}
	if b, _ := os.ReadFile(hb); string(b) != "# my custom protocol\n" {
		t.Fatalf("skipped file must be left untouched, got %q", b)
	}

	// With clobber: backup written, file restored to default, both committed.
	res, err = Update(ctx, root, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := resultFor(res, "HONEYBEE.md")
	if r.Action != "backed-up" || r.Backup == "" {
		t.Fatalf("clobber: got %+v want backed-up with a backup path", r)
	}
	bak, err := os.ReadFile(filepath.Join(root, r.Backup))
	if err != nil || string(bak) != "# my custom protocol\n" {
		t.Fatalf("backup must preserve the custom content: %v %q", err, bak)
	}
	if b, _ := os.ReadFile(hb); string(b) != prompts_Honeybee() {
		t.Fatalf("file not restored to the default after clobber")
	}
	// The update commit landed and the tree is clean (backup + file committed).
	out, _ := exec.Command("git", "-C", root, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("instruction update left the tree dirty:\n%s", out)
	}
}

func actionFor(res []Result, name string) string { return resultFor(res, name).Action }
func resultFor(res []Result, name string) Result {
	for _, r := range res {
		if r.Name == name {
			return r
		}
	}
	return Result{}
}

// prompts_Honeybee returns the default HONEYBEE.md body via the managed set, so
// the test does not import the prompts package directly.
func prompts_Honeybee() string {
	for _, f := range Files() {
		if f.Name == "HONEYBEE.md" {
			return f.Default
		}
	}
	return ""
}
