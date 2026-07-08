package web

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// blockingChatSession is a swarm.Session whose Prompt blocks until the test
// closes unblock, so a test can deterministically observe the busy/connecting/
// live-parts window of an in-flight turn without a fixed sleep. started is
// closed the first time Prompt is entered, so a test can wait for the
// background turn to actually begin before asserting on it. Messages returns a
// fixed, in-flight-looking assistant message (parts) throughout — the live
// preview a real opencode session would report while a turn runs.
type blockingChatSession struct {
	reply string
	parts []swarm.Part

	startOnce sync.Once
	started   chan struct{}
	unblock   chan struct{}
}

func newBlockingChatSession(reply string, parts []swarm.Part) *blockingChatSession {
	return &blockingChatSession{reply: reply, parts: parts, started: make(chan struct{}), unblock: make(chan struct{})}
}

func (s *blockingChatSession) Prompt(ctx context.Context, text string) (string, error) {
	s.startOnce.Do(func() { close(s.started) })
	select {
	case <-s.unblock:
		return s.reply, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *blockingChatSession) Messages(ctx context.Context) ([]swarm.Message, error) {
	return []swarm.Message{{ID: "asst-1", Role: "assistant", Parts: s.parts}}, nil
}

func (s *blockingChatSession) Close() error { return nil }

// blockingChatClient is a swarm.Client whose Open blocks until openGate is
// closed (nil = connect immediately), so a test can observe the "connecting"
// window deterministically before the underlying session becomes reachable.
type blockingChatClient struct {
	openGate chan struct{}
	sess     swarm.Session
}

func (c *blockingChatClient) Open(ctx context.Context, cwd, system string) (swarm.Session, error) {
	if c.openGate != nil {
		select {
		case <-c.openGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.sess, nil
}

// waitForTrue polls cond until it reports true or timeout passes, so a test can
// await an async goroutine's progress deterministically instead of a fixed
// sleep (mirrors swarm's waitForContains bounded-poll pattern).
func waitForTrue(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

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

// The GET/POST /edit HTTP entry point once redirected any ?path= straight into
// this package's chatManager (TestChatOpenHandlerRedirects /
// TestChatOpenHandlerRejectsTraversal, since removed): ai-edit-publish-to-main
// retired that entry — GET /edit now always opens through the publish-capable
// internal/editor Manager (editEntry in web.go; see
// TestEditEntryOpensPublishCapableEditor and friends in web_test.go). What
// remains here (chatManager.open/chat/approve/reject, exercised directly below)
// is the still-live engine backing the bootstrap wizard's LOCALS.md session.

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

// ---- chat-editor-snappy-polish ----

// TestChatConnStateLifecycle proves the connecting -> connected transition
// happens on its own, at session-open time (prewarm), never gated on the user
// sending a message: right after open the connect is still gated (openGate),
// so connState reads "connecting"; once the gate releases, it converges to
// "connected" without any turn ever having been sent.
func TestChatConnStateLifecycle(t *testing.T) {
	gate := make(chan struct{})
	client := &blockingChatClient{openGate: gate, sess: newBlockingChatSession("hi", nil)}
	s, _ := chatFixtureClient(t, client)
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := sess.connState(); got != "connecting" {
		t.Fatalf("connState = %q before the gated connect completes, want connecting", got)
	}
	close(gate)
	waitForTrue(t, 2*time.Second, func() bool { return sess.connState() == "connected" })
}

// TestChatConnStateError proves a failed connect surfaces as an explicit
// "error" state (with the failure text available) rather than leaving the
// panel stuck silently on "connecting" forever.
func TestChatConnStateError(t *testing.T) {
	s, _ := chatFixtureClient(t, erroringChatClient{})
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	waitForTrue(t, 2*time.Second, func() bool { return sess.connState() == "error" })
	if sess.connectError() == "" {
		t.Fatal("connectError should be populated once connState is error")
	}
}

// erroringChatClient always fails to connect (simulates an unreachable
// opencode server).
type erroringChatClient struct{}

var errFakeUnreachable = errors.New("fake opencode server unreachable")

func (erroringChatClient) Open(ctx context.Context, cwd, system string) (swarm.Session, error) {
	return nil, errFakeUnreachable
}

// TestChatStartChatIsAsyncWithLiveParts proves startChat returns immediately
// with the user's message already recorded and Busy visible — before the
// (gated) turn ever settles — and that the panel's LiveParts surface the
// in-flight assistant message's reasoning/tool-call breakdown with live status
// while busy. Once unblocked, the turn settles, the proposal is parsed, and the
// FINAL chatTurn carries the same structured parts for persistent history.
func TestChatStartChatIsAsyncWithLiveParts(t *testing.T) {
	liveParts := []swarm.Part{
		{ID: "r1", Type: "reasoning", Text: "thinking about gamma"},
		{ID: "t1", Type: "tool", Tool: "bash", Status: "running", Title: "run tests"},
	}
	bs := newBlockingChatSession(proposeReply("I appended gamma.", "alpha\nbeta\ngamma"), liveParts)
	s, _ := chatFixtureClient(t, &blockingChatClient{sess: bs})
	ctx := context.Background()
	path := "submodules/alpha/notes.md"

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.startChat("add a gamma line"); err != nil {
		t.Fatalf("startChat: %v", err)
	}
	// Immediately visible, before the turn is unblocked: the message and busy.
	log := sess.logCopy()
	if len(log) != 1 || log[0].Role != "user" || log[0].Text != "add a gamma line" {
		t.Fatalf("user message not recorded synchronously by startChat: %+v", log)
	}
	if !sess.isBusy() {
		t.Fatal("session should be busy immediately after startChat, before the turn settles")
	}

	select {
	case <-bs.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background turn never reached Prompt")
	}

	data := s.chatPanelData(ctx, sess)
	if data["Busy"] != true {
		t.Fatal("panel should report Busy while the turn is in flight")
	}
	live, _ := data["LiveParts"].([]swarm.Part)
	if len(live) != 2 || live[0].Type != "reasoning" || live[1].Status != "running" {
		t.Fatalf("panel should surface the live in-flight parts: %+v", live)
	}

	close(bs.unblock)
	waitForTrue(t, 2*time.Second, func() bool { return !sess.isBusy() })

	log = sess.logCopy()
	if len(log) != 2 || log[1].Role != "agent" {
		t.Fatalf("agent turn not recorded after settling: %+v", log)
	}
	if len(log[1].Parts) != 2 || log[1].Parts[1].Status != "running" {
		t.Fatalf("final turn should carry the structured parts breakdown: %+v", log[1].Parts)
	}
	if _, ok := sess.pending(); !ok {
		t.Fatal("expected a pending proposal once the turn settles")
	}
}

// TestChatMessageHandlerReturnsBeforeTurnSettles drives the real HTTP handler
// (chatMessage) end to end: the POST must return the panel with the message
// already rendered and a "working" state WITHOUT waiting for the gated turn to
// finish, proving the handler itself is non-blocking (not just the underlying
// session methods).
func TestChatMessageHandlerReturnsBeforeTurnSettles(t *testing.T) {
	bs := newBlockingChatSession("no proposal, just an answer", nil)
	s, _ := chatFixtureClient(t, &blockingChatClient{sess: bs})
	ctx := context.Background()
	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	w := postForm(t, s, "/edit/"+sess.ID+"/message", url.Values{"message": {"hello"}})
	if w.Code != 200 {
		t.Fatalf("POST /edit/{id}/message = %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, "hello") {
		t.Fatalf("response should already render the user's message:\n%s", body)
	}
	if !strings.Contains(body, "working") {
		t.Fatalf("response should show the working state while the turn is in flight:\n%s", body)
	}
	close(bs.unblock)
	waitForTrue(t, 2*time.Second, func() bool { return !sess.isBusy() })
}

// TestChatPanelRendersConnAndAgentParts locks the new panel markup: distinct
// connecting/connected/working/error badges, and every agent-output part
// (reasoning + tool call) rendered as an expandable <details> with a live
// status badge, for both the DURING-the-turn preview (LiveParts) and settled
// per-turn history (Log[i].Parts).
func TestChatPanelRendersConnAndAgentParts(t *testing.T) {
	s, _ := chatFixture(t, "")

	connecting := renderTmpl(t, s, "chatedit_panel.html", map[string]interface{}{
		"ID": "c1", "Path": "submodules/alpha/notes.md", "ConnState": "connecting",
	})
	if !strings.Contains(connecting, `state connecting`) {
		t.Fatalf("panel missing the connecting state:\n%s", connecting)
	}

	errPanel := renderTmpl(t, s, "chatedit_panel.html", map[string]interface{}{
		"ID": "c1", "Path": "submodules/alpha/notes.md", "ConnState": "error", "ConnectError": "dial tcp: refused",
	})
	if !strings.Contains(errPanel, `state error`) || !strings.Contains(errPanel, "dial tcp: refused") {
		t.Fatalf("panel missing the error state/detail:\n%s", errPanel)
	}

	working := renderTmpl(t, s, "chatedit_panel.html", map[string]interface{}{
		"ID": "c1", "Path": "submodules/alpha/notes.md", "ConnState": "connected", "Busy": true,
		"LiveParts": []swarm.Part{
			{Type: "reasoning", Text: "considering the change"},
			{Type: "tool", Tool: "bash", Status: "running", Title: "run go test", Input: map[string]any{"cmd": "go test ./..."}},
		},
	})
	for _, want := range []string{
		"state working", "<details", "considering the change", "run go test",
		`class="part-status running"`, "cmd", "go test ./...",
	} {
		if !strings.Contains(working, want) {
			t.Fatalf("working panel missing %q:\n%s", want, working)
		}
	}

	settled := renderTmpl(t, s, "chatedit_panel.html", map[string]interface{}{
		"ID": "c1", "Path": "submodules/alpha/notes.md", "ConnState": "connected", "Busy": false,
		"Log": []chatTurn{
			{Role: "agent", Text: "Done.", Parts: []swarm.Part{
				{Type: "tool", Tool: "write", Status: "completed", Title: "write notes.md", Output: "wrote 3 lines"},
			}},
		},
	})
	for _, want := range []string{"state connected", "write notes.md", `class="part-status completed"`, "wrote 3 lines"} {
		if !strings.Contains(settled, want) {
			t.Fatalf("settled panel missing %q:\n%s", want, settled)
		}
	}
	// "working" must NOT appear once the turn has settled and nothing is busy.
	if strings.Contains(settled, "state working") {
		t.Fatalf("settled panel must not show the working state:\n%s", settled)
	}
}

// TestChatPanelDataColorizesRecognizedLanguage proves chatPanelData renders the
// diff through RenderDiffFile (language/markdown syntax highlighting) using the
// session's own path, not the plain RenderDiff.
func TestChatPanelDataColorizesRecognizedLanguage(t *testing.T) {
	s, _ := chatFixture(t, proposeReply("Added main.", "package main\n\nfunc main() {}\n"))
	ctx := context.Background()
	path := "submodules/alpha/main.go"

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.chat(ctx, "create main.go"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	data := s.chatPanelData(ctx, sess)
	rows := data["Rows"].([]editor.DiffRow)
	var gotTok bool
	for _, r := range rows {
		if strings.Contains(string(r.HTML), `class="tok-`) {
			gotTok = true
			break
		}
	}
	if !gotTok {
		t.Fatalf("expected a syntax-highlighted (tok-*) row for a .go file: %+v", rows)
	}
}

// TestChatStartChatRejectsWhileBusy proves a turn already in flight rejects a
// second startChat with errBusy instead of queuing or clobbering it, matching
// the previous synchronous chat's one-turn-at-a-time contract.
func TestChatStartChatRejectsWhileBusy(t *testing.T) {
	bs := newBlockingChatSession("ok", nil)
	s, _ := chatFixtureClient(t, &blockingChatClient{sess: bs})
	ctx := context.Background()
	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.startChat("first"); err != nil {
		t.Fatalf("startChat: %v", err)
	}
	if err := sess.startChat("second"); err != errBusy {
		t.Fatalf("startChat while busy = %v, want errBusy", err)
	}
	close(bs.unblock)
	waitForTrue(t, 2*time.Second, func() bool { return !sess.isBusy() })
}

// TestBootstrapAgentOptimisticSendWiring locks the client-side markup that
// makes the user's own message appear immediately on send: a stable form id,
// the polling trigger on #chatedit (so progress/completion are picked up with
// no further user action), and the optimistic-echo script wired to it.
func TestBootstrapAgentOptimisticSendWiring(t *testing.T) {
	s, _ := chatFixture(t, "")
	body := renderTmpl(t, s, "bootstrap_agent.html", map[string]interface{}{"ID": "c1", "Path": "LOCALS.md"})
	for _, want := range []string{
		`id="chatedit-form"`,
		`hx-trigger="load, every 1500ms"`,
		"getElementById('chatedit-form')",
		"getElementById('chatedit-chat')",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("bootstrap_agent.html missing %q:\n%s", want, body)
		}
	}
}
