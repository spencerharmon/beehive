package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/swarm"
)

// fakeChatClient is a swarm.Client that returns a fixed reply for every turn and
// records the cwd and system prompt it was opened with, so a test can drive the
// propose/approve loop — and assert the seeded per-file context — without a real
// opencode server.
type fakeChatClient struct {
	reply  string
	cwd    string
	system string
}

func (f *fakeChatClient) Open(ctx context.Context, cwd, system string) (swarm.Session, error) {
	f.cwd = cwd
	f.system = system
	return &fakeChatSession{reply: f.reply}, nil
}

type fakeChatSession struct{ reply string }

func (s *fakeChatSession) Prompt(ctx context.Context, text string) (string, error) {
	return s.reply, nil
}
func (s *fakeChatSession) Messages(ctx context.Context) ([]swarm.Message, error) { return nil, nil }
func (s *fakeChatSession) Close() error                                          { return nil }

// proposeReply builds an agent reply carrying a full-file proposal: a one-line
// message followed by content wrapped in the propose markers.
func proposeReply(msg, content string) string {
	return msg + "\n" + proposeOpen + "\n" + content + "\n" + proposeClose
}

// chatFixture builds a Server over a real git repo (seeded with a committed
// notes.md so a worktree can be cut from main) and swaps in a fake-backed chat
// manager. notes.md is a plain file — NOT an editable-coordination basename — so
// it proves the chat editor works over an ARBITRARY path (the single-file editor
// would reject it).
func chatFixture(t *testing.T, reply string) (*Server, string) {
	t.Helper()
	return chatFixtureClient(t, &fakeChatClient{reply: reply})
}

