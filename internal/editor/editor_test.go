package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/swarm"
)

// fakeClient simulates an opencode agent: on each turn it runs editFn against the
// worktree (the "edit") and returns reply.
type fakeClient struct {
	editFn func(dir string)
	reply  string
}

func (f *fakeClient) NewSession(ctx context.Context, dir, system, first string) (swarm.Session, string, error) {
	if f.editFn != nil {
		f.editFn(dir)
	}
	return &fakeSession{f: f, dir: dir}, f.reply, nil
}

type fakeSession struct {
	f   *fakeClient
	dir string
}

func (s *fakeSession) Prompt(ctx context.Context, text string) (string, error) {
	if s.f.editFn != nil {
		s.f.editFn(s.dir)
	}
	return s.f.reply, nil
}
func (s *fakeSession) Messages(ctx context.Context) ([]swarm.Message, error) { return nil, nil }
func (s *fakeSession) Close() error                                          { return nil }

func gitInit(t *testing.T, dir string) *git.Repo {
	t.Helper()
	g := git.New(dir)
	ctx := context.Background()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "receive.denyCurrentBranch", "updateInstead"},
	} {
		if _, err := g.Run(ctx, a...); err != nil {
			t.Fatalf("git %v: %v", a, err)
		}
	}
	return g
}

func setupRepo(t *testing.T) (string, *repo.Repo) {
	t.Helper()
	root := t.TempDir()
	g := gitInit(t, root)
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	smDir := filepath.Join(root, "submodules", "sm")
	if err := os.MkdirAll(smDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(smDir, repo.ROIFile), []byte("# ROI\n\noriginal goal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(context.Background(), "seed"); err != nil {
		t.Fatal(err)
	}
	rp, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, rp
}

func newTestManager(t *testing.T, root string, fc *fakeClient) *Manager {
	t.Helper()
	m, err := NewManager(root, config.Defaults(""))
	if err != nil {
		t.Fatal(err)
	}
	m.client = fc
	return m
}

func TestSessionEditAndMergeButton(t *testing.T) {
	root, _ := setupRepo(t)
	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "I appended a goal. How does that look?"}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("second goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	ctx := context.Background()

	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if st := sess.State(ctx); st != "live" {
		t.Fatalf("fresh session should be live, got %s", st)
	}
	if _, err := sess.Chat(ctx, "add a second goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if st := sess.State(ctx); st != "dirty" {
		t.Fatalf("after edit want dirty, got %s (err=%s)", st, sess.Err())
	}
	base, proposed, _ := sess.Diff(ctx)
	if strings.Contains(base, "second goal") || !strings.Contains(proposed, "second goal") {
		t.Fatalf("diff wrong: base=%q proposed=%q", base, proposed)
	}
	// Button merge.
	if err := sess.Merge(ctx); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if st := sess.State(ctx); st != "live" {
		t.Fatalf("after merge want live, got %s", st)
	}
	// The change must now be on main in the primary checkout.
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "second goal") {
		t.Fatalf("merge did not reach main working tree: %q", string(onMain))
	}
}

func TestSessionAgentPerformedMerge(t *testing.T) {
	root, _ := setupRepo(t)
	file := "submodules/sm/ROI.md"
	// Agent edits and, because the user approved, emits the merge marker.
	fc := &fakeClient{reply: "Done, merging now.\n" + mergeMarker}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		_ = os.WriteFile(p, []byte("# ROI\n\nrewritten by agent\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	ctx := context.Background()
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "rewrite it and merge"); err != nil {
		t.Fatal(err)
	}
	if st := sess.State(ctx); st != "live" {
		t.Fatalf("agent merge should leave state live, got %s (err=%s)", st, sess.Err())
	}
	// Marker stripped from the displayed reply.
	log := sess.Log()
	last := log[len(log)-1]
	if strings.Contains(last.Text, mergeMarker) {
		t.Fatalf("merge marker not stripped: %q", last.Text)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "rewritten by agent") {
		t.Fatalf("agent merge did not reach main: %q", string(onMain))
	}
}
