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

// plantStub writes a session stub naming branch at submodules/sm/sessions/<sid>.md
// and returns the repo-relative path. The stub is the exact state main carries
// while a session streams (repo.SessionStub), i.e. what a failed finalize leaves.
func plantStub(t *testing.T, root, sid, branch string) string {
	t.Helper()
	rel := filepath.Join("submodules", "sm", "sessions", sid+".md")
	if err := os.WriteFile(filepath.Join(root, rel), []byte(repo.SessionStub(branch)), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

// branchWithTranscript reproduces the branch state a failed finalize leaves: a
// session branch cut from main (which holds the stub) whose tip commit replaces
// the stub at rel with the real transcript. The worktree is removed, leaving only
// the branch ref — exactly what the runner keeps when SessionPublished is false.
func branchWithTranscript(t *testing.T, g *git.Repo, root, branch, rel, body string) {
	t.Helper()
	ctx := context.Background()
	wt := filepath.Join(root, ".worktrees", branch)
	if err := g.WorktreeAdd(ctx, wt, branch, "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.New(wt).CommitPaths(ctx, "session: final "+branch, rel); err != nil {
		t.Fatalf("commit transcript on %s: %v", branch, err)
	}
	if err := g.WorktreeRemove(ctx, wt); err != nil {
		t.Fatal(err)
	}
}

// branchStubOnly creates a session branch whose tip carries no transcript (its tip
// is main, so rel is still a stub) — a session that recorded nothing recoverable.
func branchStubOnly(t *testing.T, g *git.Repo, root, branch string) {
	t.Helper()
	ctx := context.Background()
	wt := filepath.Join(root, ".worktrees", branch)
	if err := g.WorktreeAdd(ctx, wt, branch, "main"); err != nil {
		t.Fatal(err)
	}
	if err := g.WorktreeRemove(ctx, wt); err != nil {
		t.Fatal(err)
	}
}

// TestSweepSessionTranscripts drives the sweep across all four cases at once:
// a recoverable stub (branch present with a real transcript), a gone-branch stub,
// a stub whose branch tip is itself a stub, and an already-final transcript. Only
// the recoverable one is promoted; the rest are reported or ignored, never
// fabricated. A second run proves idempotency.
func TestSweepSessionTranscripts(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	if err := os.MkdirAll(filepath.Join(sm, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\n"), 0o644)
	ctx := context.Background()

	relRecover := plantStub(t, root, "s-recover", "s-recover-session")
	relGone := plantStub(t, root, "s-gone", "s-gone-session")
	relStubTip := plantStub(t, root, "s-stubtip", "s-stubtip-session")
	relReal := filepath.Join("submodules", "sm", "sessions", "s-real.md")
	os.WriteFile(filepath.Join(root, relReal), []byte("# session s-real\n\nalready final\n"), 0o644)
	if err := g.Commit(ctx, "seed sessions"); err != nil {
		t.Fatal(err)
	}

	transcript := "# session s-recover\n\nthe real transcript body\n"
	branchWithTranscript(t, g, root, "s-recover-session", relRecover, transcript)
	branchStubOnly(t, g, root, "s-stubtip-session")
	// s-gone-session is never created — its stub is unrecoverable.

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	res, err := SweepSessionTranscripts(ctx, g, subs, "")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(res.Recovered) != 1 || res.Recovered[0] != relRecover {
		t.Fatalf("Recovered = %v, want [%s]", res.Recovered, relRecover)
	}
	if len(res.GoneBranch) != 1 || res.GoneBranch[0] != relGone {
		t.Fatalf("GoneBranch = %v, want [%s]", res.GoneBranch, relGone)
	}
	if len(res.NoTranscript) != 1 || res.NoTranscript[0] != relStubTip {
		t.Fatalf("NoTranscript = %v, want [%s]", res.NoTranscript, relStubTip)
	}

	// The recoverable session now carries its real transcript on main, byte-faithful.
	body, err := g.Show(ctx, "main", relRecover)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.ParseSessionStub(body); ok {
		t.Fatalf("recovered session still a stub on main: %q", body)
	}
	if !strings.Contains(body, "the real transcript body") {
		t.Fatalf("recovered main body missing transcript: %q", body)
	}
	// The unrecoverable and already-final ones are untouched.
	for _, rel := range []string{relGone, relStubTip} {
		b, _ := g.Show(ctx, "main", rel)
		if _, ok := repo.ParseSessionStub(b); !ok {
			t.Fatalf("%s should remain a stub on main, got %q", rel, b)
		}
	}
	if b, _ := g.Show(ctx, "main", relReal); !strings.Contains(b, "already final") {
		t.Fatalf("already-final session was altered: %q", b)
	}

	// Idempotent: nothing new to recover; the two unrecoverable stubs still report.
	res2, err := SweepSessionTranscripts(ctx, g, subs, "")
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(res2.Recovered) != 0 {
		t.Fatalf("second sweep recovered %v, want none", res2.Recovered)
	}
	if len(res2.GoneBranch) != 1 || len(res2.NoTranscript) != 1 {
		t.Fatalf("second sweep reports = %+v, want unchanged gone/no-transcript", res2)
	}
}

// TestSweepSkipsLiveSessionBranch proves the sweep never races an in-flight
// session: a stub whose branch still has a checked-out worktree is left for that
// session's own finalize, not promoted here.
func TestSweepSkipsLiveSessionBranch(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	if err := os.MkdirAll(filepath.Join(sm, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\n"), 0o644)
	ctx := context.Background()

	rel := plantStub(t, root, "s-live", "s-live-session")
	if err := g.Commit(ctx, "seed"); err != nil {
		t.Fatal(err)
	}

	// Branch has a real transcript, but its worktree stays checked out (in-flight).
	wt := filepath.Join(root, ".worktrees", "s-live-session")
	if err := g.WorktreeAdd(ctx, wt, "s-live-session", "main"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, rel), []byte("# session s-live\n\nlive body\n"), 0o644)
	if err := git.New(wt).CommitPaths(ctx, "session: streaming", rel); err != nil {
		t.Fatal(err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	res, err := SweepSessionTranscripts(ctx, g, subs, "")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !res.Empty() {
		t.Fatalf("live session must be skipped entirely, got %+v", res)
	}
	body, _ := g.Show(ctx, "main", rel)
	if _, ok := repo.ParseSessionStub(body); !ok {
		t.Fatalf("live session stub must stay on main untouched, got %q", body)
	}
}
