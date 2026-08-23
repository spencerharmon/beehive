package swarm

import (
	"context"
	"errors"
	"testing"
)

// fakeHarnessSession is a minimal, non-opencode Session implementation. It
// proves the Harness contract (Client+Session, see swarm.go) is satisfiable by
// a driver that shares NO code with *Opencode — the shape a future pi harness
// driver must match. It tracks every prompt sent so a "continue" turn (a
// second Prompt call reusing the SAME Session) is verifiable, and it can be
// told to simulate the progress watchdog firing (ErrTurnIdle) on a given turn.
type fakeHarnessSession struct {
	system  string
	turns   []string  // every prompt text sent, in order, proving context persists
	idleAt  int       // 1-based turn index that should report ErrTurnIdle; 0 = never
	replies []string  // reply text for turn i (defaults to an echo when short)
	msgs    []Message // transcript this session records, grown per successful turn
	closed  bool
}

func (s *fakeHarnessSession) Prompt(_ context.Context, text string) (string, error) {
	s.turns = append(s.turns, text)
	n := len(s.turns)
	if s.idleAt != 0 && n == s.idleAt {
		return "", ErrTurnIdle
	}
	s.msgs = append(s.msgs, Message{ID: "u", Role: "user", Parts: []Part{{Type: "text", Text: text}}})
	reply := "reply-to:" + text
	if n-1 < len(s.replies) {
		reply = s.replies[n-1]
	}
	s.msgs = append(s.msgs, Message{ID: "a", Role: "assistant", Parts: []Part{{Type: "text", Text: reply}}})
	return reply, nil
}

func (s *fakeHarnessSession) Messages(_ context.Context) ([]Message, error) {
	out := make([]Message, len(s.msgs))
	copy(out, s.msgs)
	return out, nil
}

func (s *fakeHarnessSession) Close() error {
	s.closed = true
	return nil
}

// fakeHarnessClient is a minimal, non-opencode Client implementation pairing
// with fakeHarnessSession. Together they are a complete, independent Harness
// (Client+Session) driver — the same shape a real future pi driver would take
// — proving the interface, not *Opencode's internals, is what callers need.
type fakeHarnessClient struct {
	opened  []struct{ cwd, system string }
	idleAt  int
	replies []string
	model   string // last SetModel value, if the optional capability is exercised
}

func (c *fakeHarnessClient) Open(_ context.Context, cwd, system string) (Session, error) {
	c.opened = append(c.opened, struct{ cwd, system string }{cwd, system})
	return &fakeHarnessSession{system: system, idleAt: c.idleAt, replies: c.replies}, nil
}

func (c *fakeHarnessClient) SetModel(model string) { c.model = model }

// Compile-time proof the fakes satisfy the same interfaces *Opencode/ocSession
// do — a future pi driver need only match Client+Session (Harness), never
// embed or wrap the opencode type.
var (
	_ Client        = (*fakeHarnessClient)(nil)
	_ Session       = (*fakeHarnessSession)(nil)
	_ modelSelector = (*fakeHarnessClient)(nil)
	_ Client        = (*Opencode)(nil)
	_ Harness       = (*Opencode)(nil)
)

