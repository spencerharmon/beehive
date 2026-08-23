// Pi coding agent (https://pi.dev/) harness driver, implementing the same
// Harness contract (Client+Session, swarm.go) as *Opencode so an install can
// route honeybee/editor turns to pi instead of opencode with full lock-step
// parity: session persistence across "continue" turns, streaming/token-settle
// under the idle watchdog, model/temperature/token config, transcript
// recording in the SAME Message/Part shape the recorder renders to
// sessions/*.md, cancellation, and error surfacing.
//
// Integration: pi's RPC mode (JSON protocol over stdin/stdout, one command or
// event per line — see https://pi.dev/docs/latest/rpc). *Pi spawns the pi
// binary with --mode rpc, speaks the line-delimited JSON protocol for session
// open ("prompt"/"abort"/"get_messages"/"get_last_assistant_text"), and keeps
// the SAME process alive across Prompt calls so a "continue" turn is simply a
// second prompt command sent to the still-running process — pi's own
// in-process conversation state provides the context persistence, exactly as
// opencode's session id does.
//
// This does NOT drive pi's interactive TUI: RPC mode is a headless,
// commands-in/events-out protocol with no terminal attached, the same
// headless shape opencode's HTTP session API provides.
//
// Context loading: the system prompt (HONEYBEE.md + AGENTS.md + task brief,
// assembled by the caller exactly as it is for opencode) is handed to pi via
// --append-system-prompt at spawn time, so it is appended to (never replaces)
// whatever pi's own AGENTS.md/CLAUDE.md/SYSTEM.md discovery already loads for
// the project — the caller's brief reaches the agent without double-injecting
// or losing any part of it, and pi's native context path (skills, project
// AGENTS.md) still runs on top.
//
// Process spawning is abstracted behind the spawn func field so tests can
// substitute a fake stdin/stdout pipe pair standing in for the real pi binary
// (see pi_test.go) — no dependency on pi actually being installed.
package swarm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Pi talks to a `pi --mode rpc` process over stdin/stdout JSONL.
type Pi struct {
	Bin         string        // pi binary; "" = "pi"
	Model       string        // "provider/model"; split on "/" for --provider/--model
	Thinking    string        // pi thinking level (off|minimal|low|medium|high|xhigh|max); "" = backend default
	Temperature float64       // reserved for parity with Opencode; pi's RPC protocol has no per-session temperature knob (model/thinking select the behavior), so this is currently unused by Open/Prompt but kept on the struct so callers configuring Harness-agnostically never fail on a missing field.
	MaxTokens   int           // reserved for parity with Opencode; see Temperature.
	IdleTimeout time.Duration // per-turn PROGRESS watchdog; see (*piSession).awaitSettled. 0 = disabled.
	Debug       io.Writer     // non-nil: log every stdin/stdout line

	// spawn starts one pi RPC process rooted at dir with the given extra CLI
	// args and returns its stdin writer, stdout reader, and a stop func that
	// tears the process down (kill + reap). Overridable so tests can splice in
	// a fake process (an in-memory pipe pair driven by a goroutine that speaks
	// the RPC protocol) without depending on a real pi binary. nil = the
	// production default, execSpawn.
	spawn func(bin, dir string, args []string) (stdin io.WriteCloser, stdout io.Reader, stop func() error, err error)
}

// SetModel selects the provider/model for sessions opened after this call,
// mirroring Opencode.SetModel so the runner's per-kind model routing
// (Runner.ModelFor) works against a *Pi Client exactly as it does against
// *Opencode.
func (p *Pi) SetModel(model string) { p.Model = model }

