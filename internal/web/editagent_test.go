package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/swarm"
)

// edit-session-consolidation: the resolve agent and bootstrap agent are now
// editor.Sessions over the shared editor.Manager, so web tests drive them through
// the SAME fake opencode transport the editor package uses — an editor.AgentClient
// (NewSession-based), not the retired swarm.Client-based fakeChatClient. This file
// provides that fake plus the git-repo fixtures the web tests build on (write,
// gitShow, hasAddRow, and a Server whose editor.Manager is fake-backed).

// fakeAgentClient is an editor.AgentClient that returns a fixed reply for every
// turn and records the cwd/system/first it was opened with, so a test can drive a
// real editor turn (and assert the seeded system prompt) without a real opencode
// server. editFn, when set, mutates the session's worktree on open and on each
// turn (simulating the agent editing files), so the commit/diff/publish path runs
// on real content. All fields are set once at construction; mu guards the
// recorded cwd/system/first written from the session's turn goroutine.
type fakeAgentClient struct {
	reply  string
	editFn func(cwd string)

	mu     sync.Mutex
	cwd    string
	system string
	first  string
}

func (f *fakeAgentClient) NewSession(ctx context.Context, cwd, system, first string) (swarm.Session, string, error) {
	f.mu.Lock()
	f.cwd, f.system, f.first = cwd, system, first
	f.mu.Unlock()
	if f.editFn != nil {
		f.editFn(cwd)
	}
	return &fakeAgentSession{reply: f.reply, editFn: f.editFn, cwd: cwd}, f.reply, nil
}

func (f *fakeAgentClient) lastSystem() string { f.mu.Lock(); defer f.mu.Unlock(); return f.system }
func (f *fakeAgentClient) lastFirst() string  { f.mu.Lock(); defer f.mu.Unlock(); return f.first }

type fakeAgentSession struct {
	reply  string
	editFn func(cwd string)
	cwd    string
}

func (s *fakeAgentSession) Prompt(ctx context.Context, text string) (string, error) {
	if s.editFn != nil {
		s.editFn(s.cwd)
	}
	return s.reply, nil
}
func (s *fakeAgentSession) Messages(ctx context.Context) ([]swarm.Message, error) { return nil, nil }
func (s *fakeAgentSession) Close() error                                          { return nil }

// editorFixture builds a Server over a real git repo (seeded with a committed
// alpha submodule so a worktree can be cut from main) whose editor.Manager — and
// therefore its resolve/bootstrap agents — is driven by a fixed-reply fake.
func editorFixture(t *testing.T, reply string) (*Server, string) {
	t.Helper()
	return editorFixtureClient(t, &fakeAgentClient{reply: reply})
}

// editorFixtureClient is editorFixture with a caller-supplied editor.AgentClient,
// so a test can hold the client reference (e.g. to assert the seeded system
// prompt) or attach an editFn that writes files during a turn.
func editorFixtureClient(t *testing.T, client editor.AgentClient) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	g := git.New(root)
	ctx := context.Background()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if _, err := g.Run(ctx, a...); err != nil {
			t.Fatalf("git %v: %v", a, err)
		}
	}
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	sm := filepath.Join(root, "submodules", "alpha")
	if err := os.MkdirAll(sm, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(sm, repo.ROIFile), "# alpha\n")
	write(t, filepath.Join(sm, repo.PlanFile), "<!-- Beehive-ROI: abc123 -->\n# Plan\n")
	write(t, filepath.Join(sm, "notes.md"), "alpha\nbeta\n")
	if err := g.Commit(ctx, "seed"); err != nil {
		t.Fatal(err)
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(r, config.Defaults(root))
	if err != nil {
		t.Fatal(err)
	}
	// Swap the editor.Manager for a fake-backed one and re-point the resolve facade
	// at it, so every editor/resolve/bootstrap session in this Server runs turns
	// through the fake instead of a real opencode server.
	em, err := editor.NewManagerWithClient(root, config.Defaults(root), client)
	if err != nil {
		t.Fatal(err)
	}
	s.editors = em
	s.humans = newResolveManager(em)
	return s, root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// gitShow returns the trimmed content of path at ref in dir.
func gitShow(t *testing.T, dir, ref, path string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "show", ref+":"+path).Output()
	if err != nil {
		t.Fatalf("git show %s:%s in %s: %v", ref, path, dir, err)
	}
	return strings.TrimRight(string(out), "\n")
}

func hasAddRow(rows []editor.DiffRow, want string) bool {
	for _, r := range rows {
		if r.Kind == "add" && strings.Contains(string(r.HTML), want) {
			return true
		}
	}
	return false
}
