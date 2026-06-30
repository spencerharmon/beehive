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

// TestMergeBlocksWholeFileDeletionUntilConfirmed is the core delete guard: a
// proposal that wipes a human-owned file (ROI.md) must NOT merge on a plain
// Merge — it returns ErrDeleteNeedsConfirm and leaves main untouched — and only
// the separate, explicit MergeConfirm publishes it.
func TestMergeBlocksWholeFileDeletionUntilConfirmed(t *testing.T) {
	root, _ := setupRepo(t)
	file := "submodules/sm/ROI.md"
	// The "edit" empties the whole file — the wrong-base phantom-deletion shape.
	fc := &fakeClient{reply: "cleared it."}
	fc.editFn = func(dir string) {
		_ = os.WriteFile(filepath.Join(dir, filepath.FromSlash(file)), []byte(""), 0o644)
	}
	m := newTestManager(t, root, fc)
	ctx := context.Background()
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "clear the file"); err != nil {
		t.Fatal(err)
	}
	// Plain merge is default-BLOCKED.
	if err := sess.Merge(ctx); err != ErrDeleteNeedsConfirm {
		t.Fatalf("want ErrDeleteNeedsConfirm, got %v", err)
	}
	if st := sess.State(ctx); st != "dirty" {
		t.Fatalf("blocked merge should stay dirty, got %s", st)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "original goal") {
		t.Fatalf("blocked merge must not change main: %q", string(onMain))
	}
	// The explicit, separate confirmation authorizes it.
	if err := sess.MergeConfirm(ctx); err != nil {
		t.Fatalf("confirm merge: %v", err)
	}
	if st := sess.State(ctx); st != "live" {
		t.Fatalf("after confirm want live, got %s (err=%s)", st, sess.Err())
	}
	onMain, _ = os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.TrimSpace(string(onMain)) != "" {
		t.Fatalf("confirmed deletion should empty the file on main, got %q", string(onMain))
	}
}

// TestAgentMergeCannotDeleteHumanOwnedFile proves an agent-driven merge (the
// <<<MERGE>>> marker) can never confirm a protected whole-file deletion: it is
// blocked and surfaced as a panel error, never silently published.
func TestAgentMergeCannotDeleteHumanOwnedFile(t *testing.T) {
	root, _ := setupRepo(t)
	file := "submodules/sm/ROI.md"
	// Agent wipes the file AND asks to merge in the same turn.
	fc := &fakeClient{reply: "cleared and merging.\n" + mergeMarker}
	fc.editFn = func(dir string) {
		_ = os.WriteFile(filepath.Join(dir, filepath.FromSlash(file)), []byte(""), 0o644)
	}
	m := newTestManager(t, root, fc)
	ctx := context.Background()
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "clear it and merge"); err != nil {
		t.Fatal(err)
	}
	if st := sess.State(ctx); st != "dirty" {
		t.Fatalf("agent deletion-merge must stay dirty, got %s", st)
	}
	if e := sess.Err(); !strings.Contains(e, "confirm") {
		t.Fatalf("want a confirmation error surfaced, got %q", e)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "original goal") {
		t.Fatalf("agent must not delete the human-owned file on main: %q", string(onMain))
	}
}

// TestOpenFailsWhenTargetMissingAtRepoOwnBase covers guard 2: when the chosen
// base (here a repo-own origin/main that is legitimately behind local main)
// lacks a file that local main has, opening a session would render that file as
// a destructive deletion. Open must refuse instead.
func TestOpenFailsWhenTargetMissingAtRepoOwnBase(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	bare := t.TempDir()
	if _, err := git.New(bare).Run(ctx, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	g := git.New(root)
	if _, err := g.Run(ctx, "remote", "add", "origin", bare); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(ctx, "push", "-q", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	// Add an editable file to LOCAL main only; origin/main (repo-own, shared
	// history) is legitimately behind and lacks it.
	infra := "submodules/sm/" + repo.InfraFile
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(infra)), []byte("# infra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(ctx, "add infra locally"); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, root, &fakeClient{})
	if _, err := m.Open(ctx, infra); err == nil {
		t.Fatal("want Open to fail: file exists on local main but not at repo-own base")
	} else if !strings.Contains(err.Error(), "refusing to open a destructive edit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestForeignOriginIgnoredInFavorOfLocalMain covers guard 3: an origin whose
// main has UNRELATED history (a wrong/foreign remote) is neither used as the
// edit base nor as a merge push target. The session bases on local main and
// keeps no remote.
func TestForeignOriginIgnoredInFavorOfLocalMain(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	g := git.New(root)
	localMain, err := g.RevParse(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	// A bare "origin" populated from a DIFFERENT repo (unrelated history).
	bare := t.TempDir()
	if _, err := git.New(bare).Run(ctx, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	og := gitInit(t, other)
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := og.Commit(ctx, "unrelated root"); err != nil {
		t.Fatal(err)
	}
	if _, err := og.Run(ctx, "remote", "add", "origin", bare); err != nil {
		t.Fatal(err)
	}
	if _, err := og.Run(ctx, "push", "-q", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(ctx, "remote", "add", "origin", bare); err != nil {
		t.Fatal(err)
	}
	file := "submodules/sm/ROI.md"
	m := newTestManager(t, root, &fakeClient{})
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open should succeed against local main: %v", err)
	}
	if sess.remote != "" {
		t.Fatalf("foreign origin must not be a push target, got remote %q", sess.remote)
	}
	if sess.baseMain != localMain {
		t.Fatalf("want base = local main %s, got %s", localMain, sess.baseMain)
	}
}

func TestSessionMergeAutoPushesRemote(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	// Bare remote that accepts pushes to main.
	bare := t.TempDir()
	bg := git.New(bare)
	if _, err := bg.Run(ctx, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	g := git.New(root)
	if _, err := g.Run(ctx, "remote", "add", "origin", bare); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(ctx, "push", "-q", "origin", "main"); err != nil {
		t.Fatal(err)
	}

	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("remote goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if sess.remote != "origin" {
		t.Fatalf("want remote origin, got %q", sess.remote)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Merge(ctx); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if st := sess.State(ctx); st != "live" {
		t.Fatalf("want live after merge, got %s (err=%s)", st, sess.Err())
	}
	// The change must be on the remote's main.
	out, err := bg.Run(ctx, "show", "main:"+file)
	if err != nil {
		t.Fatalf("read remote main: %v", err)
	}
	if !strings.Contains(out, "remote goal") {
		t.Fatalf("remote main missing the change:\n%s", out)
	}
}