// execSpawn is the production spawn implementation: it execs the real pi
// binary with --mode rpc plus the given args, rooted at dir.
func execSpawn(bin, dir string, args []string) (io.WriteCloser, io.Reader, func() error, error) {
	if bin == "" {
		bin = "pi"
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("pi: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("pi: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("pi: start %s: %w", bin, err)
	}
	stop := func() error {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return cmd.Wait()
	}
	return stdin, stdout, stop, nil
}

// Open spawns a pi RPC process rooted at dir, appending system to whatever pi
// itself discovers via AGENTS.md/CLAUDE.md/skills (see the package doc), and
// returns a Session backed by that ONE process. No prompt is sent yet — the
// caller drives turns via Session.Prompt, exactly as Opencode.Open — so a
// recorder can start before the (often long) first turn runs.
func (p *Pi) Open(ctx context.Context, dir, system string) (Session, error) {
	spawn := p.spawn
	if spawn == nil {
		spawn = execSpawn
	}
	// --no-session: pi state (session persistence, transcripts) must never be
	// read from outside the repo (see package doc / task constraint); the
	// running process itself is this session's only state, and Messages()
	// below reconstructs the transcript live via the RPC protocol, not from
	// pi's on-disk session store.
	args := []string{"--mode", "rpc", "--no-session"}
	if system != "" {
		args = append(args, "--append-system-prompt", system)
	}
	if p.Model != "" {
		if prov, model, ok := strings.Cut(p.Model, "/"); ok {
			args = append(args, "--provider", prov, "--model", model)
		} else {
			args = append(args, "--model", p.Model)
		}
	}
	if p.Thinking != "" {
		args = append(args, "--thinking", p.Thinking)
	}
	stdin, stdout, stop, err := spawn(p.Bin, dir, args)
	if err != nil {
		return nil, err
	}
	s := &piSession{
		p:       p,
		dir:     dir,
		system:  system,
		stdin:   stdin,
		stop:    stop,
		pending: make(map[string]chan piResponse),
		done:    make(chan struct{}),
	}
	s.lastActivity.Store(time.Now().UnixNano())
	go s.readLoop(stdout)
	return s, nil
}

// piResponse is one decoded RPC "response" record (see Error Handling /
// Commands in https://pi.dev/docs/latest/rpc): success/error plus the raw
// "data" payload, left undecoded until the caller knows what shape to expect.
type piResponse struct {
	Success bool
	Error   string
	Data    json.RawMessage
}

// piSession drives one pi RPC process across its whole lifetime: Open starts
// it, every Prompt call sends one "prompt" command and blocks until the
// process reports agent_settled (or the idle watchdog fires), and Close tears
// the process down. Reusing the SAME piSession across Prompt calls is exactly
// pi's own "continue" semantics — the running process retains its full
// in-process conversation state, so a second Prompt needs to send nothing but
// the new text.
type piSession struct {
	p      *Pi
	dir    string
	system string

	stdin io.WriteCloser
	stop  func() error

	mu      sync.Mutex
	pending map[string]chan piResponse // in-flight command id -> response waiter
	nextID  int64
	settled chan struct{} // set by Prompt while a turn is in flight; closed by readLoop on agent_settled
	closed  bool

	lastActivity atomic.Int64 // UnixNano of the last stdout line seen; the idle watchdog's liveness clock
	done         chan struct{}
	readErr      error
}

// Abort tears down pi's in-flight turn via the RPC "abort" command, mirroring
// ocSession.Abort. Best-effort: the turn loop's own re-Prompt supersedes a
// dead turn regardless, so a failed abort here is not fatal to the caller.
func (s *piSession) Abort(ctx context.Context) error {
	_, err := s.call(ctx, "abort", nil)
	return err
}

// Prompt sends text to the running pi process and blocks until the turn
// settles (pi's agent_settled event) or the idle watchdog abandons it,
// returning the assistant's reply text. Because the SAME piSession (and thus
// the SAME pi process) is reused for every turn, pi's own conversation state
// carries context across calls — a second Prompt is pi's "continue" with no
// extra bookkeeping needed here.
func (s *piSession) Prompt(ctx context.Context, text string) (string, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", fmt.Errorf("pi: session closed")
	}
	settled := make(chan struct{})
	s.settled = settled
	s.mu.Unlock()

	resp, err := s.call(ctx, "prompt", map[string]any{"message": text})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("pi: prompt rejected: %s", resp.Error)
	}

	if err := s.awaitSettled(ctx, settled); err != nil {
		return "", err
	}

	textResp, err := s.call(ctx, "get_last_assistant_text", nil)
	if err != nil {
		return "", err
	}
	if !textResp.Success {
		return "", fmt.Errorf("pi: get_last_assistant_text: %s", textResp.Error)
	}
	var out struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(textResp.Data, &out); err != nil {
		return "", fmt.Errorf("pi: get_last_assistant_text decode: %w", err)
	}
	if out.Text == nil {
		return "", fmt.Errorf("pi: no assistant reply after settle")
	}
	return *out.Text, nil
}