// chatFixtureClient is chatFixture with a caller-supplied swarm.Client, so a test
// can hold the client reference (e.g. to assert the seeded system prompt).
func chatFixtureClient(t *testing.T, client swarm.Client) (*Server, string) {
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
	s.chat = newChatManager(root, client)
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

// TestChatProposeYieldsDiff: a chat turn for an arbitrary path produces a pending
// proposal rendered as a unified diff (an added line), with nothing written yet.
func TestChatProposeYieldsDiff(t *testing.T) {
	s, _ := chatFixture(t, proposeReply("I appended gamma.", "alpha\nbeta\ngamma"))
	ctx := context.Background()
	path := "submodules/alpha/notes.md"

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, ok := sess.pending(); ok {
		t.Fatal("fresh session should have no proposal")
	}
	if err := sess.chat(ctx, "add a gamma line"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	proposed, ok := sess.pending()
	if !ok {
		t.Fatalf("expected a pending proposal (err=%q)", sess.errText())
	}
	if proposed != "alpha\nbeta\ngamma\n" {
		t.Fatalf("proposal not normalized: %q", proposed)
	}
	data := s.chatPanelData(ctx, sess)
	if data["HasProposal"] != true {
		t.Fatal("panel should report HasProposal")
	}
	rows := data["Rows"].([]editor.DiffRow)
	if !hasAddRow(rows, "gamma") {
		t.Fatalf("diff missing an added gamma row: %+v", rows)
	}
	// Nothing applied yet: the worktree file is still the base.
	if got := gitShow(t, sess.wtPath, "HEAD", path); got != "alpha\nbeta" {
		t.Fatalf("proposal must not touch the worktree before approval, got %q", got)
	}
}

// TestChatApproveWritesAndCommits: approving the proposal writes the file and
// commits it in the edit worktree (and clears the pending proposal).
func TestChatApproveWritesAndCommits(t *testing.T) {
	s, _ := chatFixture(t, proposeReply("I appended gamma.", "alpha\nbeta\ngamma"))
	ctx := context.Background()
	path := "submodules/alpha/notes.md"

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.chat(ctx, "add a gamma line"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	before := gitShow(t, sess.wtPath, "HEAD", path)
	if err := sess.approve(ctx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// On disk in the worktree.
	onDisk, err := os.ReadFile(filepath.Join(sess.wtPath, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read worktree file: %v", err)
	}
	if string(onDisk) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("approve did not write the proposal: %q", string(onDisk))
	}
	// Committed on the edit branch's HEAD.
	if got := gitShow(t, sess.wtPath, "HEAD", path); got != "alpha\nbeta\ngamma" {
		t.Fatalf("approve did not commit (before=%q, after=%q)", before, got)
	}
	if _, ok := sess.pending(); ok {
		t.Fatal("proposal should be cleared after approval")
	}
}

// TestChatApproveCreatesNewFile: an arbitrary path that does not exist yet can be
// created — the base is empty and approve writes+commits the new file.
func TestChatApproveCreatesNewFile(t *testing.T) {
	s, _ := chatFixture(t, proposeReply("Created it.", "hello world"))
	ctx := context.Background()
	path := "submodules/alpha/fresh.md"

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.chat(ctx, "create it"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if err := sess.approve(ctx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := gitShow(t, sess.wtPath, "HEAD", path); got != "hello world" {
		t.Fatalf("new file not committed: %q", got)
	}
}

// TestChatRejectIsNoop: rejecting a proposal drops it and touches neither the
// worktree file nor the branch HEAD.
func TestChatRejectIsNoop(t *testing.T) {
	s, _ := chatFixture(t, proposeReply("I appended gamma.", "alpha\nbeta\ngamma"))
	ctx := context.Background()
	path := "submodules/alpha/notes.md"

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	headBefore, err := sess.wt.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if err := sess.chat(ctx, "add a gamma line"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, ok := sess.pending(); !ok {
		t.Fatal("expected a pending proposal before reject")
	}
	sess.reject()
	if _, ok := sess.pending(); ok {
		t.Fatal("reject should clear the proposal")
	}
	headAfter, err := sess.wt.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if headBefore != headAfter {
		t.Fatalf("reject must not commit: HEAD moved %s -> %s", headBefore, headAfter)
	}
	onDisk, _ := os.ReadFile(filepath.Join(sess.wtPath, filepath.FromSlash(path)))
	if string(onDisk) != "alpha\nbeta\n" {
		t.Fatalf("reject must not touch the worktree file: %q", string(onDisk))
	}
}

// TestChatNoMarkersNoProposal: a plain reply (a question/answer without the
// markers) yields no proposal — the human is never shown a spurious diff.
func TestChatNoMarkersNoProposal(t *testing.T) {
	s, _ := chatFixture(t, "Which section did you mean?")
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.chat(ctx, "change the thing"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, ok := sess.pending(); ok {
		t.Fatal("a reply without markers must not create a proposal")
	}
}

// TestCleanRepoPathRejectsTraversal locks the path guard directly.
func TestCleanRepoPathRejectsTraversal(t *testing.T) {
	bad := []string{"", "  ", "..", "../evil", "/etc/passwd", "submodules/../../x", ".git/config", ".git"}
	for _, p := range bad {
		if _, err := cleanRepoPath(p); err == nil {
			t.Errorf("cleanRepoPath(%q) should have failed", p)
		}
	}
	good := map[string]string{
		"submodules/alpha/notes.md": "submodules/alpha/notes.md",
		"./submodules/alpha/ROI.md": "submodules/alpha/ROI.md",
		"AGENTS.md":                 "AGENTS.md",
	}
	for in, want := range good {
		got, err := cleanRepoPath(in)
		if err != nil {
			t.Errorf("cleanRepoPath(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Errorf("cleanRepoPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEditEntryRejectsTraversal: the HTTP entrypoint maps a traversal path to 400
// (editor.Open -> ValidateFile), so no worktree or session is created.
func TestEditEntryRejectsTraversal(t *testing.T) {
	s, _ := chatFixture(t, "")
	req := httptest.NewRequest("POST", "/edit", strings.NewReader("path=../evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("traversal path should be 400, got %d", w.Code)
	}
}

// TestEditEntryOpensPublishingEditor: GET /edit?path=<editable> opens a PUBLISHING
// editor session (internal/editor) and redirects to its /editor/ page, whose shell
// renders the target. This is the routing that replaced the retired chat-diff
// surface, so an approved edit publishes to main instead of stranding on a
// throwaway branch.
func TestEditEntryOpensPublishingEditor(t *testing.T) {
	s, _ := chatFixture(t, "")
	w := get(t, s, "/edit?path=submodules/alpha/ROI.md")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("edit entry should redirect (303), got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/editor/edit-") {
		t.Fatalf("edit link must open the publishing editor (/editor/...), got %q", loc)
	}
	if page := get(t, s, loc).Body.String(); !strings.Contains(page, "submodules/alpha/ROI.md") {
		t.Fatalf("editor page did not render the target file: %s", page)
	}
	panel := get(t, s, loc+"/panel")
	if panel.Code != http.StatusOK {
		t.Fatalf("editor panel status %d", panel.Code)
	}
}

// TestEditEntryRejectsNonEditable: a path that is not an editable coordination
// file (an arbitrary repo file) is a 400 — the publishing editor only edits the
// declared coordination set, so no session or worktree is created.
func TestEditEntryRejectsNonEditable(t *testing.T) {
	s, _ := chatFixture(t, "")
	w := get(t, s, "/edit?path=submodules/alpha/notes.md")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-editable path should be 400, got %d", w.Code)
	}
}

// TestChatPanelWiring locks the scroll-preserve/pin contract on the polled chat
// pane and the diff pane (mirrors the editor_panel wiring test).
func TestChatPanelWiring(t *testing.T) {
	s, _ := chatFixture(t, "")
	panel := renderTmpl(t, s, "chatedit_panel.html", map[string]interface{}{"ID": "c1", "Path": "submodules/alpha/notes.md"})
	for _, want := range []string{`id="chatedit-chat"`, `id="chatedit-diff"`, "data-scroll-preserve", "data-scroll-pin"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("chatedit_panel.html missing %q:\n%s", want, panel)
		}
	}
}