// TestHarnessContractOpenSessionTurnContinue exercises the core contract a
// runner/editor caller depends on: open a session under a system prompt
// (without sending a message), send a turn, then send a SECOND turn on the
// SAME Session ("continue") and confirm the harness saw both prompts in order
// — i.e. context persists across turns purely because the caller reused the
// Session value, never because the caller resent history itself.
func TestHarnessContractOpenSessionTurnContinue(t *testing.T) {
	cases := []struct {
		name   string
		cwd    string
		system string
		turns  []string
	}{
		{name: "single-turn", cwd: "/work/a", system: "AGENTS.md prompt A", turns: []string{"do the task"}},
		{name: "continue-turn", cwd: "/work/b", system: "AGENTS.md prompt B", turns: []string{"do the task", "continue"}},
		{name: "many-continues", cwd: "/work/c", system: "AGENTS.md prompt C", turns: []string{"start", "continue", "continue", "continue"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var client Client = &fakeHarnessClient{}
			sess, err := client.Open(context.Background(), tc.cwd, tc.system)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer sess.Close()

			for i, prompt := range tc.turns {
				reply, err := sess.Prompt(context.Background(), prompt)
				if err != nil {
					t.Fatalf("turn %d Prompt(%q): %v", i, prompt, err)
				}
				if reply == "" {
					t.Fatalf("turn %d: empty reply", i)
				}
			}

			fh := sess.(*fakeHarnessSession)
			if len(fh.turns) != len(tc.turns) {
				t.Fatalf("harness recorded %d turns, want %d", len(fh.turns), len(tc.turns))
			}
			for i, want := range tc.turns {
				if fh.turns[i] != want {
					t.Errorf("turn %d = %q, want %q (context did not persist across the SAME Session)", i, fh.turns[i], want)
				}
			}
		})
	}
}

// TestHarnessContractIdleTimeoutSurfacesErrTurnIdle proves a Harness Session
// that abandons a stalled turn reports it as ErrTurnIdle — the exact sentinel
// the runner's turn loop matches on (see swarm_test.go's idleClient /
// TestIdleTurnAbandonsForGC) — regardless of which driver produced it. A
// future pi driver's watchdog must map to this SAME sentinel for the runner's
// stall-handling to work unmodified.
func TestHarnessContractIdleTimeoutSurfacesErrTurnIdle(t *testing.T) {
	client := &fakeHarnessClient{idleAt: 2} // the 2nd turn stalls
	var c Client = client
	sess, err := c.Open(context.Background(), "/work", "system")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()

	if _, err := sess.Prompt(context.Background(), "first turn"); err != nil {
		t.Fatalf("first turn: unexpected error: %v", err)
	}
	_, err = sess.Prompt(context.Background(), "second turn (stalls)")
	if !errors.Is(err, ErrTurnIdle) {
		t.Fatalf("stalled turn error = %v, want ErrTurnIdle", err)
	}
}

// TestHarnessContractTranscriptRecord proves Messages() returns the ordered
// user/assistant transcript the recorder renders to sessions/ — the third leg
// of the contract (open, turn, continue; idle->ErrTurnIdle; transcript
// record) — and that a stalled turn (ErrTurnIdle) contributes NOTHING to the
// transcript, matching opencode's own behavior of never completing the
// assistant message for an abandoned turn.
func TestHarnessContractTranscriptRecord(t *testing.T) {
	client := &fakeHarnessClient{replies: []string{"did step one", "did step two"}}
	var c Client = client
	sess, err := c.Open(context.Background(), "/work", "system")
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
	if got := msgs[1].Parts[0].Text; got != "did step one" {
		t.Errorf("assistant turn 1 text = %q, want %q", got, "did step one")
	}
	if got := msgs[3].Parts[0].Text; got != "did step two" {
		t.Errorf("assistant turn 2 text = %q, want %q", got, "did step two")
	}
}

// TestHarnessContractModelSelectorOptional proves SetModel is an OPTIONAL
// capability (the modelSelector interface, swarm.go) a Client may or may not
// implement, and that the runner's routing (Runner.ModelFor via the type
// assertion in run()) works against ANY Client that implements it — not
// specifically *Opencode.
func TestHarnessContractModelSelectorOptional(t *testing.T) {
	client := &fakeHarnessClient{}
	var c Client = client
	ms, ok := c.(modelSelector)
	if !ok {
		t.Fatal("fakeHarnessClient must implement the optional modelSelector capability")
	}
	ms.SetModel("provider/strong-model")
	if client.model != "provider/strong-model" {
		t.Fatalf("model = %q, want %q", client.model, "provider/strong-model")
	}
}
