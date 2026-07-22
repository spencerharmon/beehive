package main

// These tests lock the convergence-protocol invariant for the CLI verbs that
// author a commit DIRECTLY on the primary main: `submodule remote`, `plan
// archive`, and `task human` (the `instruction update` verb is covered by an
// equivalent test in internal/instruct). Each MUST call SyncMainFromRemote
// BEFORE authoring and PublishPrimaryMain AFTER, mirroring `submodule sync`
// (syncSubmodule) exactly, so none can manufacture the diverged fork that
// ff-only pullMain cannot heal (docs/main-convergence-protocol.md).
//
// Per verb: a fork-seeded fixture (a peer pushes main ahead on the origin
// between the verb's setup and its run) asserts the fork is HEALED (the peer
// commit survives in local history) and the origin RECEIVES the push (local and
// origin main converge on the same tip that carries the verb's commit). A shared
// negative control proves that WITHOUT the sync-before step (a stubbed no-op
// sync) the very same setup manufactures a fork an ff-only merge cannot heal.

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

func writeFileMW(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newHive builds a hive repo whose primary main tracks a bare `origin`, with
// AGENTS.md committed so findRoot resolves it. Returns the hive root and the
// bare origin path.
func newHive(t *testing.T) (root, origin string) {
	t.Helper()
	origin = t.TempDir()
	mustGit(t, origin, "init", "-q", "--bare", "-b", "main")
	root = t.TempDir()
	mustGit(t, root, "init", "-q", "-b", "main")
	mustGit(t, root, "config", "user.email", "t@t")
	mustGit(t, root, "config", "user.name", "t")
	writeFileMW(t, root, "AGENTS.md", "hive\n")
	mustGit(t, root, "add", "AGENTS.md")
	mustGit(t, root, "commit", "-q", "-m", "base")
	mustGit(t, root, "remote", "add", "origin", origin)
	mustGit(t, root, "push", "-q", "origin", "main")
	return root, origin
}

// commitPush commits every tracked change under the hive root and publishes it to
// origin/main, extending the shared base a peer will later branch from.
func commitPush(t *testing.T, root, msg string) {
	t.Helper()
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", msg)
	mustGit(t, root, "push", "-q", "origin", "main")
}

// seedRemoteAhead makes a fresh clone push a commit to origin/main so the hive's
// local main is now BEHIND origin. Returns the peer commit sha; a verb that
// authors on the stale local main WITHOUT syncing first would fork against it.
func seedRemoteAhead(t *testing.T, origin string) string {
	t.Helper()
	parent := t.TempDir()
	peer := filepath.Join(parent, "peer")
	mustGit(t, parent, "clone", "-q", origin, peer)
	mustGit(t, peer, "config", "user.email", "p@p")
	mustGit(t, peer, "config", "user.name", "p")
	writeFileMW(t, peer, "PEER.txt", "peer ahead\n")
	mustGit(t, peer, "add", "PEER.txt")
	mustGit(t, peer, "commit", "-q", "-m", "peer ahead")
	mustGit(t, peer, "push", "-q", "origin", "main")
	return strings.TrimSpace(mustGit(t, peer, "rev-parse", "HEAD"))
}

// assertHealedAndPushed asserts the fork was healed (peerSha is an ancestor of
// local main) and the origin received the push (local and origin main converge on
// the same tip, whose history carries wantMsg).
func assertHealedAndPushed(t *testing.T, root, origin, peerSha, wantMsg string) {
	t.Helper()
	localTip := strings.TrimSpace(mustGit(t, root, "rev-parse", "main"))
	remoteTip := strings.TrimSpace(mustGit(t, origin, "rev-parse", "main"))
	if localTip != remoteTip {
		t.Fatalf("not converged: local main %s != origin main %s (bump not published)", localTip, remoteTip)
	}
	if _, err := git.New(root).Run(context.Background(), "merge-base", "--is-ancestor", peerSha, "main"); err != nil {
		t.Fatalf("fork NOT healed: peer commit %s is not in local main history: %v", peerSha, err)
	}
	logs := mustGit(t, root, "log", "--format=%s", "main")
	if !strings.Contains(logs, wantMsg) {
		t.Fatalf("verb commit %q missing from published main history:\n%s", wantMsg, logs)
	}
}

// inDir runs fn with the process cwd set to dir (findRoot ascends from cwd).
func inDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	fn()
}

