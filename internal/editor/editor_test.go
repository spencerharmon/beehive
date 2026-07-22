package editor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func branchExists(t *testing.T, g *git.Repo, branch string) bool {
	t.Helper()
	_, err := g.Run(context.Background(), "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

// TestReloadNeverTouchesForeignEditWorktrees is the editor-session-wipe
// namespace-ownership regression: the beehive-root .worktrees/ dir is SHARED
// with the web layer's resolve agent (`edit-resolve-*`) and bootstrap chat
// editor (`edit-*`), which hold their proposals in memory with no persistence
// store. This Manager's startup/gc reclaim must own ONLY its own
// `hive-edit-*` namespace: a foreign edit worktree — even one that is clean and
// long past the TTL, i.e. exactly what USED to be adopted-then-deleted — must be
// left entirely alone (worktree, local branch, and NOT adopted into the store).
func TestReloadNeverTouchesForeignEditWorktrees(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	g := git.New(root)

	addWt := func(branch string) string {
		wt := filepath.Join(root, ".worktrees", branch)
		if _, err := g.Run(ctx, "worktree", "add", "-b", branch, wt, "main"); err != nil {
			t.Fatalf("worktree add %s: %v", branch, err)
		}
		return wt
	}

	// A live resolve-agent worktree and a bootstrap chat-editor worktree, both
	// clean (proposal held in memory) — the exact shape that was being wiped.
	resolveBranch := "edit-resolve-flux-sometask-1783835888732333469"
	resolveWt := addWt(resolveBranch)
	chatBranch := "edit-submodules-sm-ROI-md-1783835888"
	chatWt := addWt(chatBranch)

	m := newTestManager(t, root, &fakeClient{})
	// Clock far past the TTL: if these were (wrongly) treated as this Manager's
	// own stale+clean sessions, they would be reclaimed here.
	m.now = func() time.Time { return time.Now().Add(1000 * time.Hour) }

	if err := m.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}

	for _, wt := range []string{resolveWt, chatWt} {
		if _, err := os.Stat(wt); err != nil {
			t.Fatalf("foreign edit worktree %s must never be removed by the editor Manager: %v", wt, err)
		}
	}
	for _, b := range []string{resolveBranch, chatBranch} {
		if !branchExists(t, g, b) {
			t.Fatalf("foreign edit branch %s must never be deleted by the editor Manager", b)
		}
		if _, ok := m.Get(b); ok {
			t.Fatalf("foreign edit session %s must never be adopted into the editor Manager", b)
		}
	}
	// The store must not have been polluted with foreign records.
	recs, err := m.store.load()
	if err != nil {
		t.Fatalf("store load: %v", err)
	}
	for _, r := range recs {
		t.Fatalf("store must be empty of foreign sessions, found %q", r.Branch)
	}

	// And Reclaimable (the gc dry-run) must likewise ignore them.
	reclaimable, err := m.Reclaimable(ctx)
	if err != nil {
		t.Fatalf("reclaimable: %v", err)
	}
	if len(reclaimable) != 0 {
		t.Fatalf("Reclaimable must never list a foreign edit worktree, got %v", reclaimable)
	}
}

// TestReloadNeverReclaimsLiveRegisteredSession is the editor-session-wipe
// never-reclaim-a-live-session regression: an operator's OPEN session (live in
// byID) whose record has aged past the TTL and whose worktree is clean (a
// freshly opened session, or one whose change already merged) must NEVER be
// reclaimed by a reload/gc run on the LIVE daemon — doing so deletes the
// worktree/branch out from under the operator and 404s their next turn.
func TestReloadNeverReclaimsLiveRegisteredSession(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	g := git.New(root)
	file := "submodules/sm/ROI.md"

	m := newTestManager(t, root, &fakeClient{})
	sess, err := m.Open(ctx, file) // registers a live session; worktree is clean
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	branch := sess.Branch

	// Age the session past the TTL: its persisted record is now "stale", and the
	// worktree carries no pending change — the stale+clean shape that IS reclaimed
	// for an ABANDONED session, but must be KEPT for a LIVE (in-byID) one.
	m.now = func() time.Time { return time.Now().Add(2 * m.ttl) }

	if got, err := m.Reclaimable(ctx); err != nil {
		t.Fatalf("reclaimable: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("a LIVE registered session must never be reclaimable, got %v", got)
	}
	if err := m.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := m.Get(branch); !ok {
		t.Fatalf("live session %s was dropped from the Manager by Reload", branch)
	}
	if _, err := os.Stat(sess.wtPath); err != nil {
		t.Fatalf("live session worktree must survive Reload: %v", err)
	}
	if !branchExists(t, g, branch) {
		t.Fatalf("live session branch %s must survive Reload", branch)
	}
}

// TestReloadPrunesStaleKeepsActivePendingAndBee is the startup-prune acceptance:
// over a root repo carrying several worktrees, Reload removes EXACTLY the stale,
// change-free edit worktrees (and their branches), while it keeps and re-registers
// active (fresh record) and pending-change edit worktrees, and never touches
// honeybee bee-* worktrees or the main checkout.
func TestReloadPrunesStaleKeepsActivePendingAndBee(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	g := git.New(root)
	reloadNow := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	addWt := func(branch string) string {
		wt := filepath.Join(root, ".worktrees", branch)
		if _, err := g.Run(ctx, "worktree", "add", "-b", branch, wt, "main"); err != nil {
			t.Fatalf("worktree add %s: %v", branch, err)
		}
		return wt
	}

	// 1) stale + clean (no pending change) -> PRUNED.
	staleBranch := "hive-edit-stale-100"
	staleWt := addWt(staleBranch)

	// 2) active (fresh persisted record) + clean -> KEPT and re-registered.
	activeBranch := "hive-edit-active-200"
	activeWt := addWt(activeBranch)

	// 3) stale record-LESS worktree carrying a committed, unmerged change ->
	//    KEPT (pending-change safety) and recovered via derived file.
	pendingBranch := "hive-edit-pending-300"
	pendingWt := addWt(pendingBranch)
	pf := filepath.Join(pendingWt, "submodules", "sm", repo.InfraFile)
	if err := os.WriteFile(pf, []byte("pending infra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pg := git.New(pendingWt)
	if _, err := pg.Run(ctx, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Run(ctx, "commit", "-m", "pending edit"); err != nil {
		t.Fatal(err)
	}

	// 4) honeybee worktree -> NEVER enumerated for prune or registered.
	beeBranch := "bee-sometask-400"
	beeWt := addWt(beeBranch)

	m := newTestManager(t, root, &fakeClient{})
	m.now = func() time.Time { return reloadNow }
	// Persist a store: active is fresh; stale is old; pending is intentionally
	// absent (its record was "lost") to prove pending is kept regardless.
	recs := []sessionRecord{
		{ID: staleBranch, File: "submodules/sm/ROI.md", Branch: staleBranch, WtPath: staleWt, Activity: reloadNow.Add(-120 * time.Minute)},
		{ID: activeBranch, File: "submodules/sm/" + repo.InfraFile, Branch: activeBranch, WtPath: activeWt, Activity: reloadNow.Add(-1 * time.Minute)},
	}
	if err := m.store.save(recs); err != nil {
		t.Fatal(err)
	}

	if err := m.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// Stale: worktree + branch gone, not registered.
	if _, err := os.Stat(staleWt); !os.IsNotExist(err) {
		t.Fatalf("stale worktree not removed (stat err=%v)", err)
	}
	if branchExists(t, g, staleBranch) {
		t.Fatalf("stale branch %s not deleted", staleBranch)
	}
	if _, ok := m.Get(staleBranch); ok {
		t.Fatalf("stale session must not be registered")
	}

	// Active: kept and registered.
	if _, err := os.Stat(activeWt); err != nil {
		t.Fatalf("active worktree wrongly removed: %v", err)
	}
	if !branchExists(t, g, activeBranch) {
		t.Fatalf("active branch %s wrongly deleted", activeBranch)
	}
	if _, ok := m.Get(activeBranch); !ok {
		t.Fatalf("active session not re-registered after reload")
	}

	// Pending: kept and registered (recovered with the derived edited file).
	if _, err := os.Stat(pendingWt); err != nil {
		t.Fatalf("pending worktree wrongly removed: %v", err)
	}
	if !branchExists(t, g, pendingBranch) {
		t.Fatalf("pending branch %s wrongly deleted", pendingBranch)
	}
	ps, ok := m.Get(pendingBranch)
	if !ok {
		t.Fatalf("pending session not recovered")
	}
	if ps.File != "submodules/sm/"+repo.InfraFile {
		t.Fatalf("pending recovered file = %q, want submodules/sm/%s", ps.File, repo.InfraFile)
	}

	// Bee: never touched, never registered as an editor session.
	if _, err := os.Stat(beeWt); err != nil {
		t.Fatalf("honeybee worktree wrongly removed: %v", err)
	}
	if !branchExists(t, g, beeBranch) {
		t.Fatalf("honeybee branch %s wrongly deleted", beeBranch)
	}
	if _, ok := m.Get(beeBranch); ok {
		t.Fatalf("honeybee worktree must never be an editor session")
	}

	// Idempotent: a second reload removes nothing more and keeps the same set.
	if err := m.Reload(ctx); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if _, ok := m.Get(activeBranch); !ok {
		t.Fatalf("active lost after second reload")
	}
	if _, ok := m.Get(pendingBranch); !ok {
		t.Fatalf("pending lost after second reload")
	}
	if branchExists(t, g, staleBranch) {
		t.Fatalf("stale branch reappeared after second reload")
	}
}

// TestSessionStateSurvivesRestart is the persistence acceptance: an in-flight
// edit (open + one agent turn leaving a pending proposal) is recovered by a fresh
// Manager over the same repo after a simulated beehived restart — same open file,
// same pending diff, still mergeable, chat log intact.
func TestSessionStateSurvivesRestart(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/ROI.md"
	fc := &fakeClient{reply: "added a goal."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("second goal\n")...), 0o644)
	}

	// Manager A: open and run one turn, leaving a pending (dirty) proposal.
	mA := newTestManager(t, root, fc)
	sess, err := mA.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if st := sess.State(ctx); st != "dirty" {
		t.Fatalf("pre-restart want dirty, got %s (err=%s)", st, sess.Err())
	}
	id := sess.ID

	// Simulated restart: a brand-new Manager over the SAME root recovers state
	// purely from the persisted store (no shared memory with mA).
	mB := newTestManager(t, root, fc)
	if _, ok := mB.Get(id); ok {
		t.Fatalf("fresh manager should start empty before reload")
	}
	if err := mB.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}

	got, ok := mB.Get(id)
	if !ok {
		t.Fatalf("session %s not recovered after restart", id)
	}
	if got.File != file {
		t.Fatalf("recovered file = %q, want %q", got.File, file)
	}
	if st := got.State(ctx); st != "dirty" {
		t.Fatalf("recovered session want dirty, got %s", st)
	}
	base, proposed, _ := got.Diff(ctx)
	if strings.Contains(base, "second goal") || !strings.Contains(proposed, "second goal") {
		t.Fatalf("recovered diff wrong: base=%q proposed=%q", base, proposed)
	}
	if len(got.Log()) == 0 {
		t.Fatalf("recovered session lost its chat log")
	}
	// The recovered proposal is still live and mergeable.
	if err := got.Merge(ctx); err != nil {
		t.Fatalf("merge recovered session: %v", err)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if !strings.Contains(string(onMain), "second goal") {
		t.Fatalf("recovered merge did not reach main: %q", string(onMain))
	}
}

// TestReloadEmptyIsNoop confirms recovery on a repo with no editor state is a
// clean no-op: no sessions, no error, and a written (empty) store.
func TestReloadEmptyIsNoop(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	m := newTestManager(t, root, &fakeClient{})
	if err := m.Reload(ctx); err != nil {
		t.Fatalf("reload empty: %v", err)
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("want 0 sessions after empty reload, got %d", n)
	}
}

// TestPublishFileEndToEnd is the hive-edit-command acceptance for the local-
// sharing (no remote) case: PublishFile runs worktree -> write -> commit ->
// publish -> cleanup in one call and lands the change on main with no
// dangling worktree or branch left behind.
func TestPublishFileEndToEnd(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/ROI.md"

	if err := PublishFile(ctx, root, file, "# ROI\n\noriginal goal\ncli goal\n", "operator: add cli goal", false); err != nil {
		t.Fatalf("PublishFile: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	if !strings.Contains(string(got), "cli goal") {
		t.Fatalf("main missing the published change: %q", string(got))
	}

	// No dangling worktree/branch: the CLI's edit-cli-* branch is gone from both
	// git's worktree admin and refs/heads.
	g := git.New(root)
	wts, err := g.Worktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wts {
		if strings.HasPrefix(w.Branch, "edit-cli-") {
			t.Fatalf("dangling worktree left behind: %+v", w)
		}
	}
	out, _ := g.Run(ctx, "for-each-ref", "--format=%(refname)", "refs/heads/edit-cli-*")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("dangling branch left behind: %q", out)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".worktrees"))
	if err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "edit-cli-") {
				t.Fatalf("dangling worktree dir left behind: %s", e.Name())
			}
		}
	}

	// Verify the commit carries the operator's message, not a placeholder.
	logOut, err := g.Run(ctx, "log", "-1", "--format=%s", "--", file)
	if err != nil {
		t.Fatal(err)
	}
	if logOut != "operator: add cli goal" {
		t.Fatalf("commit message = %q, want %q", logOut, "operator: add cli goal")
	}
}