// awaitSettled blocks until the readLoop closes settled (pi's agent_settled
// event, meaning no automatic retry/compaction/queued continuation remains)
// or the progress watchdog decides the turn is wedged. It mirrors
// ocSession.watchIdle's semantics exactly: ANY stdout activity (a
// message_update delta, a tool_execution_* event, anything) resets the idle
// clock, so a long but productive turn runs to completion while a truly dead
// process/model is abandoned and surfaced as ErrTurnIdle — the SAME sentinel
// the runner's turn loop matches on regardless of which driver produced it.
func (s *piSession) awaitSettled(ctx context.Context, settled chan struct{}) error {
	var idleTimer *time.Timer
	var idleC <-chan time.Time
	if s.p.IdleTimeout > 0 {
		idleTimer = time.NewTimer(s.p.IdleTimeout)
		defer idleTimer.Stop()
		idleC = idleTimer.C
	}
	poll := time.NewTicker(pollTick(s.p.IdleTimeout))
	defer poll.Stop()
	lastSeen := s.lastActivity.Load()
	for {
		select {
		case <-settled:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			s.mu.Lock()
			rerr := s.readErr
			s.mu.Unlock()
			return fmt.Errorf("pi: process exited unexpectedly: %v", rerr)
		case <-poll.C:
			if cur := s.lastActivity.Load(); cur != lastSeen {
				lastSeen = cur
				if idleTimer != nil {
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}
					idleTimer.Reset(s.p.IdleTimeout)
				}
			}
		case <-idleC:
			// Wedged: no stdout activity at all within IdleTimeout. Abort the
			// live turn server-side (best-effort) before surfacing, mirroring
			// Opencode's cancel-then-ErrTurnIdle behavior.
			_ = s.sendOnly("abort", nil)
			return fmt.Errorf("%w (%s)", ErrTurnIdle, s.p.IdleTimeout)
		}
	}
}

// pollTick picks a liveness-check cadence proportional to the configured idle
// timeout so a tiny test timeout (milliseconds) is polled finely while a
// production timeout (minutes) is not busy-polled. 0 (disabled) still needs a
// working ticker to notice s.done/ctx, so it falls back to a coarse default.
func pollTick(idle time.Duration) time.Duration {
	if idle <= 0 {
		return 500 * time.Millisecond
	}
	t := idle / 10
	if t < time.Millisecond {
		t = time.Millisecond
	}
	if t > 500*time.Millisecond {
		t = 500 * time.Millisecond
	}
	return t
}

