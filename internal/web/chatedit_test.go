package web

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
//
// err, gate, promptGate and messages are chat-editor-snappy-polish additions,
// all set ONCE at construction (a struct literal built before the fake is ever
// handed to a session) and never mutated afterward — so no lock is needed
// despite Open()/Prompt() running on the session's own eager-connect/turn
// goroutines, same as the pre-existing reply/cwd/system fields:
//
//	err        non-nil -> every Open() fails with this error (tests the
//	           "connecting -> error" state).
//	gate       non-nil -> Open() blocks until the test closes it (tests that
//	           "connecting" is observable and distinct from "connected").
//	promptGate non-nil -> every session's Prompt() blocks until the test closes
//	           it (tests that a turn in flight shows "working"/Busy/the user's
//	           own message before the reply arrives).
//	messages   returned by every session's Messages() call (tests the live and
//	           persisted reasoning/tool-call step breakdown).
type fakeChatClient struct {
	reply      string
	cwd        string
	system     string
	err        error
	gate       chan struct{}
	promptGate chan struct{}
	messages   []swarm.Message
}

func (f *fakeChatClient) Open(ctx context.Context, cwd, system string) (swarm.Session, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.cwd = cwd
	f.system = system
	if f.err != nil {
		return nil, f.err
	}
	return &fakeChatSession{reply: f.reply, promptGate: f.promptGate, messages: f.messages}, nil
}

type fakeChatSession struct {
	reply      string
	promptGate chan struct{}
	messages   []swarm.Message
}

func (s *fakeChatSession) Prompt(ctx context.Context, text string) (string, error) {
	if s.promptGate != nil {
		<-s.promptGate
	}
	return s.reply, nil
}
func (s *fakeChatSession) Messages(ctx context.Context) ([]swarm.Message, error) {
	return s.messages, nil
}
func (s *fakeChatSession) Close() error { return nil }

// waitUntilNotBusy polls until a background turn (startChat) settles or fails
// the test after a bounded timeout — the only way to observe an async turn's
// completion from outside, since chatSession exposes no completion channel.
func waitUntilNotBusy(t *testing.T, sess *chatSession) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !sess.isBusy() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the background turn to settle")
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
//
// Never strand the user on a bare spinner: connecting/connected/working/error
// are visibly distinct and accurate; the diff view is syntax-highlighted; and
// every agent output item (reasoning + tool calls) is shown, expandable, with
// live status throughout.

// TestChatSessionConnectingThenConnected proves the session shows "connecting"
// the instant it opens (the eager background connect attempt has not resolved
// yet) and "connected" once the opencode session is established — the exact
// distinction the acceptance criteria requires, gated deterministically (no
// sleeps) via a fake client that blocks Open() until the test releases it.
func TestChatSessionConnectingThenConnected(t *testing.T) {
	gate := make(chan struct{})
	client := &fakeChatClient{reply: "ok", gate: gate}
	s, _ := chatFixtureClient(t, client)
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := sess.connState(); got != stateConnecting {
		t.Fatalf("connState right after open = %q, want %q", got, stateConnecting)
	}
	if got := s.chatPanelData(ctx, sess)["ConnState"]; got != string(stateConnecting) {
		t.Fatalf("panel ConnState = %v, want %q", got, stateConnecting)
	}

	close(gate)
	// ensureConnected is idempotent/blocking: calling it directly from the test
	// waits for (whichever goroutine is performing) the single Open() attempt to
	// finish, with no sleep-based polling.
	if err := sess.ensureConnected(ctx); err != nil {
		t.Fatalf("ensureConnected: %v", err)
	}
	if got := sess.connState(); got != stateConnected {
		t.Fatalf("connState after connect = %q, want %q", got, stateConnected)
	}
}