const donePlan = `<!-- Beehive-ROI: deadbeef -->
# Plan

## t1 [DONE] <!-- attempts=0 deps= weight=32 -->
Add a thing.
Files: internal/x/x.go.
Doc: docs/tasks/t1.md
Accept: unit tests.
Impl (bee-t1, commit c21a4f0, pushed origin): closed the gap; added --prune.
Tests green; vet clean; static build OK.
Review (approved, beehive-123): verified branch vs task + ROI. Accept met.
Merged; pointer bumped.
`

const todoPlan = `<!-- Beehive-ROI: deadbeef -->
# Plan

## t1 [TODO] <!-- attempts=0 deps= weight=32 -->
Do a thing.
Files: internal/x/x.go.
Doc: docs/tasks/t1.md
Accept: unit tests.
`

func TestPlanArchiveHealsForkAndPublishes(t *testing.T) {
	root, origin := newHive(t)
	writeFileMW(t, root, "submodules/mysm/PLAN.md", donePlan)
	commitPush(t, root, "seed plan")
	peer := seedRemoteAhead(t, origin)

	inDir(t, root, func() {
		c := planCmd()
		c.SetArgs([]string{"archive", "mysm"})
		if err := c.Execute(); err != nil {
			t.Fatalf("plan archive: %v", err)
		}
	})
	assertHealedAndPushed(t, root, origin, peer, "plan: archive DONE narrative for mysm")
}

func TestTaskHumanHealsForkAndPublishes(t *testing.T) {
	root, origin := newHive(t)
	writeFileMW(t, root, "submodules/mysm/PLAN.md", todoPlan)
	commitPush(t, root, "seed plan")
	peer := seedRemoteAhead(t, origin)

	inDir(t, root, func() {
		c := taskHumanCmd()
		c.SetArgs([]string{"mysm", "t1", "--category", "secret", "--reason", "need a token"})
		if err := c.Execute(); err != nil {
			t.Fatalf("task human: %v", err)
		}
	})
	assertHealedAndPushed(t, root, origin, peer, "plan: request human for t1")
}

func TestSubmoduleRemoteHealsForkAndPublishes(t *testing.T) {
	root, origin := newHive(t)

	// A real registered submodule for SetRemoteURL to repoint.
	smOrigin := t.TempDir()
	mustGit(t, smOrigin, "init", "-q", "--bare", "-b", "main")
	sp := t.TempDir()
	smSeed := filepath.Join(sp, "s")
	mustGit(t, sp, "clone", "-q", smOrigin, smSeed)
	mustGit(t, smSeed, "config", "user.email", "s@s")
	mustGit(t, smSeed, "config", "user.name", "s")
	writeFileMW(t, smSeed, "README.md", "sm\n")
	mustGit(t, smSeed, "add", "README.md")
	mustGit(t, smSeed, "commit", "-q", "-m", "sm base")
	mustGit(t, smSeed, "push", "-q", "origin", "main")

	mustGit(t, root, "-c", "protocol.file.allow=always", "submodule", "add", smOrigin, "submodules/mysm/repo")
	commitPush(t, root, "add submodule mysm")
	peer := seedRemoteAhead(t, origin)

	inDir(t, root, func() {
		c := submoduleRemoteCmd()
		c.SetArgs([]string{"mysm", "https://example.com/new.git"})
		if err := c.Execute(); err != nil {
			t.Fatalf("submodule remote: %v", err)
		}
	})
	assertHealedAndPushed(t, root, origin, peer, "submodule remote: mysm")
}

// TestMainWriteWithoutSyncManufacturesFork is the negative control: with the
// sync-before step stubbed to a no-op, authoring a commit directly on the stale
// local main (exactly what these verbs did BEFORE this fix) diverges from
// origin/main into a fork that an ff-only merge — the reconciliation pullMain
// uses — cannot heal. This is the failure the sync-before wrapper prevents.
func TestMainWriteWithoutSyncManufacturesFork(t *testing.T) {
	ctx := context.Background()
	root, origin := newHive(t)
	_ = origin
	seedRemoteAhead(t, origin)

	g := git.New(root)
	// Stubbed sync: skip SyncMainFromRemote entirely, author on the stale base.
	writeFileMW(t, root, "change.txt", "local edit\n")
	if err := g.CommitPaths(ctx, "stale author (sync stubbed)", "change.txt"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := g.Fetch(ctx, "origin", "main"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := g.Run(ctx, "merge", "--ff-only", "FETCH_HEAD"); err == nil {
		t.Fatal("expected ff-only merge to FAIL on the manufactured fork, but it succeeded")
	}
}
