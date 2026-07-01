package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
)

// seedFile writes a repo-relative file into the primary checkout and commits it
// on main, so a generic session can base its worktree on it (or, for a new file,
// callers simply skip seeding).
func seedFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.New(root).Commit(context.Background(), "seed "+rel); err != nil {
		t.Fatal(err)
	}
}

// branchTip returns the commit SHA at the session's edit-branch tip, read from
// the primary checkout (shared object store/refs).
func branchTip(t *testing.T, root, branch string) string {
	t.Helper()
	sha, err := git.New(root).RevParse(context.Background(), branch)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", branch, err)
	}
	return sha
}

// TestValidateAnyPathTraversalRejected is the "path traversal rejected"
// acceptance for the generic surface: ValidateAnyPath (and thus OpenPath) accepts
// an arbitrary in-repo path but rejects empty, absolute, and any escaping path —
// while, unlike ValidateFile, NOT restricting the basename to coordination files.
func TestValidateAnyPathTraversalRejected(t *testing.T) {
	ok := []string{
		"submodules/sm/repo/main.go",
		"README.md",
		"a/b/c/deep.txt",
		"submodules/sm/INFRASTRUCTURE.md",
	}
	for _, p := range ok {
		if err := ValidateAnyPath(p); err != nil {
			t.Errorf("ValidateAnyPath(%q) = %v, want nil", p, err)
		}
	}
	bad := []string{
		"",
		".",
		"/etc/passwd",
		"../outside",
		"submodules/../../escape",
		"a/../../b",
	}
	for _, p := range bad {
		if err := ValidateAnyPath(p); err == nil {
			t.Errorf("ValidateAnyPath(%q) = nil, want error", p)
		}
	}
}

// TestOpenPathRejectsTraversal proves the traversal guard fires at the OpenPath
// entry point (no worktree is created for an escaping path).
func TestOpenPathRejectsTraversal(t *testing.T) {
	root, _ := setupRepo(t)
	m := newTestManager(t, root, &fakeClient{})
	if _, err := m.OpenPath(context.Background(), "../../etc/passwd"); err == nil {
		t.Fatal("OpenPath must reject a traversal path")
	}
}

