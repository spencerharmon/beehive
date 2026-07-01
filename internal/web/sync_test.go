package web

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/repo"
)

// runGit runs git in dir (cwd when dir==""), failing the test on error. It is
// named runGit, not git, because web.go imports the internal git package into
// this same package scope under the identifier `git`.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	if dir != "" {
		c.Dir = dir
	}
	// Hermetic identity so commits don't depend on the machine's git config.
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %q): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newHive stands up the distributed topology the follower pull exists for: a
// bare origin, a producer beehive repo (the off-box honeybee's host) that pushes
// to it, and a viewer clone wrapped in a Server (the beehived that never authors,
// only follows). It returns the viewer server plus the three repo paths.
func newHive(t *testing.T) (s *Server, viewer, prod, origin string) {
	t.Helper()
	base := t.TempDir()

	origin = filepath.Join(base, "origin.git")
	runGit(t, base, "init", "--bare", "-b", "main", origin)

	// repo.Init already `git init`s and checks out main, and installs AGENTS.md
	// (which repo.Open requires) — so the clone is a valid beehive repo.
	prod = filepath.Join(base, "prod")
	if err := repo.Init(prod); err != nil {
		t.Fatalf("init producer: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(prod, "submodules", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prod, "submodules", "alpha", repo.ROIFile), []byte("# alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, prod, "add", "-A")
	runGit(t, prod, "commit", "-m", "seed")
	runGit(t, prod, "remote", "add", "origin", origin)
	runGit(t, prod, "push", "-u", "origin", "main")

	viewer = filepath.Join(base, "viewer")
	runGit(t, base, "clone", origin, viewer)
	r, err := repo.Open(viewer)
	if err != nil {
		t.Fatalf("open viewer: %v", err)
	}
	s, err = New(r, config.Defaults(viewer))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return s, viewer, prod, origin
}

// TestSyncViewStates locks the follower-state projection the UI reads: no-remote
// hives render nothing; a repo that has never synced, one whose last sync is
// overdue, a diverged local main, and an unreachable remote each flag stale with
// a distinct, honest note; a recent successful sync is fresh with no note.
func TestSyncViewStates(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	boom := errors.New("boom")
	cases := []struct {
		name       string
		hasRemote  bool
		everSynced bool
		lastSynced time.Time
		lastErr    error
		fetched    bool
		wantRemote bool
		wantSynced bool
		wantStale  bool
		noteHas    string
	}{
		{name: "no remote", hasRemote: false, wantRemote: false},
		{name: "never synced", hasRemote: true, everSynced: false,
			wantRemote: true, wantSynced: false, wantStale: true, noteHas: "not yet synced"},
		{name: "fresh", hasRemote: true, everSynced: true, lastSynced: now.Add(-2 * time.Second),
			wantRemote: true, wantSynced: true, wantStale: false},
		{name: "overdue", hasRemote: true, everSynced: true, lastSynced: now.Add(-2 * syncStaleAfter),
			wantRemote: true, wantSynced: true, wantStale: true, noteHas: "overdue"},
		{name: "diverged", hasRemote: true, everSynced: true, lastSynced: now.Add(-time.Second), lastErr: boom, fetched: true,
			wantRemote: true, wantSynced: true, wantStale: true, noteHas: "diverged"},
		{name: "unreachable", hasRemote: true, everSynced: true, lastSynced: now.Add(-time.Second), lastErr: boom, fetched: false,
			wantRemote: true, wantSynced: true, wantStale: true, noteHas: "unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := &remoteSync{
				hasRemote:  c.hasRemote,
				everSynced: c.everSynced,
				lastSynced: c.lastSynced,
				lastErr:    c.lastErr,
				fetched:    c.fetched,
			}
			got := rs.view(now, syncStaleAfter)
			if got.Remote != c.wantRemote {
				t.Fatalf("Remote=%v, want %v", got.Remote, c.wantRemote)
			}
			if !c.wantRemote {
				return
			}
			if got.Synced != c.wantSynced {
				t.Errorf("Synced=%v, want %v", got.Synced, c.wantSynced)
			}
			if got.Stale != c.wantStale {
				t.Errorf("Stale=%v, want %v (note %q)", got.Stale, c.wantStale, got.Note)
			}
			if c.noteHas != "" && !strings.Contains(got.Note, c.noteHas) {
				t.Errorf("Note=%q, want it to contain %q", got.Note, c.noteHas)
			}
			if !c.wantStale && got.Note != "" {
				t.Errorf("a fresh sync must have no note, got %q", got.Note)
			}
		})
	}
}

