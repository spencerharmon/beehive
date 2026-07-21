package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupPointerBumpRepo builds a beehive root whose submodule "beehive" has a
// checkout at submodules/beehive/repo wired to a bare origin, plus a HEAD commit.
// It returns the root, the submodule checkout dir, and the origin path.
func setupPointerBumpRepo(t *testing.T) (root, subDir, origin string) {
	t.Helper()
	ctx := context.Background()
	root = t.TempDir()
	gitDo(t, root, "init", "-q")
	gitDo(t, root, "config", "user.email", "t@t")
	gitDo(t, root, "config", "user.name", "t")
	writeF(t, filepath.Join(root, "AGENTS.md"), "agents")

	// A bare origin the submodule tracks.
	origin = t.TempDir()
	gitDo(t, origin, "init", "-q", "--bare", "-b", "main")

	// The submodule checkout cloned from origin, with a seed commit on main.
	subDir = filepath.Join(root, "submodules", "beehive", "repo")
	gitDo(t, root, "clone", "-q", origin, subDir)
	gitDo(t, subDir, "config", "user.email", "t@t")
	gitDo(t, subDir, "config", "user.name", "t")
	writeF(t, filepath.Join(subDir, "f"), "v1\n")
	gitDo(t, subDir, "add", "-A")
	gitDo(t, subDir, "commit", "-qm", "seed")
	gitDo(t, subDir, "push", "-q", "origin", "main")

	// .gitmodules records the tracked branch (defaults to main anyway).
	writeF(t, filepath.Join(root, ".gitmodules"),
		"[submodule \"submodules/beehive/repo\"]\n\tpath = submodules/beehive/repo\n\turl = "+origin+"\n\tbranch = main\n")
	gitDo(t, root, "add", "AGENTS.md", ".gitmodules")
	gitDo(t, root, "commit", "-qm", "init")
	_ = ctx
	return root, subDir, origin
}

func writeF(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPointerBumpRefusesUnpushedCommit is the core regression: a commit that was
// never pushed to the submodule origin must NOT be recorded as the gitlink — the
// bump is refused with an error naming the unreachable sha.
func TestPointerBumpRefusesUnpushedCommit(t *testing.T) {
	ctx := context.Background()
	root, subDir, _ := setupPointerBumpRepo(t)
	subGit := gitOut

	// Make a local-only commit in the submodule worktree (the defect scenario:
	// the push was skipped/failed, so it never reached origin).
	writeF(t, filepath.Join(subDir, "g"), "local only\n")
	gitDo(t, subDir, "add", "-A")
	gitDo(t, subDir, "commit", "-qm", "unpushed work")
	sha := strings.TrimSpace(subGit(t, subDir, "rev-parse", "HEAD"))

	err := pointerBumpSubmodule(ctx, root, "beehive", "HEAD")
	if err == nil {
		t.Fatal("pointer bump to an unpushed commit must be refused")
	}
	if !strings.Contains(err.Error(), sha) {
		t.Fatalf("refusal must name the unreachable sha %s, got: %v", sha, err)
	}
	// The gitlink index entry must be unchanged (still the seed commit).
	if strings.Contains(gitOut(t, root, "diff", "--cached", "--", "submodules/beehive/repo"), sha) {
		t.Fatalf("refused bump must not stage the unreachable sha")
	}
}

// TestPointerBumpAcceptsPushedCommit is the unregressed success path: once the
// commit is on origin, the bump succeeds and stages the gitlink at that sha.
func TestPointerBumpAcceptsPushedCommit(t *testing.T) {
	ctx := context.Background()
	root, subDir, _ := setupPointerBumpRepo(t)

	writeF(t, filepath.Join(subDir, "g"), "shared\n")
	gitDo(t, subDir, "add", "-A")
	gitDo(t, subDir, "commit", "-qm", "shared work")
	gitDo(t, subDir, "push", "-q", "origin", "main")
	sha := strings.TrimSpace(gitOut(t, subDir, "rev-parse", "HEAD"))

	if err := pointerBumpSubmodule(ctx, root, "beehive", "HEAD"); err != nil {
		t.Fatalf("pointer bump of a pushed commit must succeed: %v", err)
	}
	// ls-files reports the staged gitlink at the pushed sha.
	out := gitOut(t, root, "ls-files", "-s", "--", "submodules/beehive/repo")
	if !strings.Contains(out, sha) {
		t.Fatalf("gitlink not staged at pushed sha %s:\n%s", sha, out)
	}
}
