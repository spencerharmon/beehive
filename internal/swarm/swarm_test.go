package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// mockClient records prompts and lets the test drive the submodule to terminal.
type mockClient struct {
	sess *mockSession
}

func (m *mockClient) NewSession(ctx context.Context, cwd, system, first string) (Session, error) {
	if m.sess == nil {
		m.sess = &mockSession{}
	}
	return m.sess, nil
}

type mockSession struct {
	prompts int
	onTurn  func(turn int)
}

func (s *mockSession) Prompt(ctx context.Context, text string) error {
	s.prompts++
	if s.onTurn != nil {
		s.onTurn(s.prompts)
	}
	return nil
}
func (s *mockSession) Close() error { return nil }

func gitInit(t *testing.T, dir string) *git.Repo {
	g := git.New(dir)
	ctx := context.Background()
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	return g
}

func TestRunCompletes(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1"}}

	mc := &mockClient{}
	r := &Runner{Repo: rp, Git: g, Client: mc, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r.Client = cl
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed || res.GCMarked {
		t.Fatalf("res %+v", res)
	}
}

func TestRunGCCap(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.MkdirAll(filepath.Join(sm, "repo"), 0o755)
	gitInit(t, filepath.Join(sm, "repo"))
	os.WriteFile(filepath.Join(sm, "repo", "f"), []byte("x"), 0o644)
	git.New(filepath.Join(sm, "repo")).Commit(context.Background(), "base")
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")
	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1"}}
	r := &Runner{Repo: rp, Git: g, Client: &mockClient{}, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed || !res.GCMarked || res.Turns != 4 {
		t.Fatalf("want gc cap, got %+v", res)
	}
}