// Messages returns the full ordered conversation history via pi's
// get_messages RPC command, converted to the SAME Message/Part shape
// Opencode.Messages produces, so the recorder renders an identical
// sessions/*.md transcript regardless of which harness drove the turns.
func (s *piSession) Messages(ctx context.Context) ([]Message, error) {
	resp, err := s.call(ctx, "get_messages", nil)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("pi: get_messages: %s", resp.Error)
	}
	var out struct {
		Messages []piMessage `json:"messages"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, fmt.Errorf("pi: get_messages decode: %w", err)
	}
	msgs := make([]Message, 0, len(out.Messages))
	for i, m := range out.Messages {
		msgs = append(msgs, m.toMessage(i))
	}
	return msgs, nil
}

// Close tears the pi process down. Idempotent.
func (s *piSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.stop != nil {
		return s.stop()
	}
	return nil
}

// piMessage is pi's on-the-wire AgentMessage shape (UserMessage /
// AssistantMessage / ToolResultMessage / BashExecutionMessage — RPC mode
// docs, "Types" section), parsed tolerantly: every message carries a role and
// an ordered content array whose blocks may be plain text, thinking, a tool
// call, or a tool result. Fields absent for a given block type simply stay
// zero, matching how Opencode's Part already carries a superset of fields
// only some part Types populate.
type piMessage struct {
	Role    string          `json:"role"`
	Content []piContentPart `json:"content"`
	// Some pi message shapes (e.g. a plain string content, seen for a
	// minimal UserMessage) may carry content as a bare string rather than a
	// block array; RawContent captures that fallback.
	RawContent json.RawMessage `json:"-"`
}

// UnmarshalJSON tolerates BOTH content shapes pi may emit: an array of typed
// blocks (the common case) or a bare string (a plain user message with no
// attachments/tool activity). Decoding never fails outright on an unexpected
// shape — an unrecognized content value degrades to a single text part
// rather than dropping the message, since a message record we cannot fully
// parse must still not be silently lost from the transcript.
func (m *piMessage) UnmarshalJSON(b []byte) error {
	var probe struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	m.Role = probe.Role
	if len(probe.Content) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(probe.Content)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return fmt.Errorf("pi: message content string: %w", err)
		}
		m.Content = []piContentPart{{Type: "text", Text: text}}
		return nil
	}
	var parts []piContentPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		// Unrecognized content shape: preserve it verbatim as text rather
		// than dropping the message from the transcript.
		m.Content = []piContentPart{{Type: "text", Text: string(trimmed)}}
		return nil
	}
	m.Content = parts
	return nil
}

// piContentPart is one block of a piMessage's content array: text/thinking
// content, a tool_call (name+id+input), or a tool_result (id+output/isError).
// Field names follow pi's documented AssistantMessage/ToolResultMessage
// shapes (RPC mode docs); every field the caller does not use for a given
// Type is simply left zero.
type piContentPart struct {
	Type      string          `json:"type"` // text | thinking | tool_call | tool_result
	Text      string          `json:"text"`
	ToolName  string          `json:"toolName"`
	ToolID    string          `json:"id"`
	ToolUseID string          `json:"toolCallId"` // tool_result's back-reference to its tool_call id
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"content"` // tool_result content: string or [{type:"text",text:...}]
	IsError   bool            `json:"isError"`
}

// toMessage converts one piMessage into the package's Message/Part shape
// (see opencode.go), the exact type the recorder renders to sessions/*.md —
// so a transcript recorded from a pi-driven turn is byte-for-byte the same
// shape as one recorded from opencode for an equivalent turn sequence.
func (pm piMessage) toMessage(idx int) Message {
	msg := Message{ID: "pi-" + strconv.Itoa(idx), Role: pm.Role}
	for pi, cp := range pm.Content {
		part := Part{ID: msg.ID + "-" + strconv.Itoa(pi)}
		switch cp.Type {
		case "tool_call":
			part.Type = "tool"
			part.Tool = cp.ToolName
			part.CallID = cp.ToolID
			part.Status = "completed"
			if len(cp.Input) > 0 {
				var in map[string]any
				if err := json.Unmarshal(cp.Input, &in); err == nil {
					part.Input = in
				}
			}
		case "tool_result":
			part.Type = "tool"
			part.CallID = cp.ToolUseID
			if cp.IsError {
				part.Status = "error"
				part.Error = extractPiText(cp.Output)
			} else {
				part.Status = "completed"
				part.Output = extractPiText(cp.Output)
			}
		case "thinking":
			part.Type = "reasoning"
			part.Text = cp.Text
		default: // "text" and any unrecognized type degrade to plain text
			part.Type = "text"
			part.Text = cp.Text
		}
		msg.Parts = append(msg.Parts, part)
	}
	return msg
}

