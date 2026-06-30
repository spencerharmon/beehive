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

// A turn now leaves its change UNCOMMITTED (pending). Approve commits it onto the
// edit branch but does NOT publish to main; the human gate is real.
func TestSessionApproveCommitsProposalWithoutPublishing(t *testing.T) {
	root, _ := setupRepo(t)
	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "appended."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("approved goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	ctx := context.Background()
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatal(err)
	}
	// Proposed but uncommitted: pending, and the branch HEAD does not have it yet.
	if !sess.Pending(ctx) {
		t.Fatal("want pending right after a turn")
	}
	if head, _ := sess.wt.Show(ctx, "HEAD", file); strings.Contains(head, "approved goal") {
		t.Fatalf("proposal must not be committed before approval; HEAD=%q", head)
	}

	if err := sess.Approve(ctx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// Approve records it on the branch (HEAD now has it) and clears pending...
	if sess.Pending(ctx) {
		t.Fatal("approve should clear pending")
	}
	head, err := sess.wt.Show(ctx, "HEAD", file)
	if err != nil || !strings.Contains(head, "approved goal") {
		t.Fatalf("approve did not commit onto branch HEAD: %q err=%v", head, err)
	}
	// ...but does NOT publish to main: still dirty, primary working tree untouched.
	if st := sess.State(ctx); st != "dirty" {
		t.Fatalf("approve must not publish; want dirty, got %s", st)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.Contains(string(onMain), "approved goal") {
		t.Fatalf("approve leaked to main working tree: %q", string(onMain))
	}
}

// Reject restores an existing file to its committed state: the session becomes a
// no-op (live) and the proposed content is gone.
func TestSessionRejectRestoresExistingFile(t *testing.T) {
	root, _ := setupRepo(t)
	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "rewrote."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		_ = os.WriteFile(p, []byte("# ROI\n\ntotally different\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	ctx := context.Background()
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "rewrite it"); err != nil {
		t.Fatal(err)
	}
	if !sess.Pending(ctx) {
		t.Fatal("want pending")
	}
	if err := sess.Reject(ctx); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if sess.Pending(ctx) {
		t.Fatal("reject should clear pending")
	}
	if st := sess.State(ctx); st != "live" {
		t.Fatalf("reject should restore live, got %s", st)
	}
	_, proposed, _ := sess.Diff(ctx)
	if strings.Contains(proposed, "totally different") || !strings.Contains(proposed, "original goal") {
		t.Fatalf("reject did not restore original content: %q", proposed)
	}
}

// Reject of a file the agent newly created (absent at HEAD) removes it entirely.
func TestSessionRejectRemovesNewFile(t *testing.T) {
	root, _ := setupRepo(t)
	file := "submodules/sm/NEWNOTES.md" // absent on main/HEAD
	fc := &fakeClient{reply: "created."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		_ = os.WriteFile(p, []byte("brand new\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	ctx := context.Background()
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "create it"); err != nil {
		t.Fatal(err)
	}
	if !sess.Pending(ctx) {
		t.Fatal("want pending for a new file")
	}
	if err := sess.Reject(ctx); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if sess.Pending(ctx) {
		t.Fatal("reject should clear pending")
	}
	if _, err := os.Stat(filepath.Join(sess.wtPath, filepath.FromSlash(file))); !os.IsNotExist(err) {
		t.Fatalf("reject should remove the new file; stat err=%v", err)
	}
}

// The surface is generic over ANY repo path, not just coordination files: a turn
// over a plain source file yields a proposed diff that approve+merge makes live.
func TestSessionArbitraryFileProposesAndMerges(t *testing.T) {
	root, _ := setupRepo(t)
	file := "internal/app/main.go"
	fc := &fakeClient{reply: "scaffolded."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("package app\n\nfunc main() {}\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	ctx := context.Background()
	sess, err := m.Open(ctx, file)
	if err != nil {
		t.Fatalf("open arbitrary file: %v", err)
	}
	if _, err := sess.Chat(ctx, "scaffold main"); err != nil {
		t.Fatal(err)
	}
	base, proposed, _ := sess.Diff(ctx)
	if base != "" {
		t.Fatalf("new file base should be empty, got %q", base)
	}
	if !strings.Contains(proposed, "func main()") {
		t.Fatalf("arbitrary-file turn did not yield a proposal: %q", proposed)
	}
	if !sess.Pending(ctx) {
		t.Fatal("arbitrary-file proposal should be pending")
	}
	if err := sess.Approve(ctx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := sess.Merge(ctx); err != nil {
		t.Fatalf("merge: %v", err)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "func main()") {
		t.Fatalf("arbitrary file did not reach main: %q", string(onMain))
	}
}

// Path traversal (and other escapes) are rejected at Open, before any worktree.
func TestOpenRejectsTraversal(t *testing.T) {
	root, _ := setupRepo(t)
	m := newTestManager(t, root, &fakeClient{})
	ctx := context.Background()
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "submodules/../../escape", ".git/config", "", "."} {
		if _, err := m.Open(ctx, bad); err == nil {
			t.Errorf("Open(%q) should be rejected", bad)
		}
	}
}