// TestSyncRemoteFollowsOrigin proves the core follower behavior: an off-box
// honeybee's session materializes in a viewer that shares no filesystem with it,
// purely by pulling the beehive repo's main. The producer streams a transcript to
// an isolated branch and plants a STUB on main; before the follower pull the
// viewer sees no session, and after it the session lists and its live transcript
// renders (resolved by fetching the stream branch). A later turn pushed to the
// branch shows up without another main pull, and the follower reports fresh.
func TestSyncRemoteFollowsOrigin(t *testing.T) {
	ctx := context.Background()
	s, _, prod, _ := newHive(t)

	prodSess := filepath.Join(prod, "submodules", "alpha", "sessions")
	writeProd := func(content string) {
		if err := os.MkdirAll(prodSess, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prodSess, "sess1.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Off-box honeybee: stream the transcript to an isolated branch, push it.
	runGit(t, prod, "checkout", "-b", "bee-strm-1")
	writeProd("turn one output\n")
	runGit(t, prod, "add", "-A")
	runGit(t, prod, "commit", "-m", "stream turn 1")
	runGit(t, prod, "push", "origin", "bee-strm-1")
	// ...and plant the STUB on main so the session shows up in the list at all.
	runGit(t, prod, "checkout", "main")
	writeProd(repo.SessionStub("bee-strm-1"))
	runGit(t, prod, "add", "-A")
	runGit(t, prod, "commit", "-m", "stub sess1")
	runGit(t, prod, "push", "origin", "main")

	sm, err := s.submodule("alpha")
	if err != nil {
		t.Fatalf("resolve submodule: %v", err)
	}

	// Before the follower pull, the off-box session is invisible to the viewer.
	if infos := s.sessionInfos(ctx, sm.SessionsDir(), time.Now()); len(infos) != 0 {
		t.Fatalf("pre-sync: want no sessions, got %d (%+v)", len(infos), infos)
	}

	s.SyncRemote(ctx)

	// After the pull, the stub landed and the session is listed.
	if !hasSession(s.sessionInfos(ctx, sm.SessionsDir(), time.Now()), "sess1") {
		t.Fatalf("post-sync: session sess1 should be listed")
	}
	// Its body resolves through the stream branch (fetched on demand).
	if b := get(t, s, "/submodule/alpha/session/sess1/body").Body.String(); !strings.Contains(b, "turn one output") {
		t.Fatalf("body should render the streamed transcript, got: %q", b)
	}

	// A later turn pushed to the stream branch is followed WITHOUT another main
	// pull — the per-branch read fetches it directly.
	runGit(t, prod, "checkout", "bee-strm-1")
	writeProd("turn one output\nturn two output\n")
	runGit(t, prod, "add", "-A")
	runGit(t, prod, "commit", "-m", "stream turn 2")
	runGit(t, prod, "push", "origin", "bee-strm-1")
	runGit(t, prod, "checkout", "main")

	if b := get(t, s, "/submodule/alpha/session/sess1/body").Body.String(); !strings.Contains(b, "turn two output") {
		t.Fatalf("body should follow later turns on the stream branch, got: %q", b)
	}

	if st := s.sync.view(time.Now(), syncStaleAfter); !st.Remote || !st.Synced || st.Stale {
		t.Fatalf("after a clean pull the follower should read fresh, got %+v", st)
	}
}

// TestSyncRemoteDivergenceFallsBackToFetch proves the follower stays a follower:
// when the viewer's local main has diverged from a remote that also advanced, the
// ff-only pull cannot apply, so SyncRemote must NOT merge or reset local main —
// it degrades to a plain fetch (advancing the remote-tracking ref the per-branch
// reads use) and surfaces the divergence as staleness.
func TestSyncRemoteDivergenceFallsBackToFetch(t *testing.T) {
	ctx := context.Background()
	s, viewer, prod, _ := newHive(t)

	// A clean first sync so everSynced=true; otherwise view() would report "not
	// yet synced" and the divergence note would never surface.
	s.SyncRemote(ctx)
	if st := s.sync.view(time.Now(), syncStaleAfter); !st.Synced || st.Stale {
		t.Fatalf("first sync should be clean, got %+v", st)
	}

	// The viewer commits locally (never pushed): local main moves ahead.
	if err := os.WriteFile(filepath.Join(viewer, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, viewer, "add", "-A")
	runGit(t, viewer, "commit", "-m", "viewer-local")
	localTip := runGit(t, viewer, "rev-parse", "main")

	// Origin advances on a different commit: the two histories now diverge.
	if err := os.WriteFile(filepath.Join(prod, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, prod, "add", "-A")
	runGit(t, prod, "commit", "-m", "upstream-advance")
	runGit(t, prod, "push", "origin", "main")
	originTip := runGit(t, prod, "rev-parse", "main")

	s.SyncRemote(ctx)

	if got := runGit(t, viewer, "rev-parse", "main"); got != localTip {
		t.Errorf("local main moved: a follower must not merge or reset\n got %s\nwant %s", got, localTip)
	}
	// A merge commit would have two parents; the follower must create none.
	if parents := strings.Fields(runGit(t, viewer, "rev-list", "--parents", "-n", "1", "main")); len(parents) != 2 {
		t.Errorf("main tip has %d fields (want 2): follower must not create a merge commit", len(parents))
	}
	if got := runGit(t, viewer, "rev-parse", "origin/main"); got != originTip {
		t.Errorf("fallback fetch did not advance origin/main\n got %s\nwant %s", got, originTip)
	}
	st := s.sync.view(time.Now(), syncStaleAfter)
	if !st.Stale || !strings.Contains(st.Note, "diverged") {
		t.Errorf("want stale with a diverged note, got %+v", st)
	}
}

func hasSession(infos []sessionInfo, id string) bool {
	for _, in := range infos {
		if in.ID == id {
			return true
		}
	}
	return false
}