// extractPiText normalizes a tool_result's content field, which pi may emit
// as either a bare string or a [{type:"text",text:...}, ...] block array
// (mirroring the request-side content shape), into a single concatenated
// string for Part.Output/Part.Error.
func extractPiText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
		return string(trimmed)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return string(trimmed)
	}
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// call sends a command with a fresh id and blocks until its matching
// response arrives (or ctx is done / the process dies).
func (s *piSession) call(ctx context.Context, typ string, extra map[string]any) (piResponse, error) {
	id := s.newID()
	ch := make(chan piResponse, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return piResponse{}, fmt.Errorf("pi: session closed")
	}
	s.pending[id] = ch
	s.mu.Unlock()

	body := map[string]any{"type": typ, "id": id}
	for k, v := range extra {
		body[k] = v
	}
	if err := s.writeLine(body); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return piResponse{}, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return piResponse{}, ctx.Err()
	case <-s.done:
		s.mu.Lock()
		rerr := s.readErr
		delete(s.pending, id)
		s.mu.Unlock()
		return piResponse{}, fmt.Errorf("pi: process exited waiting for %s response: %v", typ, rerr)
	}
}

// sendOnly writes a fire-and-forget command (no response wait) — used for the
// idle watchdog's best-effort abort, which must not itself block on a process
// that may already be wedged.
func (s *piSession) sendOnly(typ string, extra map[string]any) error {
	body := map[string]any{"type": typ}
	for k, v := range extra {
		body[k] = v
	}
	return s.writeLine(body)
}

func (s *piSession) writeLine(body map[string]any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if s.p.Debug != nil {
		fmt.Fprintf(s.p.Debug, "[pi] -> %s\n", buf)
	}
	buf = append(buf, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("pi: session closed")
	}
	_, err = s.stdin.Write(buf)
	return err
}

func (s *piSession) newID() string {
	n := s.nextIDVal()
	return "pi-" + strconv.FormatInt(n, 10)
}

func (s *piSession) nextIDVal() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	return s.nextID
}

// readLoop consumes stdout, one JSONL record per line, dispatching a
// "response" record to its waiting call() and every other record (an event)
// to handleEvent. It runs for the whole process lifetime and closes s.done
// when stdout closes (the process exited), unblocking any in-flight
// call/awaitSettled with s.readErr.
func (s *piSession) readLoop(stdout io.Reader) {
	defer close(s.done)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if s.p.Debug != nil {
			fmt.Fprintf(s.p.Debug, "[pi] <- %s\n", line)
		}
		s.lastActivity.Store(time.Now().UnixNano())
		var head struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			continue // malformed line: not progress-worthy, but not fatal either
		}
		if head.Type == "response" {
			var resp struct {
				Success bool            `json:"success"`
				Error   string          `json:"error"`
				Data    json.RawMessage `json:"data"`
			}
			_ = json.Unmarshal(line, &resp)
			s.mu.Lock()
			ch, ok := s.pending[head.ID]
			if ok {
				delete(s.pending, head.ID)
			}
			s.mu.Unlock()
			if ok {
				ch <- piResponse{Success: resp.Success, Error: resp.Error, Data: resp.Data}
			}
			continue
		}
		s.handleEvent(head.Type)
	}
	s.mu.Lock()
	s.readErr = sc.Err()
	s.mu.Unlock()
}

// handleEvent reacts to the one event type Prompt actually waits on:
// agent_settled closes the current turn's settled channel. Every other event
// (message_update, tool_execution_*, turn_start/end, ...) has already reset
// the idle-watchdog liveness clock via readLoop's lastActivity stamp above —
// their content does not otherwise need decoding since Messages()/Prompt's
// reply both come from authoritative RPC calls (get_messages /
// get_last_assistant_text) made AFTER settle, not reconstructed from the
// streamed deltas.
func (s *piSession) handleEvent(typ string) {
	if typ != "agent_settled" {
		return
	}
	s.mu.Lock()
	settled := s.settled
	s.settled = nil
	s.mu.Unlock()
	if settled != nil {
		close(settled)
	}
}

var _ Client = (*Pi)(nil)
var _ Session = (*piSession)(nil)
var _ modelSelector = (*Pi)(nil)
var _ aborter = (*piSession)(nil)
