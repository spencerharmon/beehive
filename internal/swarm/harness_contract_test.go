// harness_contract_test.go is the standing lock-step-parity regression gate the
// ROI's "lock-step parity (the acceptance bar)" requires: ONE shared table-driven
// test suite run against BOTH the opencode driver (*Opencode, backed by a fake
// HTTP server — never a live opencode server) and the pi driver (*Pi, backed by
// pi_test.go's fakePiProc — never a real pi process), asserting every capability
// one harness has, the other must have too: session persistence across
// "continue" turns, streaming/token-settle honoring the idle watchdog
// (ErrTurnIdle on a simulated stall), model/temperature/max-tokens config
// plumbed through, transcript recording byte-identical in shape (sessions/*.md)
// regardless of harness, cancellation, and error surfacing.
//
// A future capability added to only one driver — a new config knob, a new
// transcript field, a new failure mode — must fail this suite until the other
// driver gets it too, per the ROI's "not done until the other has it" rule.
package swarm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- opencode fake transport --------------------------------------------

// fakeOCServer stands in for a live opencode server: one session, POST
// /session/<id>/message settles SYNCHRONOUSLY (mirroring the real server's
// blocking-POST behavior documented on ocSession.Prompt) unless the turn index
// matches idleAt, in which case the POST blocks until its request ctx is
// canceled (simulating a wedged turn) while GET /session/<id>/message keeps
// reporting the SAME transcript (no progress), so Opencode's idle watchdog must
// fire and cancel it. Every accepted POST body is captured so tests can assert
// model/temperature/maxTokens threading.
type fakeOCServer struct {
	t       *testing.T
	scripts []string
	idleAt  int
	errAt   int // 1-based turn index to fail with a 500 instead of settling

	mu       sync.Mutex
	turn     int
	lastBody map[string]any
	msgs     []ocFakeMsg
}

type ocFakeMsg struct {
	ID        string
	Role      string
	Text      string
	Completed bool
}

