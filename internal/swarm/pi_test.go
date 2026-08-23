package swarm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePiProc is a fake pi RPC process: a goroutine reading commands off one
// end of an in-memory pipe and writing responses/events to the other,
// speaking the exact line-delimited JSON protocol pi's real --mode rpc
// process speaks (https://pi.dev/docs/latest/rpc). It stands in for the real
// pi binary so the driver is fully tested with no dependency on pi being
// installed. scripts maps a 1-based turn index to the canned assistant reply
// text pi should report via get_last_assistant_text for that turn; idleAt, if
// nonzero, is the 1-based turn index on which the fake goes silent forever
// (simulating a wedged/stalled pi process) instead of ever settling.
type fakePiProc struct {
	t         *testing.T
	scripts   []string
	idleAt    int
	turn      int
	stopped   chan struct{}
	replyText string   // last accepted prompt's text, echoed as this turn's "reply"
	prompts   []string // every prompt text accepted, in order
}

// newFakePi wires two os.Pipe pairs to stand in for a spawned process's
// stdin/stdout and starts the fake's protocol goroutine. It returns the
// driver-facing stdin writer / stdout reader (exactly what execSpawn would
// return for a real process) and a stop func.
func newFakePi(t *testing.T, scripts []string, idleAt int) (io.WriteCloser, io.Reader, func() error) {
	t.Helper()
	// driverStdin: driver writes commands here; fake reads from cmdR.
	cmdR, driverStdin := io.Pipe()
	// driverStdout: fake writes responses/events here; driver reads from evtR.
	evtR, fakeStdout := io.Pipe()

	f := &fakePiProc{t: t, scripts: scripts, idleAt: idleAt, stopped: make(chan struct{})}
	go f.run(cmdR, fakeStdout)

	stop := func() error {
		_ = driverStdin.Close()
		_ = cmdR.Close()
		_ = fakeStdout.Close()
		_ = evtR.Close()
		return nil
	}
	return driverStdin, evtR, stop
}

func (f *fakePiProc) writeLine(w io.Writer, v map[string]any) {
	buf, err := json.Marshal(v)
	if err != nil {
		f.t.Fatalf("fakePi marshal: %v", err)
	}
	buf = append(buf, '\n')
	if _, err := w.Write(buf); err != nil {
		return // driver stopped reading (session closed); nothing to do
	}
}

// run is the fake's whole protocol loop: read one JSONL command at a time and
// react. Only the commands the real *Pi driver actually issues are handled
// (prompt, abort, get_last_assistant_text, get_messages) — anything else is
// acked success:true so an unexpected extra command never wedges the test.
func (f *fakePiProc) run(cmdR io.Reader, out io.WriteCloser) {
	defer out.Close()
	sc := bufio.NewScanner(cmdR)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		var cmd struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(line, &cmd); err != nil {
			continue
		}
		switch cmd.Type {
		case "prompt":
			f.turn++
			f.prompts = append(f.prompts, cmd.Message)
			f.writeLine(out, map[string]any{"id": cmd.ID, "type": "response", "command": "prompt", "success": true})
			if f.idleAt != 0 && f.turn == f.idleAt {
				// Simulate a wedged turn: accept the prompt, then go silent
				// forever (never emit agent_settled, never emit anything
				// else) so the idle watchdog must fire.
				continue
			}
			reply := "reply-to:" + cmd.Message
			if f.turn-1 < len(f.scripts) {
				reply = f.scripts[f.turn-1]
			}
			f.replyText = reply
			// A couple of realistic in-between events so the transcript/idle
			// clock sees genuine turn activity, then settle.
			f.writeLine(out, map[string]any{"type": "agent_start"})
			f.writeLine(out, map[string]any{"type": "turn_start"})
			f.writeLine(out, map[string]any{
				"type": "message_update",
				"assistantMessageEvent": map[string]any{
					"type": "text_delta", "contentIndex": 0, "delta": reply,
				},
			})
			f.writeLine(out, map[string]any{"type": "turn_end"})
			f.writeLine(out, map[string]any{"type": "agent_end", "willRetry": false})
			f.writeLine(out, map[string]any{"type": "agent_settled"})
		case "get_last_assistant_text":
			f.writeLine(out, map[string]any{
				"id": cmd.ID, "type": "response", "command": "get_last_assistant_text",
				"success": true, "data": map[string]any{"text": f.replyText},
			})
		case "get_messages":
			f.writeLine(out, map[string]any{
				"id": cmd.ID, "type": "response", "command": "get_messages",
				"success": true, "data": map[string]any{"messages": f.messages()},
			})
		case "abort":
			f.writeLine(out, map[string]any{"id": cmd.ID, "type": "response", "command": "abort", "success": true})
		default:
			f.writeLine(out, map[string]any{"id": cmd.ID, "type": "response", "command": cmd.Type, "success": true})
		}
	}
	close(f.stopped)
}

