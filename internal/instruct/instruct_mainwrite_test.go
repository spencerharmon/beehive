package instruct

// Convergence-protocol coverage for `instruction update`: like the CLI verbs that
// author directly on the primary main (submodule sync/remote, plan, task), Update
// MUST SyncMainFromRemote BEFORE authoring and PublishPrimaryMain AFTER, so it
// cannot manufacture the diverged fork ff-only pullMain cannot heal
// (docs/main-convergence-protocol.md). The fork-seeded fixture asserts the fork is
// healed and the origin receives the push; the negative control proves a stubbed
// (no-op) sync manufactures the fork.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git.New(dir).Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return out
}

// newHiveWithRemote builds a repo whose main tracks a bare origin, seeded with a
// base commit so a peer can later branch a fork from it.
func newHiveWithRemote(t *testing.T) (root, origin string) {
	t.Helper()
	origin = t.TempDir()
	mustGit(t, origin, "init", "-q", "--bare", "-b", "main")
	root = t.TempDir()
	mustGit(t, root, "init", "-q", "-b", "main")
	mustGit(t, root, "config", "user.email", "t@t")
	mustGit(t, root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "base.txt")
	mustGit(t, root, "commit", "-q", "-m", "base")
	mustGit(t, root, "remote", "add", "origin", origin)
	mustGit(t, root, "push", "-q", "origin", "main")
	return root, origin
}

func seedRemoteAhead(t *testing.T, origin string) string {
	t.Helper()
	parent := t.TempDir()
	peer := filepath.Join(parent, "peer")
	mustGit(t, parent, "clone", "-q", origin, peer)
	mustGit(t, peer, "config", "user.email", "p@p")
	mustGit(t, peer, "config", "user.name", "p")
	if err := os.WriteFile(filepath.Join(peer, "PEER.txt"), []byte("peer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, peer, "add", "PEER.txt")
	mustGit(t, peer, "commit", "-q", "-m", "peer ahead")
	mustGit(t, peer, "push", "-q", "origin", "main")
	return strings.TrimSpace(mustGit(t, peer, "rev-parse", "HEAD"))
}

func TestUpdateHealsForkAndPublishes(t *testing.T) {
	root, origin := newHiveWithRemote(t)
	peer := seedRemoteAhead(t, origin)

	// The managed instruction files are absent, so Update creates + commits them,
	// exercising the primary-main author path.
	results, err := Update(context.Background(), root, false, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	created := 0
	for _, r := range results {
		if r.Action == "created" {
			created++
		}
	}
	if created == 0 {
		t.Fatal("expected Update to create managed files")
	}

	localTip := strings.TrimSpace(mustGit(t, root, "rev-parse", "main"))
	remoteTip := strings.TrimSpace(mustGit(t, origin, "rev-parse", "main"))
	if localTip != remoteTip {
		t.Fatalf("not converged: local %s != origin %s (update not published)", localTip, remoteTip)
	}
	if _, err := git.New(root).Run(context.Background(), "merge-base", "--is-ancestor", peer, "main"); err != nil {
		t.Fatalf("fork NOT healed: peer commit %s absent from local main: %v", peer, err)
	}
	logs := mustGit(t, root, "log", "--format=%s", "main")
	if !strings.Contains(logs, "beehive instruction update") {
		t.Fatalf("update commit missing from published history:\n%s", logs)
	}
}

// Negative control: with sync stubbed to a no-op, authoring on the stale base
// diverges into a fork an ff-only merge cannot heal.
func TestUpdateWithoutSyncManufacturesFork(t *testing.T) {
	ctx := context.Background()
	root, origin := newHiveWithRemote(t)
	seedRemoteAhead(t, origin)

	g := git.New(root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("stale author\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.CommitPaths(ctx, "stale author (sync stubbed)", "AGENTS.md"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := g.Fetch(ctx, "origin", "main"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := g.Run(ctx, "merge", "--ff-only", "FETCH_HEAD"); err == nil {
		t.Fatal("expected ff-only merge to FAIL on the manufactured fork, but it succeeded")
	}
}
