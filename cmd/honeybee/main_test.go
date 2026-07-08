package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

// gitInit makes dir a real git repo (branch main) with a committer identity, the
// minimum the drift-guard's real git operations (CommitExists/IsAncestor/
// GitlinkAt) need. Mirrors internal/git's own test setup.
func gitInit(t *testing.T, dir string) *git.Repo {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	r := git.New(dir)
	ctx := context.Background()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if _, err := r.Run(ctx, a...); err != nil {
			t.Fatalf("git %v: %v", a, err)
		}
	}
	return r
}

func commitFile(t *testing.T, r *git.Repo, name, body string) string {
	t.Helper()
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if _, err := r.Run(ctx, "add", "--", name); err != nil {
		t.Fatalf("add %s: %v", name, err)
	}
	if _, err := r.Run(ctx, "commit", "-q", "-m", "c "+name); err != nil {
		t.Fatalf("commit %s: %v", name, err)
	}
	sha, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return sha
}

// newDriftHive builds a realistic fixture: a "beehive product" submodule with two
// sequential commits (c1 an ancestor of c2), an UNRELATED target submodule (flux)
// whose history shares nothing with the product, and a hive superproject on main
// whose tree pins the beehive gitlink to the requested pointer ("c1" or "c2").
// Returns the hive git.Repo, its listed submodules, and c1/c2.
func newDriftHive(t *testing.T, pointer string) (hive *git.Repo, subs []repo.Submodule, c1, c2 string) {
	t.Helper()
	ctx := context.Background()
	hiveDir := t.TempDir()

	// The beehive product repo (self): two commits, c1 -> c2.
	prod := gitInit(t, filepath.Join(hiveDir, "submodules", "beehive", "repo"))
	c1 = commitFile(t, prod, "a", "one")
	c2 = commitFile(t, prod, "b", "two")

	// An unrelated target (its commit must never be mistaken for our build SHA).
	flux := gitInit(t, filepath.Join(hiveDir, "submodules", "flux", "repo"))
	fluxSHA := commitFile(t, flux, "f", "flux")

	beePtr := c2
	if pointer == "c1" {
		beePtr = c1
	}

	// The hive superproject: AGENTS.md (so repo.Open accepts it) + two real
	// gitlinks staged as a superproject records submodule pointers, committed on
	// main. Explicit staging only — never `add -A`, which would auto-embed the
	// nested repos at their own HEAD instead of the pointer under test.
	hive = gitInit(t, hiveDir)
	if err := os.WriteFile(filepath.Join(hiveDir, repo.AgentsFile), []byte("hive\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if _, err := hive.Run(ctx, "add", "--", repo.AgentsFile); err != nil {
		t.Fatalf("add AGENTS.md: %v", err)
	}
	if _, err := hive.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+beePtr+",submodules/beehive/repo"); err != nil {
		t.Fatalf("stage beehive gitlink: %v", err)
	}
	if _, err := hive.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+fluxSHA+",submodules/flux/repo"); err != nil {
		t.Fatalf("stage flux gitlink: %v", err)
	}
	if _, err := hive.Run(ctx, "commit", "-q", "-m", "hive"); err != nil {
		t.Fatalf("commit hive: %v", err)
	}

	rp, err := repo.Open(hiveDir)
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	subs, err = rp.Submodules()
	if err != nil {
		t.Fatalf("Submodules: %v", err)
	}
	return hive, subs, c1, c2
}

func TestPromptEmbedDriftWarning(t *testing.T) {
	ctx := context.Background()

	// Stale: built from c1 while the hive pins the tip at c2 -> the build does not
	// contain the tip -> warn, naming the self submodule and both short SHAs.
	t.Run("stale-warns", func(t *testing.T) {
		hive, subs, c1, c2 := newDriftHive(t, "c2")
		w := promptEmbedDriftWarning(ctx, hive, "HEAD", c1, subs)
		if w == "" {
			t.Fatal("stale build produced no warning, want one")
		}
		for _, want := range []string{"submodules/beehive/repo", shortSHA(c1), shortSHA(c2)} {
			if !strings.Contains(w, want) {
				t.Fatalf("warning %q missing %q", w, want)
			}
		}
	})

	// Fresh: built from the exact tracked tip -> silent.
	t.Run("fresh-silent", func(t *testing.T) {
		hive, subs, _, c2 := newDriftHive(t, "c2")
		if w := promptEmbedDriftWarning(ctx, hive, "HEAD", c2, subs); w != "" {
			t.Fatalf("fresh build warned: %q", w)
		}
	})

	// Ahead: built from c2 while the hive still pins c1 (build contains the tip) ->
	// not drift -> silent.
	t.Run("ahead-silent", func(t *testing.T) {
		hive, subs, _, c2 := newDriftHive(t, "c1")
		if w := promptEmbedDriftWarning(ctx, hive, "HEAD", c2, subs); w != "" {
			t.Fatalf("ahead build warned: %q", w)
		}
	})

	// Unstamped dev build: nothing to compare -> silent.
	t.Run("dev-silent", func(t *testing.T) {
		hive, subs, _, _ := newDriftHive(t, "c2")
		if w := promptEmbedDriftWarning(ctx, hive, "HEAD", "", subs); w != "" {
			t.Fatalf("dev build warned: %q", w)
		}
	})

	// A build SHA no target's history contains cannot identify a self submodule ->
	// silent (never a false alarm).
	t.Run("unknown-sha-silent", func(t *testing.T) {
		hive, subs, _, _ := newDriftHive(t, "c2")
		phantom := strings.Repeat("a", 40)
		if w := promptEmbedDriftWarning(ctx, hive, "HEAD", phantom, subs); w != "" {
			t.Fatalf("unknown SHA warned: %q", w)
		}
	})
}

func TestShortSHA(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef01234567"
	if got := shortSHA(full); got != "0123456789ab" {
		t.Fatalf("shortSHA(full) = %q, want %q", got, "0123456789ab")
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Fatalf("shortSHA(short) = %q, want %q", got, "abc")
	}
}
