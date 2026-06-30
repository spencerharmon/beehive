package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/repo"
)

// mustGit runs a git command in dir and fails the test on error, returning
// trimmed stdout+stderr. The pull tests need real git history (a bare origin,
// fast-forwards, a divergent local commit), so they shell out exactly like the
// production git wrapper rather than faking refs.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remoteHive builds a beehive repo (the one the Server serves) backed by a bare
// origin, with a single submodule "alpha" carrying a session transcript at
// sessions/<branch>.md. It returns the Server, the work-tree root, and the bare
// origin path. The work tree has origin as its remote and main pushed to it, so
// the viewer's pullRemote can fast-forward from commits other clones push.
func remoteHive(t *testing.T, branch, body string) (*Server, string, string) {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, base, "init", "--bare", "-b", "main", origin)
	if err := repo.Init(work); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "init", "-b", "main")
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	sm := filepath.Join(work, "submodules", "alpha")
	if err := os.MkdirAll(filepath.Join(sm, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sm, repo.ROIFile), []byte("# alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sm, "sessions", branch+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "add", "-A")
	mustGit(t, work, "commit", "-q", "-m", "seed")
	mustGit(t, work, "remote", "add", "origin", origin)
	mustGit(t, work, "push", "-q", "-u", "origin", "main")

	r, err := repo.Open(work)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(r, config.Defaults(work))
	if err != nil {
		t.Fatal(err)
	}
	return s, work, origin
}

// pushSessionTurn simulates another host's producer: it clones origin, rewrites
// the session transcript (an appended turn), commits, and pushes to origin/main —
// the periodic session commit the viewer must pick up.
func pushSessionTurn(t *testing.T, origin, branch, newBody string) {
	t.Helper()
	prod := t.TempDir()
	mustGit(t, prod, "clone", "-q", origin, ".")
	mustGit(t, prod, "config", "user.email", "p@p")
	mustGit(t, prod, "config", "user.name", "p")
	if err := os.WriteFile(filepath.Join(prod, "submodules", "alpha", "sessions", branch+".md"), []byte(newBody), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, prod, "commit", "-q", "-am", "session turn")
	mustGit(t, prod, "push", "-q", "origin", "main")
}

// TestViewerPullPicksUpRemoteTurns is the core of remote-host-session-view: a
// fake origin receives a periodic session commit from another host, the viewer's
// pullRemote fast-forwards local main, and the rendered session pane reflects the
// new turn — with the last-pulled staleness surfaced.
func TestViewerPullPicksUpRemoteTurns(t *testing.T) {
	branch := "bee-remote"
	s, _, origin := remoteHive(t, branch, "# session\n\nturn one\n")
	base := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return base }

	// Another host appends a turn and pushes it to origin.
	pushSessionTurn(t, origin, branch, "# session\n\nturn one\n\nturn two\n")

	// Before pulling, the viewer's working tree has not seen the remote turn.
	if b := get(t, s, "/submodule/alpha/session/"+branch+"/body").Body.String(); strings.Contains(b, "turn two") {
		t.Fatalf("viewer saw the remote turn before pulling — setup wrong:\n%s", b)
	}

	// The viewer pull fast-forwards local main; the pane now reflects the new turn.
	if err := s.pullRemote(context.Background()); err != nil {
		t.Fatalf("pullRemote: %v", err)
	}
	body := get(t, s, "/submodule/alpha/session/"+branch+"/body").Body.String()
	if !strings.Contains(body, "turn two") {
		t.Fatalf("viewer pane did not pick up the remote turn after pull:\n%s", body)
	}
	if !strings.Contains(body, "following remote") {
		t.Fatalf("session pane missing the remote-following staleness banner:\n%s", body)
	}

	// A successful pull was recorded; staleness grows from that instant.
	ps := s.pullStatusAt(base.Add(3*time.Second), true)
	if !ps.Ran || ps.Err != "" || ps.Ago != "3s" {
		t.Fatalf("pull status after success = %+v, want Ran, no err, Ago=3s", ps)
	}
}

// TestViewerPullFFOnlyDivergence proves the ff-only contract: when local main has
// diverged from origin (a local edit plus a remote turn), the viewer pull errors
// instead of merging — it makes NO merge commit, leaves HEAD and the tree exactly
// as they were, keeps the local copy renderable, and surfaces the failure.
func TestViewerPullFFOnlyDivergence(t *testing.T) {
	branch := "bee-div"
	s, work, origin := remoteHive(t, branch, "# session\n\nbase turn\n")

	// Another host advances origin/main.
	pushSessionTurn(t, origin, branch, "# session\n\nbase turn\n\norigin turn\n")

	// The viewer makes its own divergent local commit without pulling first.
	if err := os.WriteFile(filepath.Join(work, "submodules", "alpha", "sessions", branch+".md"),
		[]byte("# session\n\nbase turn\n\nlocal turn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "commit", "-q", "-am", "local divergent turn")
	headBefore := mustGit(t, work, "rev-parse", "HEAD")
	countBefore := mustGit(t, work, "rev-list", "--count", "main")

	err := s.pullRemote(context.Background())
	if err == nil {
		t.Fatalf("divergent ff-only pull should error, got nil")
	}
	if h := mustGit(t, work, "rev-parse", "HEAD"); h != headBefore {
		t.Fatalf("local HEAD moved on a failed ff-only pull: %s -> %s", headBefore, h)
	}
	if c := mustGit(t, work, "rev-list", "--count", "main"); c != countBefore {
		t.Fatalf("ff-only pull created a commit (count %s -> %s) — must never merge", countBefore, c)
	}
	if st := mustGit(t, work, "status", "--porcelain"); st != "" {
		t.Fatalf("failed pull left the tree dirty / mid-merge:\n%s", st)
	}

	// The local copy is intact and still renders the LOCAL turn, never origin's.
	body := get(t, s, "/submodule/alpha/session/"+branch+"/body").Body.String()
	if !strings.Contains(body, "local turn") || strings.Contains(body, "origin turn") {
		t.Fatalf("viewer should keep its own copy after a failed pull:\n%s", body)
	}
	if !strings.Contains(body, "pull failed") {
		t.Fatalf("divergence not surfaced in the session pane:\n%s", body)
	}
	if ps := s.pullStatusAt(time.Now(), true); ps.Err == "" {
		t.Fatalf("pull status should carry the ff-only error, got %+v", ps)
	}
}

// TestViewerPullNoRemoteNoop locks the gate: a single-host repo (no remote) has
// no off-box runs to follow, so pullRemote is a no-op (no error) and the session
// pane shows no remote-following banner.
func TestViewerPullNoRemoteNoop(t *testing.T) {
	s, _ := setup(t) // repo.Init + git init, NO remote
	if err := s.pullRemote(context.Background()); err != nil {
		t.Fatalf("pullRemote on a no-remote repo should be a no-op, got %v", err)
	}
	if ps := s.pullStatusAt(time.Now(), false); ps.Remote {
		t.Fatalf("no-remote repo must not report remote-following: %+v", ps)
	}
	if b := get(t, s, "/submodule/alpha/session/bee-x/body").Body.String(); strings.Contains(b, "following remote") {
		t.Fatalf("single-host pane must not show a remote-following banner:\n%s", b)
	}
}