// TestChatSessionConnectErrorSelfHeals proves a failed connect attempt surfaces
// as the "error" state (not a silent hang) AND that the retry-on-next-call
// semantics the original lazy-open had are preserved: a later successful
// attempt clears the error.
func TestChatSessionConnectErrorSelfHeals(t *testing.T) {
	boom := errors.New("boom: opencode unreachable")
	client := &fakeChatClient{err: boom}
	s, _ := chatFixtureClient(t, client)
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Drive the connect synchronously (races harmlessly with the eager
	// background attempt via connMu; ensureConnected is idempotent).
	if err := sess.ensureConnected(ctx); err == nil {
		t.Fatal("expected the connect attempt to fail")
	}
	if got := sess.connState(); got != stateConnError {
		t.Fatalf("connState = %q, want %q", got, stateConnError)
	}

	// Self-heal: clearing the fake's error and retrying (as a real chat turn
	// would via prompt -> ensureConnected) reconnects.
	client.err = nil
	if err := sess.ensureConnected(ctx); err != nil {
		t.Fatalf("retry ensureConnected: %v", err)
	}
	if got := sess.connState(); got != stateConnected {
		t.Fatalf("connState after retry = %q, want %q", got, stateConnected)
	}
}

// TestChatPanelDataSurfacesConnectErrorText proves a connect failure's own
// reason reaches the panel's existing Error field (not just a mute "error"
// badge with no explanation), and clears once a retry connects.
func TestChatPanelDataSurfacesConnectErrorText(t *testing.T) {
	boom := errors.New("boom: opencode unreachable")
	client := &fakeChatClient{err: boom}
	s, _ := chatFixtureClient(t, client)
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.ensureConnected(ctx); err == nil {
		t.Fatal("expected the connect attempt to fail")
	}
	data := s.chatPanelData(ctx, sess)
	if data["ConnState"] != string(stateConnError) {
		t.Fatalf("ConnState = %v, want %q", data["ConnState"], stateConnError)
	}
	if got, _ := data["Error"].(string); !strings.Contains(got, "boom") {
		t.Fatalf("panel Error = %q, want it to surface the connect failure", got)
	}

	client.err = nil
	if err := sess.ensureConnected(ctx); err != nil {
		t.Fatalf("retry ensureConnected: %v", err)
	}
	data2 := s.chatPanelData(ctx, sess)
	if data2["ConnState"] != string(stateConnected) {
		t.Fatalf("ConnState after retry = %v, want %q", data2["ConnState"], stateConnected)
	}
	if got, _ := data2["Error"].(string); got != "" {
		t.Fatalf("panel Error after a successful retry = %q, want empty", got)
	}
}

// TestChatStartChatShowsUserMessageAndWorkingBeforeReply is the core "never
// strand the user on a bare spinner" acceptance: startChat's own message
// renders immediately (before the agent has replied), Busy/ConnState=working
// show right away, and the final agent reply appears once the gated turn
// settles.
func TestChatStartChatShowsUserMessageAndWorkingBeforeReply(t *testing.T) {
	promptGate := make(chan struct{})
	client := &fakeChatClient{reply: "ok", promptGate: promptGate}
	s, _ := chatFixtureClient(t, client)
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.ensureConnected(ctx); err != nil {
		t.Fatalf("ensureConnected: %v", err)
	}

	if err := sess.startChat(context.Background(), "hello"); err != nil {
		t.Fatalf("startChat: %v", err)
	}
	// The user's own message and the working state are visible IMMEDIATELY —
	// startChat's beginTurn runs synchronously before the goroutine dispatches,
	// so no polling is needed to observe this.
	data := s.chatPanelData(ctx, sess)
	log, _ := data["Log"].([]chatLogEntry)
	if len(log) != 1 || log[0].Role != "user" || log[0].Text != "hello" {
		t.Fatalf("user message should render immediately: %+v", log)
	}
	if data["Busy"] != true {
		t.Fatal("session should be busy while the reply is gated")
	}
	if data["ConnState"] != string(stateWorking) {
		t.Fatalf("ConnState = %v, want %q", data["ConnState"], stateWorking)
	}

	// A second send while busy is ignored (mirrors editor/resolve-agent), never
	// double-dispatches or corrupts the log.
	if err := sess.startChat(context.Background(), "again"); err != errBusy {
		t.Fatalf("startChat while busy = %v, want errBusy", err)
	}

	close(promptGate)
	waitUntilNotBusy(t, sess)
	final := s.chatPanelData(ctx, sess)
	log2 := final["Log"].([]chatLogEntry)
	if len(log2) != 2 || log2[1].Role != "agent" || !strings.Contains(string(log2[1].HTML), "ok") {
		t.Fatalf("agent reply should be appended once the turn settles: %+v", log2)
	}
	if final["ConnState"] != string(stateConnected) {
		t.Fatalf("ConnState after settling = %v, want %q", final["ConnState"], stateConnected)
	}
}

// TestChatLiveStepsWhileBusy proves EVERY agent output item — reasoning AND
// tool calls — is surfaced while a turn is in flight (polled via Messages, the
// same public API the recorder uses; no SSE plumbing crosses the swarm/web
// boundary), and that the breakdown persists on the completed log entry
// afterward rather than vanishing once the turn settles.
func TestChatLiveStepsWhileBusy(t *testing.T) {
	promptGate := make(chan struct{})
	inFlight := []swarm.Message{
		{ID: "u1", Role: "user", Parts: []swarm.Part{{Type: "text", Text: "go"}}},
		{ID: "a1", Role: "assistant", Parts: []swarm.Part{
			{ID: "p1", Type: "reasoning", Text: "let me check the file first"},
			{ID: "p2", Type: "tool", Tool: "bash", Title: "ls -la", Status: "running"},
			{ID: "p3", Type: "text", Text: "ignored: duplicates the turn's own Text"},
		}},
	}
	client := &fakeChatClient{reply: "done", promptGate: promptGate, messages: inFlight}
	s, _ := chatFixtureClient(t, client)
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.ensureConnected(ctx); err != nil {
		t.Fatalf("ensureConnected: %v", err)
	}
	if err := sess.startChat(context.Background(), "go"); err != nil {
		t.Fatalf("startChat: %v", err)
	}

	data := s.chatPanelData(ctx, sess)
	live, ok := data["LiveSteps"].([]swarm.Part)
	if !ok || len(live) != 2 {
		t.Fatalf("LiveSteps = %#v, want the 2 reasoning/tool parts (text/step markers filtered)", data["LiveSteps"])
	}
	if live[0].Type != "reasoning" || live[0].Text != "let me check the file first" {
		t.Errorf("LiveSteps[0] = %+v, want the reasoning part", live[0])
	}
	if live[1].Type != "tool" || live[1].Tool != "bash" || live[1].Status != "running" {
		t.Errorf("LiveSteps[1] = %+v, want the running bash tool call", live[1])
	}

	close(promptGate)
	waitUntilNotBusy(t, sess)
	log := sess.logCopy()
	if len(log) != 2 {
		t.Fatalf("expected 2 log turns after settling, got %d: %+v", len(log), log)
	}
	if len(log[1].Parts) != 2 || log[1].Parts[0].Type != "reasoning" || log[1].Parts[1].Type != "tool" {
		t.Fatalf("completed turn should retain its Parts breakdown: %+v", log[1].Parts)
	}
	// Busy is now false: the panel must fall back cleanly (no stale LiveSteps).
	if got, _ := s.chatPanelData(ctx, sess)["LiveSteps"].([]swarm.Part); len(got) != 0 {
		t.Fatalf("LiveSteps after settling = %#v, want empty/nil (idle)", got)
	}
}

// TestChatPanelDataHighlightsRecognizedLanguage proves the chat-diff view
// syntax-highlights a recognized language: a .go path's proposed content gets
// classed <span> tokens (e.g. "package"/"func" as keywords), not just the
// plain escape.
func TestChatPanelDataHighlightsRecognizedLanguage(t *testing.T) {
	s, _ := chatFixture(t, proposeReply("Created it.", "package main\n\nfunc main() {}\n"))
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/main.go")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.chat(ctx, "create main.go"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	rows := s.chatPanelData(ctx, sess)["Rows"].([]editor.DiffRow)
	found := false
	for _, r := range rows {
		if strings.Contains(string(r.HTML), `class="hl-kw"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a highlighted keyword span in the diff rows: %+v", rows)
	}
}

// TestChatPanelDataFallsBackForUnrecognizedPath proves a file type chroma does
// not recognize keeps today's plain diff rendering (no hl- spans, no error) —
// the "common languages" acceptance never breaks an uncommon file.
func TestChatPanelDataFallsBackForUnrecognizedPath(t *testing.T) {
	s, _ := chatFixture(t, proposeReply("Appended.", "alpha\nbeta\ngamma"))
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.chat(ctx, "add a gamma line"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	rows := s.chatPanelData(ctx, sess)["Rows"].([]editor.DiffRow)
	if !hasAddRow(rows, "gamma") {
		t.Fatalf("diff missing an added gamma row: %+v", rows)
	}
	for _, r := range rows {
		if strings.Contains(string(r.HTML), `class="hl-`) {
			t.Fatalf("notes.md is not chroma-recognized; unexpected highlight span: %+v", r)
		}
	}
}

// TestChatEditPanelRendersConnStateAndSteps is the template-shape lock for the
// new markup: the conn-state badge and an expandable per-step <details> both
// render from real session/panel data (chatPanelWiring's minimal fixture
// deliberately omits these fields, so this test drives the real pipeline), for
// BOTH kinds of agent output the acceptance criteria names — reasoning AND a
// tool call — not just one.
func TestChatEditPanelRendersConnStateAndSteps(t *testing.T) {
	inFlight := []swarm.Message{
		{ID: "a1", Role: "assistant", Parts: []swarm.Part{
			{ID: "p0", Type: "reasoning", Text: "checking the test suite first"},
			{ID: "p1", Type: "tool", Tool: "bash", Title: "go test ./...", Status: "completed", Output: "ok"},
		}},
	}
	promptGate := make(chan struct{})
	client := &fakeChatClient{reply: "ok", promptGate: promptGate, messages: inFlight}
	s, _ := chatFixtureClient(t, client)
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.ensureConnected(ctx); err != nil {
		t.Fatalf("ensureConnected: %v", err)
	}
	if err := sess.startChat(context.Background(), "run the tests"); err != nil {
		t.Fatalf("startChat: %v", err)
	}
	body := renderTmpl(t, s, "chatedit_panel.html", s.chatPanelData(ctx, sess))
	for _, want := range []string{
		`conn-state working`, "Working…",
		"Reasoning", "checking the test suite first",
		"bash", "go test ./...", "completed",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("chatedit_panel.html missing %q while busy:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "<details"); n != 2 {
		t.Fatalf("expected 2 expandable steps (reasoning + tool call), found %d:\n%s", n, body)
	}
	close(promptGate)
	waitUntilNotBusy(t, sess)

	body2 := renderTmpl(t, s, "chatedit_panel.html", s.chatPanelData(ctx, sess))
	if !strings.Contains(body2, "conn-state connected") {
		t.Fatalf("chatedit_panel.html should show connected once idle:\n%s", body2)
	}
	if n := strings.Count(body2, "<details"); n != 2 {
		t.Fatalf("both completed steps should stay expandable after settling, found %d:\n%s", n, body2)
	}
}

// TestChatEditPanelClearsWorkingOnIdle is the chat-editor-working-indicator-clear
// acceptance: while a turn is in flight the panel shows the working/spinner
// bubble AND re-arms the self-perpetuating poll; the instant the turn goes idle
// (the completed-turn signal Prompt blocks on) the very next render drops the
// working bubble AND the poll node, and swaps in the rendered (markdown->HTML)
// reply — no manual refresh. The busy re-arm node also carries a per-render
// unique id so idiomorph replaces (never preserves) it, which is what keeps the
// poll firing until idle instead of freezing after one tick.
func TestChatEditPanelClearsWorkingOnIdle(t *testing.T) {
	promptGate := make(chan struct{})
	client := &fakeChatClient{reply: "All **done** now.", promptGate: promptGate}
	s, _ := chatFixtureClient(t, client)
	ctx := context.Background()

	sess, err := s.chat.open(ctx, "submodules/alpha/notes.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.ensureConnected(ctx); err != nil {
		t.Fatalf("ensureConnected: %v", err)
	}
	if err := sess.startChat(context.Background(), "make a change"); err != nil {
		t.Fatalf("startChat: %v", err)
	}

	// While busy: the working bubble AND the re-arm poll node (with a unique id)
	// are present so the loop keeps refreshing until the turn settles.
	busy := renderTmpl(t, s, "chatedit_panel.html", s.chatPanelData(ctx, sess))
	for _, want := range []string{`msg agent busy`, "Working…", `id="chatedit-poll-`, `hx-trigger="load delay:1500ms"`} {
		if !strings.Contains(busy, want) {
			t.Fatalf("busy chatedit panel missing %q:\n%s", want, busy)
		}
	}

	close(promptGate)
	waitUntilNotBusy(t, sess)

	// Once idle: no working bubble, no spinner, and no re-arm poll node — the
	// panel has settled and stops polling. The reply is rendered markdown.
	idle := renderTmpl(t, s, "chatedit_panel.html", s.chatPanelData(ctx, sess))
	for _, gone := range []string{`msg agent busy`, "Working…", `id="chatedit-poll-`} {
		if strings.Contains(idle, gone) {
			t.Fatalf("idle chatedit panel must drop %q (working state must auto-clear):\n%s", gone, idle)
		}
	}
	if !strings.Contains(idle, "<strong>done</strong>") {
		t.Fatalf("idle chatedit panel must show the rendered (markdown->HTML) reply:\n%s", idle)
	}
}

