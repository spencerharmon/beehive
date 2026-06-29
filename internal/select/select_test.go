package selectt

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
)

func hive(t *testing.T) (*repo.Repo, *git.Repo, string) {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	g := git.New(root)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	repo.Init(root)
	rp, _ := repo.Open(root)
	return rp, g, root
}

func sub(root, name string, files map[string]string) {
	d := filepath.Join(root, "submodules", name)
	os.MkdirAll(d, 0o755)
	for f, b := range files {
		os.WriteFile(filepath.Join(d, f), []byte(b), 0o644)
	}
}

func sel(root string, g *git.Repo) *Selector {
	rp, _ := repo.Open(root)
	return &Selector{Repo: rp, Git: g, Rand: rand.New(rand.NewSource(1)), TTL: time.Hour}
}

func TestSelectWork(t *testing.T) {
	_, g, root := hive(t)
	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
	g.Commit(context.Background(), "seed")
	head, _ := g.LastCommit(context.Background(), "submodules/a/ROI.md")
	os.WriteFile(filepath.Join(root, "submodules/a/PLAN.md"), []byte("<!-- Beehive-ROI: "+head+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "stamp")
	s, err := sel(root, g).Select(context.Background())
	if err != nil || s == nil {
		t.Fatalf("sel %v %v", s, err)
	}
	if s.Kind != Work || s.Task.ID != "T1" {
		t.Fatalf("got %+v", s)
	}
}

func TestDormantSkipped(t *testing.T) {
	_, g, root := hive(t)
	sub(root, "a", map[string]string{}) // no ROI -> dormant
	g.Commit(context.Background(), "seed")
	s, _ := sel(root, g).Select(context.Background())
	if s != nil {
		t.Fatalf("dormant selected: %+v", s)
	}
}

func TestBootstrap(t *testing.T) {
	_, g, root := hive(t)
	sub(root, "a", map[string]string{"ROI.md": "x"}) // ROI no PLAN
	g.Commit(context.Background(), "seed")
	s, _ := sel(root, g).Select(context.Background())
	if s == nil || s.Kind != Bootstrap {
		t.Fatalf("want bootstrap, got %+v", s)
	}
}

func TestReconcilePriority0(t *testing.T) {
	_, g, root := hive(t)
	// PLAN stamped to an old sha but ROI committed later -> drift.
	sub(root, "a", map[string]string{"ROI.md": "x", "PLAN.md": "<!-- Beehive-ROI: dead -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"})
	g.Commit(context.Background(), "seed")
	s, _ := sel(root, g).Select(context.Background())
	if s == nil || s.Kind != Reconcile || s.DiffRange == "" {
		t.Fatalf("want reconcile, got %+v", s)
	}
}