// TestGenericTurnProposesWithoutCommitting is the core chat-diff behavior: a turn
// over an ARBITRARY file edits the worktree and surfaces a proposed diff, but does
// NOT auto-commit (unlike the restricted flow) and does NOT touch main — the
// change stays a pending proposal awaiting human approval.
func TestGenericTurnProposesWithoutCommitting(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/repo/app.py"
	seedFile(t, root, file, "print('one')\n")

	fc := &fakeClient{reply: "I added a second print."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("print('two')\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)

	sess, err := m.OpenPath(ctx, file)
	if err != nil {
		t.Fatalf("open path: %v", err)
	}
	if !sess.Generic() {
		t.Fatal("OpenPath session must be generic")
	}
	if sess.Pending(ctx) {
		t.Fatal("fresh session has no pending proposal")
	}
	base := sess.baseMain

	if _, err := sess.Chat(ctx, "add another print"); err != nil {
		t.Fatalf("chat: %v (err=%s)", err, sess.Err())
	}

	// A proposed diff is visible.
	b, proposed, _ := sess.Diff(ctx)
	if strings.Contains(b, "print('two')") || !strings.Contains(proposed, "print('two')") {
		t.Fatalf("diff wrong: base=%q proposed=%q", b, proposed)
	}
	// Pending, and NOT committed: the edit branch tip is still the base.
	if !sess.Pending(ctx) {
		t.Fatal("generic turn should leave a pending (uncommitted) proposal")
	}
	if tip := branchTip(t, root, sess.Branch); tip != base {
		t.Fatalf("generic turn must not auto-commit: tip=%s base=%s", tip, base)
	}
	// main is untouched.
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.Contains(string(onMain), "print('two')") {
		t.Fatalf("generic turn must not change main: %q", string(onMain))
	}
}

// TestGenericApproveCommits is the approve acceptance: Approve commits the pending
// proposal onto the edit branch (Pending clears, a new commit lands carrying the
// change) while deliberately NOT publishing to main.
func TestGenericApproveCommits(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/repo/app.py"
	seedFile(t, root, file, "print('one')\n")

	fc := &fakeClient{reply: "added a line."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("print('two')\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.OpenPath(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	base := sess.baseMain
	if _, err := sess.Chat(ctx, "add a line"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Approve(ctx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// No longer pending, and a commit now carries the change on the branch.
	if sess.Pending(ctx) {
		t.Fatal("after approve the proposal must be committed (not pending)")
	}
	tip := branchTip(t, root, sess.Branch)
	if tip == base {
		t.Fatal("approve must create a commit on the edit branch")
	}
	committed, err := git.New(root).Run(ctx, "show", sess.Branch+":"+file)
	if err != nil {
		t.Fatalf("show committed file: %v", err)
	}
	if !strings.Contains(committed, "print('two')") {
		t.Fatalf("committed file missing the change: %q", committed)
	}
	// Approve does NOT publish to main (that is a separate step).
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.Contains(string(onMain), "print('two')") {
		t.Fatalf("approve must not publish to main: %q", string(onMain))
	}
}

// TestGenericRejectDiscardsExistingFile is the reject acceptance for an existing
// file: Reject restores the file to its committed (base) content, leaves nothing
// pending, makes no commit, and never touches main — a true no-op.
func TestGenericRejectDiscardsExistingFile(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/repo/app.py"
	seedFile(t, root, file, "print('one')\n")

	fc := &fakeClient{reply: "rewrote it."}
	fc.editFn = func(dir string) {
		_ = os.WriteFile(filepath.Join(dir, filepath.FromSlash(file)), []byte("print('WRONG')\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.OpenPath(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	base := sess.baseMain
	if _, err := sess.Chat(ctx, "rewrite it"); err != nil {
		t.Fatal(err)
	}
	if !sess.Pending(ctx) {
		t.Fatal("precondition: a pending proposal should exist")
	}
	if err := sess.Reject(ctx); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if sess.Pending(ctx) {
		t.Fatal("reject must clear the pending proposal")
	}
	// Worktree file is back to the base content; no commit was made.
	wtBody, _ := os.ReadFile(filepath.Join(sess.wtPath, filepath.FromSlash(file)))
	if strings.Contains(string(wtBody), "WRONG") || !strings.Contains(string(wtBody), "print('one')") {
		t.Fatalf("reject did not restore base content: %q", string(wtBody))
	}
	if tip := branchTip(t, root, sess.Branch); tip != base {
		t.Fatalf("reject must not commit: tip=%s base=%s", tip, base)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.Contains(string(onMain), "WRONG") {
		t.Fatalf("reject must not touch main: %q", string(onMain))
	}
}

// TestGenericRejectRemovesNewFile covers reject of a brand-NEW file the agent
// created (absent at HEAD): it is removed from the worktree entirely, leaving no
// pending change.
func TestGenericRejectRemovesNewFile(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/repo/newfile.txt" // never seeded: new on main

	fc := &fakeClient{reply: "created it."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("brand new\n"), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.OpenPath(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "create it"); err != nil {
		t.Fatal(err)
	}
	if !sess.Pending(ctx) {
		t.Fatal("a new untracked file should be pending")
	}
	if err := sess.Reject(ctx); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if sess.Pending(ctx) {
		t.Fatal("reject must clear the pending new file")
	}
	if _, err := os.Stat(filepath.Join(sess.wtPath, filepath.FromSlash(file))); !os.IsNotExist(err) {
		t.Fatalf("reject must remove the new untracked file (stat err=%v)", err)
	}
}

// TestGenericPromptOmitsMergeMarker proves a generic session's system prompt does
// not arm the agent merge-marker, and that even if an agent erroneously emits the
// marker, a generic turn neither auto-commits nor publishes to main — the human
// Approve/Reject gate is the only path a generic change lands.
func TestGenericPromptOmitsMergeMarker(t *testing.T) {
	file := "submodules/sm/repo/app.py"
	if p := promptFor(file, true); strings.Contains(p, mergeMarker) {
		t.Fatalf("generic prompt must not mention the merge marker:\n%s", p)
	}
	if p := promptFor(file, false); !strings.Contains(p, mergeMarker) {
		t.Fatal("restricted prompt should still arm the merge marker")
	}

	root, _ := setupRepo(t)
	ctx := context.Background()
	seedFile(t, root, file, "print('one')\n")
	fc := &fakeClient{reply: "done.\n" + mergeMarker} // agent (wrongly) emits marker
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("print('two')\n")...), 0o644)
	}
	m := newTestManager(t, root, fc)
	sess, err := m.OpenPath(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	base := sess.baseMain
	if _, err := sess.Chat(ctx, "add a line and merge"); err != nil {
		t.Fatal(err)
	}
	// Marker must NOT drive a commit or a merge in generic mode.
	if tip := branchTip(t, root, sess.Branch); tip != base {
		t.Fatalf("generic marker turn must not commit: tip=%s base=%s", tip, base)
	}
	onMain, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if strings.Contains(string(onMain), "print('two')") {
		t.Fatalf("generic marker turn must not publish to main: %q", string(onMain))
	}
	if !sess.Pending(ctx) {
		t.Fatal("generic marker turn should still leave a pending proposal")
	}
}

// TestGenericSessionSurvivesRestart proves the generic flag persists: a generic
// session recovered by a fresh Manager after a restart is still generic (so it
// keeps deferring commits) with its pending proposal intact.
func TestGenericSessionSurvivesRestart(t *testing.T) {
	root, _ := setupRepo(t)
	ctx := context.Background()
	file := "submodules/sm/repo/app.py"
	seedFile(t, root, file, "print('one')\n")
	fc := &fakeClient{reply: "added a line."}
	fc.editFn = func(dir string) {
		p := filepath.Join(dir, filepath.FromSlash(file))
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, []byte("print('two')\n")...), 0o644)
	}
	mA := newTestManager(t, root, fc)
	sess, err := mA.OpenPath(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Chat(ctx, "add a line"); err != nil {
		t.Fatal(err)
	}
	id := sess.ID

	mB := newTestManager(t, root, fc)
	if err := mB.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := mB.Get(id)
	if !ok {
		t.Fatalf("generic session %s not recovered", id)
	}
	if !got.Generic() {
		t.Fatal("recovered session lost its generic flag")
	}
	if !got.Pending(ctx) {
		t.Fatal("recovered generic session lost its pending proposal")
	}
}