// TestPublishFileForkSeededHealsViaMerge is the fork-seeded acceptance: a
// concurrent writer advances the trusted remote's main (a genuine fork against
// the worktree branch's base) between PublishFile's worktree creation and its
// publish. PublishToMain's fetch+merge retry must heal that fork rather than
// fail or clobber the concurrent commit.
func TestPublishFileForkSeededHealsViaMerge(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
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

	// Seed a fork: a second, independent clone commits directly to the bare
	// remote's main AFTER PublishFile's base is resolved but before it publishes.
	// Simulate that ordering by making the concurrent commit first (PublishToMain's
	// retry loop must still merge it in, not just at open time), then running
	// PublishFile against the now-diverged remote.
	other := t.TempDir()
	if _, err := exec.Command("git", "clone", "-q", bare, other).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v", err)
	}
	og := git.New(other)
	for _, a := range [][]string{
		{"config", "user.email", "o@o"},
		{"config", "user.name", "o"},
	} {
		if _, err := og.Run(ctx, a...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(other, "submodules", "sm", "OTHER.md"), []byte("concurrent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := og.Run(ctx, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := og.Run(ctx, "commit", "-q", "-m", "concurrent writer"); err != nil {
		t.Fatal(err)
	}
	if _, err := og.Run(ctx, "push", "-q", "origin", "main"); err != nil {
		t.Fatal(err)
	}

	if err := PublishFile(ctx, root, file, "# ROI\n\noriginal goal\nfork-seeded goal\n", "operator: fork-seeded edit", false); err != nil {
		t.Fatalf("PublishFile did not heal the fork: %v", err)
	}

	// Both the concurrent writer's commit and the CLI's edit must survive on the
	// remote's main (a real merge, not one clobbering the other).
	out, err := bg.Run(ctx, "show", "main:"+file)
	if err != nil {
		t.Fatalf("read remote main: %v", err)
	}
	if !strings.Contains(out, "fork-seeded goal") {
		t.Fatalf("remote main missing the CLI's change:\n%s", out)
	}
	if !bg_fileExists(ctx, bg, "main", "submodules/sm/OTHER.md") {
		t.Fatal("remote main lost the concurrent writer's file after the merge")
	}
}

// bg_fileExists reports whether path exists at ref in repo g.
func bg_fileExists(ctx context.Context, g *git.Repo, ref, path string) bool {
	_, err := g.Run(ctx, "show", ref+":"+path)
	return err == nil
}

// TestPublishWorktreeSharedByEditorAndCLI proves the accept criterion that the
// chat editor's Session.merge and the CLI's PublishFile call the SAME shared
// helper (PublishWorktree), rather than each re-implementing the
// publish-to-main sequence: both exercised end to end here, then the source is
// checked to confirm each call site actually invokes PublishWorktree (not a
// parallel copy of its logic).
func TestPublishWorktreeSharedByEditorAndCLI(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()

	// Path 1: the chat editor's Session.Merge.
	fc := &fakeClient{reply: "done."}
	roiFile := "submodules/sm/ROI.md"
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(roiFile))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("chat goal\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.Open(ctx, roiFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sess.Chat(ctx, "add a goal"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if err := sess.Merge(ctx); err != nil {
		t.Fatalf("session merge: %v", err)
	}

	// Path 2: the CLI's PublishFile, on a second coordination file.
	linksFile := repo.LinksFile
	if err := PublishFile(ctx, root, linksFile, "links: {}\n", "operator: seed links", false); err != nil {
		t.Fatalf("PublishFile: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(roiFile)))
	if !strings.Contains(string(got), "chat goal") {
		t.Fatalf("chat-driven merge did not land: %q", got)
	}
	got2, _ := os.ReadFile(filepath.Join(root, linksFile))
	if !strings.Contains(string(got2), "links:") {
		t.Fatalf("CLI PublishFile did not land: %q", got2)
	}

	// Static proof the two entry points share the helper, not parallel logic.
	src, err := os.ReadFile("editor.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	mergeIdx := strings.Index(text, "func (s *Session) merge(")
	publishFileIdx := strings.Index(text, "func PublishFile(")
	if mergeIdx < 0 || publishFileIdx < 0 {
		t.Fatal("could not locate merge()/PublishFile() in editor.go")
	}
	mergeBody := text[mergeIdx:publishFileIdx]
	if !strings.Contains(mergeBody, "PublishWorktree(ctx") {
		t.Fatal("Session.merge no longer calls the shared PublishWorktree helper")
	}
	rest := text[publishFileIdx:]
	if !strings.Contains(rest, "PublishWorktree(ctx") {
		t.Fatal("PublishFile no longer calls the shared PublishWorktree helper")
	}
}