// messages reconstructs pi's get_messages shape for every completed turn so
// far: one user message + one assistant text message per turn, matching what
// the driver sent and what it scripted as the reply.
func (f *fakePiProc) messages() []map[string]any {
	var out []map[string]any
	n := f.turn
	if f.idleAt != 0 && f.idleAt <= n {
		n = f.idleAt - 1 // the stalled turn produced no message
	}
	for i := 0; i < n; i++ {
		reply := "reply"
		if i < len(f.scripts) {
			reply = f.scripts[i]
		}
		prompt := "turn"
		if i < len(f.prompts) {
			prompt = f.prompts[i]
		}
		out = append(out,
			map[string]any{"role": "user", "content": prompt},
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": reply}}},
		)
	}
	return out
}

// piWithFake returns a *Pi whose spawn is wired to a fresh fakePiProc.
func piWithFake(t *testing.T, scripts []string, idleAt int, idle time.Duration) *Pi {
	t.Helper()
	return &Pi{
		IdleTimeout: idle,
		spawn: func(bin, dir string, args []string) (io.WriteCloser, io.Reader, func() error, error) {
			in, out, stop := newFakePi(t, scripts, idleAt)
			return in, out, stop, nil
		},
	}
}

// TestPiOpenPromptContinue proves *Pi/piSession satisfy the Harness contract
// end to end against the fake process: Open, a first turn, then a SECOND
// "continue" turn on the SAME Session, asserting the reply for each and that
// the fake (standing in for pi's own in-process conversation state) saw both
// prompts.
func TestPiOpenPromptContinue(t *testing.T) {
	p := piWithFake(t, []string{"did step one", "did step two"}, 0, 0)
	sess, err := p.Open(context.Background(), t.TempDir(), "system prompt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()

	reply1, err := sess.Prompt(context.Background(), "start")
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if reply1 != "did step one" {
		t.Fatalf("turn 1 reply = %q, want %q", reply1, "did step one")
	}

	reply2, err := sess.Prompt(context.Background(), "continue")
	if err != nil {
		t.Fatalf("turn 2 (continue): %v", err)
	}
	if reply2 != "did step two" {
		t.Fatalf("turn 2 reply = %q, want %q", reply2, "did step two")
	}
}

// TestPiIdleWatchdogSurfacesErrTurnIdle proves a turn the fake process never
// settles (simulating a wedged pi) is abandoned by the SAME idle watchdog
// semantics as Opencode and surfaces the exact ErrTurnIdle sentinel the
// runner's turn loop matches on, regardless of which harness produced it.
func TestPiIdleWatchdogSurfacesErrTurnIdle(t *testing.T) {
	p := piWithFake(t, nil, 1, 30*time.Millisecond)
	sess, err := p.Open(context.Background(), t.TempDir(), "system")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()

	_, err = sess.Prompt(context.Background(), "this turn stalls")
	if !errors.Is(err, ErrTurnIdle) {
		t.Fatalf("stalled turn error = %v, want ErrTurnIdle", err)
	}
}

// TestPiMessagesTranscriptShape proves Messages() returns the ordered
// user/assistant history in the SAME Message/Part shape opencode's Messages()
// produces, so the recorder renders an equivalent sessions/*.md transcript
// regardless of harness.
func TestPiMessagesTranscriptShape(t *testing.T) {
	p := piWithFake(t, []string{"first reply", "second reply"}, 0, 0)
	sess, err := p.Open(context.Background(), t.TempDir(), "system")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()

	if _, err := sess.Prompt(context.Background(), "start"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), "continue"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	msgs, err := sess.Messages(context.Background())
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4 (2 user + 2 assistant)", len(msgs))
	}
	wantRoles := []string{"user", "assistant", "user", "assistant"}
	for i, m := range msgs {
		if m.Role != wantRoles[i] {
			t.Errorf("message %d role = %q, want %q", i, m.Role, wantRoles[i])
		}
	}
	if got := msgs[1].Parts[0].Text; got != "first reply" {
		t.Errorf("assistant turn 1 text = %q, want %q", got, "first reply")
	}
	if got := msgs[3].Parts[0].Text; got != "second reply" {
		t.Errorf("assistant turn 2 text = %q, want %q", got, "second reply")
	}
}

