package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

// TestFinalizeSessionRecoversWhenMainAdvancedBeforeStub is the regression guard for
// the finalize bug that left 96% of sessions as stubs on main. It reproduces the
// exact stranding condition — main advances BEFORE the stub publish, so the stub
// publish merges main onto the session branch and HEAD becomes a merge commit above
// the stub carrying peer files. The old `reset --soft stub` + pathspec commit left
// those peer files as staged residue, and finalize's publish `merge main` was then
// refused ("local changes would be overwritten"), stranding the transcript on the
// branch. The fix rebuilds the tree (reset --hard stub + checkout tip -- rel) so the
// publish merges cleanly and the transcript reaches main.
func TestFinalizeSessionRecoversWhenMainAdvancedBeforeStub(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	if err := os.MkdirAll(filepath.Join(sm, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\n"), 0o644)
	ctx := context.Background()
	if err := g.Commit(ctx, "seed"); err != nil {
		t.Fatal(err)
	}

	sid := "s1"
	rel := filepath.Join("submodules", "sm", "sessions", sid+".md")
	branch := "bee-x-session"

	sessPath := filepath.Join(root, ".worktrees", branch)
	if err := g.WorktreeAdd(ctx, sessPath, branch, "main"); err != nil {
		t.Fatal(err)
	}
	sessGit := git.New(sessPath)

	// advanceMain lands a peer file on main via its own worktree, exactly as a
	// concurrent honeybee would, so the session branch is genuinely behind.
	advanceMain := func(name, content string) {
		wtp := filepath.Join(root, ".worktrees", "adv-"+name)
		if err := g.WorktreeAdd(ctx, wtp, "adv-"+name, "main"); err != nil {
			t.Fatal(err)
		}
		awg := git.New(wtp)
		os.WriteFile(filepath.Join(wtp, name), []byte(content), 0o644)
		if err := awg.CommitPaths(ctx, "advance "+name, name); err != nil {
			t.Fatal(err)
		}
		if err := awg.PublishToMain(ctx, ""); err != nil {
			t.Fatal(err)
		}
		_ = g.WorktreeRemove(ctx, wtp)
	}

	// Peer work lands BEFORE the stub publish → the stub publish must merge main.
	advanceMain("peer.txt", "peer work\n")

	// startSession-equivalent: commit the stub, capture the squash base, then publish
	// — which merges the advanced main onto the session branch (HEAD := merge commit).
	// Mirror startSession's MkdirAll: the sessions dir is untracked (empty) on main,
	// so it does not exist in this fresh worktree until we create it.
	if err := os.MkdirAll(filepath.Dir(filepath.Join(sessPath, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessPath, rel), []byte(repo.SessionStub(branch)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sessGit.CommitPaths(ctx, "session: start", rel); err != nil {
		t.Fatal(err)
	}
	stubCommit, err := sessGit.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := sessGit.PublishToMain(ctx, ""); err != nil {
		t.Fatalf("stub publish: %v", err)
	}
	// The setup MUST have produced a merge commit above the stub, else there is no
	// residue to strand and the test would prove nothing.
	if head, _ := sessGit.RevParse(ctx, "HEAD"); head == stubCommit {
		t.Fatal("setup did not create a merge above the stub; residue condition not reproduced")
	}

	// Stream the final transcript onto the session branch.
	transcript := "# session " + sid + "\n\nreal transcript body\n"
	if err := os.WriteFile(filepath.Join(sessPath, rel), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sessGit.CommitPaths(ctx, "session: stream", rel); err != nil {
		t.Fatal(err)
	}

	// More peer work lands, so finalize's publish push is non-fast-forward and must
	// `merge main` — the exact step the stranded residue made git refuse.
	advanceMain("peer2.txt", "more peer work\n")

	r := &Runner{
		SessionGit:     sessGit,
		SessionPublish: func(ctx context.Context) error { return sessGit.PublishToMain(ctx, "") },
	}
	if err := r.finalizeSession(ctx, sid, rel, stubCommit); err != nil {
		t.Fatalf("finalizeSession returned error (the regression): %v", err)
	}

	// The transcript is now durable on main, not a stub.
	body, err := g.Show(ctx, "main", rel)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.ParseSessionStub(body); ok {
		t.Fatalf("transcript not promoted; main still a stub: %q", body)
	}
	if !strings.Contains(body, "real transcript body") {
		t.Fatalf("main missing the transcript body: %q", body)
	}
	// Peer work is not clobbered by the finalize merge.
	for _, f := range []string{"peer.txt", "peer2.txt"} {
		if _, err := g.Show(ctx, "main", f); err != nil {
			t.Errorf("peer file %s lost from main after finalize: %v", f, err)
		}
	}
}