func (f *fakeOCServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/session":
		fmt.Fprint(w, `{"id":"s1"}`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/message") && !strings.HasSuffix(path, "/abort"):
		f.handlePrompt(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/abort"):
		fmt.Fprint(w, `{}`)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/message"):
		f.handlePoll(w)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeOCServer) handlePrompt(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.lastBody = body
	f.turn++
	turn := f.turn
	f.mu.Unlock()

	// Record the user turn text in the transcript up front (mirrors a real
	// server's stub-then-fill), so a stalled turn's user message is still
	// visible but its assistant half never completes.
	text := ""
	if parts, ok := body["parts"].([]any); ok && len(parts) > 0 {
		if p0, ok := parts[0].(map[string]any); ok {
			text, _ = p0["text"].(string)
		}
	}
	f.mu.Lock()
	f.msgs = append(f.msgs, ocFakeMsg{ID: fmt.Sprintf("u%d", turn), Role: "user", Text: text, Completed: true})
	f.mu.Unlock()

	if f.errAt != 0 && turn == f.errAt {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"simulated failure"}`)
		return
	}

	if f.idleAt != 0 && turn == f.idleAt {
		// Wedged turn: never respond until the caller's (turn) ctx is canceled by
		// the idle watchdog — mirroring the real server's fully-synchronous POST.
		<-r.Context().Done()
		return
	}

	reply := "reply-to:" + text
	f.mu.Lock()
	if turn-1 < len(f.scripts) {
		reply = f.scripts[turn-1]
	}
	f.msgs = append(f.msgs, ocFakeMsg{ID: fmt.Sprintf("a%d", turn), Role: "assistant", Text: reply, Completed: true})
	f.mu.Unlock()

	fmt.Fprintf(w, `{"info":{"id":"a%d","time":{"completed":1700000000000}},"parts":[{"type":"text","text":%q}]}`, turn, reply)
}

func (f *fakeOCServer) handlePoll(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var sb strings.Builder
	sb.WriteByte('[')
	for i, m := range f.msgs {
		if i > 0 {
			sb.WriteByte(',')
		}
		completed := "0"
		if m.Completed {
			completed = "1700000000000"
		}
		fmt.Fprintf(&sb, `{"info":{"id":%q,"role":%q,"time":{"completed":%s}},"parts":[{"type":"text","text":%q}]}`,
			m.ID, m.Role, completed, m.Text)
	}
	sb.WriteByte(']')
	w.Write([]byte(sb.String()))
}

func (f *fakeOCServer) capturedBody() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBody
}

// newOpencodeContractClient builds an *Opencode wired to a fake server that
// settles turns synchronously (or stalls/errors per idleAt/errAt), with tiny
// poll bounds so idle-watchdog tests run fast.
func newOpencodeContractClient(t *testing.T, scripts []string, idleAt, errAt int, idle time.Duration, model string, temperature float64, maxTokens int) (*Opencode, *httptest.Server) {
	oc, srv, _ := newOpencodeContractClientCaptured(t, scripts, idleAt, errAt, idle, model, temperature, maxTokens)
	return oc, srv
}

// newOpencodeContractClientCaptured is newOpencodeContractClient plus the
// backing fakeOCServer, so a test can inspect the last accepted request body
// (model/temperature/maxTokens threading) the same way pi's captured spawn
// args are inspected.
func newOpencodeContractClientCaptured(t *testing.T, scripts []string, idleAt, errAt int, idle time.Duration, model string, temperature float64, maxTokens int) (*Opencode, *httptest.Server, *fakeOCServer) {
	t.Helper()
	f := &fakeOCServer{t: t, scripts: scripts, idleAt: idleAt, errAt: errAt}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	oc := &Opencode{
		Base:        srv.URL,
		Model:       model,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		HTTP:        srv.Client(),
		IdleTimeout: idle,
	}
	oc.pollMin, oc.pollMax = time.Millisecond, 2*time.Millisecond
	return oc, srv, f
}

// ---- pi fake transport ----------------------------------------------------

// newPiContractClient builds a *Pi wired to fakePiProc (pi_test.go), capturing
// the exact spawn args (bin/dir/args) so tests can assert model/thinking
// threading the same way newOpencodeContractClient's captured body does.
func newPiContractClient(t *testing.T, scripts []string, idleAt, errAt int, idle time.Duration, model, thinking string) (*Pi, *[]string) {
	t.Helper()
	var capturedArgs []string
	p := &Pi{
		Model:       model,
		Thinking:    thinking,
		IdleTimeout: idle,
		spawn: func(bin, dir string, args []string) (io.WriteCloser, io.Reader, func() error, error) {
			capturedArgs = args
			in, out, stop := newFakePiContract(t, scripts, idleAt, errAt)
			return in, out, stop, nil
		},
	}
	return p, &capturedArgs
}

// fakePiContractProc extends fakePiProc's protocol with an errAt turn that
// replies success:false to "prompt" instead of settling, mirroring
// fakeOCServer's errAt.
type fakePiContractProc struct {
	fakePiProc
	errAt int
}

func newFakePiContract(t *testing.T, scripts []string, idleAt, errAt int) (io.WriteCloser, io.Reader, func() error) {
	t.Helper()
	cmdR, driverStdin := io.Pipe()
	evtR, fakeStdout := io.Pipe()

	f := &fakePiContractProc{fakePiProc: fakePiProc{t: t, scripts: scripts, idleAt: idleAt, stopped: make(chan struct{})}, errAt: errAt}
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

// run mirrors fakePiProc.run but fails the errAt-th prompt with success:false
// instead of settling, so pi's error-surfacing path can be exercised
// identically to opencode's 500 response.
func (f *fakePiContractProc) run(cmdR io.Reader, out io.WriteCloser) {
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
			if f.errAt != 0 && f.turn == f.errAt {
				f.writeLine(out, map[string]any{"id": cmd.ID, "type": "response", "command": "prompt", "success": false, "error": "simulated failure"})
				continue
			}
			f.writeLine(out, map[string]any{"id": cmd.ID, "type": "response", "command": "prompt", "success": true})
			if f.idleAt != 0 && f.turn == f.idleAt {
				continue // wedged: never settle
			}
			reply := "reply-to:" + cmd.Message
			if f.turn-1 < len(f.scripts) {
				reply = f.scripts[f.turn-1]
			}
			f.replyText = reply
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

// ---- the shared contract table --------------------------------------------

// contractDriver names one harness under test plus the constructor the shared
// suite calls to get a fresh Client for a given scripted scenario. Adding a
// third driver in the future means adding one more entry here — the suite
// itself never special-cases opencode or pi by name.
type contractDriver struct {
	name string
	// open builds a fresh Client for the given scripted replies plus an
	// optional idle-stall turn (1-based; 0 = never) and error turn (1-based;
	// 0 = never), with idle set as the driver's progress-watchdog timeout.
	open func(t *testing.T, scripts []string, idleAt, errAt int, idle time.Duration) Client
}

func contractDrivers() []contractDriver {
	return []contractDriver{
		{
			name: "opencode",
			open: func(t *testing.T, scripts []string, idleAt, errAt int, idle time.Duration) Client {
				oc, srv := newOpencodeContractClient(t, scripts, idleAt, errAt, idle, "anthropic/claude-3", 0, 0)
				t.Cleanup(srv.Close)
				return oc
			},
		},
		{
			name: "pi",
			open: func(t *testing.T, scripts []string, idleAt, errAt int, idle time.Duration) Client {
				p, _ := newPiContractClient(t, scripts, idleAt, errAt, idle, "anthropic/claude-3", "")
				return p
			},
		},
	}
}

// TestHarnessParitySessionPersistence proves BOTH drivers honor the SAME
// contract for session persistence across "continue" turns: opening one
// session and sending multiple Prompt calls on it must thread every prompt in
// order with distinct replies, on the opencode driver (fake HTTP transport)
// AND the pi driver (fake RPC-process transport) alike.
func TestHarnessParitySessionPersistence(t *testing.T) {
	for _, d := range contractDrivers() {
		t.Run(d.name, func(t *testing.T) {
			scripts := []string{"did step one", "did step two", "did step three"}
			client := d.open(t, scripts, 0, 0, 0)

			sess, err := client.Open(context.Background(), t.TempDir(), "system prompt")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer sess.Close()

			turns := []string{"start", "continue", "continue"}
			for i, prompt := range turns {
				reply, err := sess.Prompt(context.Background(), prompt)
				if err != nil {
					t.Fatalf("turn %d Prompt(%q): %v", i, prompt, err)
				}
				if reply != scripts[i] {
					t.Fatalf("turn %d reply = %q, want %q (context did not persist across the SAME Session)", i, reply, scripts[i])
				}
			}
		})
	}
}

// TestHarnessParityIdleWatchdog proves BOTH drivers' progress watchdog
// abandons a turn that produces no new transcript activity within IdleTimeout
// and surfaces the SAME ErrTurnIdle sentinel the runner's turn loop matches on
// — regardless of which harness produced it.
func TestHarnessParityIdleWatchdog(t *testing.T) {
	for _, d := range contractDrivers() {
		t.Run(d.name, func(t *testing.T) {
			client := d.open(t, nil, 1, 0, 30*time.Millisecond) // the 1st turn stalls
			sess, err := client.Open(context.Background(), t.TempDir(), "system")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer sess.Close()

			_, err = sess.Prompt(context.Background(), "this turn stalls")
			if !errors.Is(err, ErrTurnIdle) {
				t.Fatalf("stalled turn error = %v, want ErrTurnIdle", err)
			}
		})
	}
}

// TestHarnessParityErrorSurfacing proves BOTH drivers surface a genuine
// backend failure (opencode: HTTP 500; pi: prompt rejected success:false) as a
// non-nil, non-ErrTurnIdle error — a driver must never silently swallow a real
// failure or misreport it as an idle stall.
func TestHarnessParityErrorSurfacing(t *testing.T) {
	for _, d := range contractDrivers() {
		t.Run(d.name, func(t *testing.T) {
			client := d.open(t, nil, 0, 1, 0) // the 1st turn fails
			sess, err := client.Open(context.Background(), t.TempDir(), "system")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer sess.Close()

			_, err = sess.Prompt(context.Background(), "this turn fails")
			if err == nil {
				t.Fatal("Prompt: want a non-nil error for a backend failure, got nil")
			}
			if errors.Is(err, ErrTurnIdle) {
				t.Fatalf("Prompt: a genuine backend failure must not be reported as ErrTurnIdle: %v", err)
			}
		})
	}
}

// TestHarnessParityCancellation proves BOTH drivers honor caller ctx
// cancellation: a Prompt call whose ctx is already canceled before the turn
// can settle must return promptly with a non-nil error, never hang or ignore
// cancellation regardless of harness.
func TestHarnessParityCancellation(t *testing.T) {
	for _, d := range contractDrivers() {
		t.Run(d.name, func(t *testing.T) {
			// A turn that would otherwise stall forever (idleAt with IdleTimeout
			// disabled) so the ONLY thing that can end it is ctx cancellation.
			client := d.open(t, nil, 1, 0, 0)
			sess, err := client.Open(context.Background(), t.TempDir(), "system")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, perr := sess.Prompt(ctx, "this turn never settles")
				done <- perr
			}()
			select {
			case perr := <-done:
				if perr == nil {
					t.Fatal("Prompt: want a non-nil error when ctx is canceled mid-turn, got nil")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Prompt did not honor ctx cancellation within 5s")
			}
		})
	}
}

// TestHarnessParityModelConfigPlumbed proves the resolved model config reaches
// BOTH drivers' backend: opencode's request body carries the split
// providerID/modelID, and pi's spawn args carry --provider/--model, for the
// SAME "provider/model" config value.
func TestHarnessParityModelConfigPlumbed(t *testing.T) {
	const model = "anthropic/claude-3"

	t.Run("opencode", func(t *testing.T) {
		oc, srv, f := newOpencodeContractClientCaptured(t, []string{"ok"}, 0, 0, 0, model, 0.42, 1234)
		defer srv.Close()
		sess, err := oc.Open(context.Background(), "/wt", "system")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer sess.Close()
		if _, err := sess.Prompt(context.Background(), "hi"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		body := f.capturedBody()
		m, _ := body["model"].(map[string]any)
		if m["providerID"] != "anthropic" || m["modelID"] != "claude-3" {
			t.Fatalf("opencode body model = %v, want anthropic/claude-3 split", body["model"])
		}
	})

	t.Run("pi", func(t *testing.T) {
		p, args := newPiContractClient(t, []string{"ok"}, 0, 0, 0, model, "high")
		sess, err := p.Open(context.Background(), "/wt", "system")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer sess.Close()
		if _, err := sess.Prompt(context.Background(), "hi"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		got := strings.Join(*args, " ")
		if !strings.Contains(got, "--provider anthropic") || !strings.Contains(got, "--model claude-3") {
			t.Fatalf("pi spawn args = %q, want --provider anthropic --model claude-3", got)
		}
		if !strings.Contains(got, "--thinking high") {
			t.Fatalf("pi spawn args = %q, want --thinking high", got)
		}
	})
}

// TestHarnessParityModelKnobsInOpencodeBody re-asserts (alongside
// opencode_test.go's own unit tests) that temperature/maxTokens are threaded
// into the opencode request body — the config-plumbing half of the contract
// this suite exists to hold BOTH drivers to. pi's Temperature/MaxTokens fields
// are documented (pi.go) as reserved-but-unused (pi's RPC protocol has no
// per-session temperature/max-tokens knob), so the parity bar for pi is that
// setting them never breaks Open/Prompt — proven by
// TestHarnessParityModelConfigPlumbed constructing *Pi with those fields
// implicitly zero and every other contract test constructing pi drivers that
// never fail regardless of those fields.
func TestHarnessParityModelKnobsInOpencodeBody(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message") {
			_ = json.NewDecoder(r.Body).Decode(&captured)
			fmt.Fprint(w, `{"info":{"id":"a1","time":{"completed":1700000000000}},"parts":[{"type":"text","text":"ok"}]}`)
			return
		}
		fmt.Fprint(w, `{"id":"s1"}`)
	}))
	defer srv.Close()

	oc := &Opencode{Base: srv.URL, Model: "anthropic/claude-3", Temperature: 0.7, MaxTokens: 999, HTTP: srv.Client()}
	sess, err := oc.Open(context.Background(), "/wt", "system")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()
	if _, err := sess.Prompt(context.Background(), "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if v, ok := captured["temperature"].(float64); !ok || v != 0.7 {
		t.Errorf("body temperature = %v (ok=%v), want 0.7", captured["temperature"], ok)
	}
	if v, ok := captured["maxTokens"].(float64); !ok || v != 999 {
		t.Errorf("body maxTokens = %v (ok=%v), want 999", captured["maxTokens"], ok)
	}
}

// TestHarnessParityTranscriptShape proves the recorder renders a
// byte-identical sessions/*.md transcript shape for an equivalent scripted
// turn sequence regardless of which driver produced it — the third leg of the
// standing parity gate (alongside pi_test.go's
// TestPiRecorderTranscriptMatchesOpencodeShape, which this generalizes into
// the shared table by driving BOTH real drivers through the recorder rather
// than one real driver against a hand-built stub).
func TestHarnessParityTranscriptShape(t *testing.T) {
	dir := t.TempDir()
	var rendered []string
	for _, d := range contractDrivers() {
		client := d.open(t, []string{"did the work"}, 0, 0, 0)
		sess, err := client.Open(context.Background(), t.TempDir(), "system")
		if err != nil {
			t.Fatalf("%s: Open: %v", d.name, err)
		}
		if _, err := sess.Prompt(context.Background(), "start"); err != nil {
			t.Fatalf("%s: Prompt: %v", d.name, err)
		}

		rc := &recorder{
			sess:    sess,
			path:    filepath.Join(dir, d.name+"-session.md"),
			header:  "# session\n",
			toolSt:  map[string]string{},
			partLen: map[string]int{},
			started: map[string]bool{},
		}
		if err := rc.snapshot(context.Background()); err != nil {
			t.Fatalf("%s: snapshot: %v", d.name, err)
		}
		_ = sess.Close()
		out, err := os.ReadFile(rc.path)
		if err != nil {
			t.Fatalf("%s: read transcript: %v", d.name, err)
		}
		rendered = append(rendered, string(out))
	}
	for i := 1; i < len(rendered); i++ {
		if rendered[i] != rendered[0] {
			t.Fatalf("transcript shape differs between drivers:\n%s:\n%s\n%s:\n%s",
				contractDrivers()[0].name, rendered[0], contractDrivers()[i].name, rendered[i])
		}
	}
	if !strings.Contains(rendered[0], "did the work") {
		t.Fatalf("rendered transcript missing assistant reply: %q", rendered[0])
	}
}