// TestPiRecorderTranscriptMatchesOpencodeShape drives the SAME recorder used
// for opencode against a pi-backed Session and confirms the rendered
// sessions/*.md file has the identical shape (same header markers/ordering)
// as one rendered from an equivalent opencode-shaped message sequence — the
// on-repo transcript format stays git-derived-stats/tag-facet compatible
// regardless of which harness produced the turns.
func TestPiRecorderTranscriptMatchesOpencodeShape(t *testing.T) {
	p := piWithFake(t, []string{"did the work"}, 0, 0)
	sess, err := p.Open(context.Background(), t.TempDir(), "system")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()
	if _, err := sess.Prompt(context.Background(), "start"); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	dir := t.TempDir()
	rc := &recorder{
		sess:    sess,
		path:    filepath.Join(dir, "pi-session.md"),
		header:  "# pi-session\n",
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	if err := rc.snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	piRendered, err := os.ReadFile(rc.path)
	if err != nil {
		t.Fatalf("read pi transcript: %v", err)
	}

	// An equivalent opencode-shaped transcript (same messages, different
	// Session implementation entirely) must render the SAME shape.
	stub := &stubSession{msgs: func() []Message {
		return []Message{
			{ID: "u1", Role: "user", Parts: []Part{{Type: "text", Text: "start"}}},
			{ID: "a1", Role: "assistant", Parts: []Part{{Type: "text", Text: "did the work"}}},
		}
	}}
	rc2 := &recorder{
		sess:    stub,
		path:    filepath.Join(dir, "oc-session.md"),
		header:  "# pi-session\n",
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	if err := rc2.snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot (oc-shaped): %v", err)
	}
	ocRendered, err := os.ReadFile(rc2.path)
	if err != nil {
		t.Fatalf("read oc-shaped transcript: %v", err)
	}

	if !strings.Contains(string(piRendered), "did the work") {
		t.Fatalf("pi transcript missing assistant reply: %q", piRendered)
	}
	if string(piRendered) != string(ocRendered) {
		t.Fatalf("pi-driven transcript shape differs from opencode-shaped transcript:\npi:\n%s\noc:\n%s", piRendered, ocRendered)
	}
}

// Compile-time proof *Pi/piSession satisfy the same Harness contract
// (Client+Session, modelSelector) *Opencode does, matching harness_test.go's
// pattern for the fake harness.
var (
	_ Client        = (*Pi)(nil)
	_ Session       = (*piSession)(nil)
	_ modelSelector = (*Pi)(nil)
	_ Harness       = (*Pi)(nil)
)
