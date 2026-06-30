// HTTP client for an opencode server. Provider-agnostic: the model is chosen in
// /etc/beehive config and split into provider/model. One session per honeybee;
// "continue" turns reuse the same session so context persists.
package swarm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// Turn-idle poll cadence. opencode's POST /session/{id}/message returns as soon
// as the turn is ACCEPTED, so a turn is settled by polling the message list until
// the assistant message reports completed. The gap backs off geometrically from
// min to max so a long turn isn't hammered while a short one settles promptly.
const (
	defaultPollMin = 250 * time.Millisecond
	defaultPollMax = 2 * time.Second
)

// maxStreamLine bounds one SSE line from opencode's /event stream so a malformed
// or hostile server cannot make the reader allocate without limit (the buffers
// requirement). opencode emits one compact JSON event per data line, well under
// this; a line past the cap fails the scan and is surfaced, never silently grown.
const maxStreamLine = 8 << 20 // 8 MiB

// errNoStream marks an opencode server that exposes no usable /event stream
// (endpoint missing, non-2xx, or unreachable). Prompt treats it as a signal to
// fall back to the proven post+poll turn-idle path, NOT as a turn failure.
var errNoStream = errors.New("opencode: event stream unavailable")

// Opencode talks to an opencode server's session API.
type Opencode struct {
	Base        string  // server base URL
	Model       string  // "provider/model"
	Temperature float64 // sampling temperature; 0 = backend default (omitted from the request)
	MaxTokens   int     // max output tokens; 0 = backend default (omitted from the request)
	HTTP        *http.Client
	Debug       io.Writer // non-nil: log each HTTP request/response

	// Stream, when true, makes Prompt consume opencode's /event SSE stream to
	// observe assistant text as it is produced, emitting deltas via OnDelta and
	// returning the assembled reply when the turn goes idle (stream end ==
	// turn-idle, consistent with the poll path). A server with no usable /event
	// stream transparently falls back to the post+poll path, so enabling Stream is
	// always safe. Off by default: the recorder already renders a near-real-time
	// transcript by polling, so streaming is opt-in per deployment.
	Stream bool
	// OnDelta, when non-nil and Stream is true, receives each incremental
	// assistant text delta (the session id and the newly-appended text) as it
	// arrives on the event stream. The full reply is still returned by Prompt
	// regardless, so a nil OnDelta simply means "stream but don't tee deltas".
	OnDelta func(session, delta string)

	// pollMin/pollMax bound the turn-idle backoff (see Prompt/awaitTurn). Zero =
	// the package defaults; tests set tiny values to keep the wait loop fast. Kept
	// unexported because production never needs to tune them.
	pollMin time.Duration
	pollMax time.Duration
}

// Open creates a server session for the working directory dir (an absolute path;
// opencode takes the cwd from the ?directory= query, not a body field) under the
// given system prompt, WITHOUT sending a first message. The caller drives turns
// via Session.Prompt, which lets a recorder start before the first (often long)
// turn.
func (o *Opencode) Open(ctx context.Context, dir, system string) (Session, error) {
	body := map[string]any{
		"agent": "build", // primary agent that can edit/run, not read-only chat
		// auto-approve all tool actions so the honeybee runs autonomously
		"permission": []map[string]any{{"permission": "*", "pattern": "**", "action": "allow"}},
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := o.post(ctx, "/session", dir, body, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, fmt.Errorf("opencode: empty session id")
	}
	return &ocSession{oc: o, id: created.ID, dir: dir, system: system}, nil
}

// NewSession creates a session and sends the first prompt, returning its reply.
// Convenience for callers that don't record the first turn (e.g. the editor,
// which reads the changed file from disk rather than the opencode transcript).
func (o *Opencode) NewSession(ctx context.Context, dir, system, first string) (Session, string, error) {
	s, err := o.Open(ctx, dir, system)
	if err != nil {
		return nil, "", err
	}
	reply, err := s.Prompt(ctx, first)
	if err != nil {
		return nil, "", err
	}
	return s, reply, nil
}

// Part is one element of a message: assistant text, model reasoning, or a tool
// call with its input/output. Step markers are surfaced verbatim by Type.
type Part struct {
	ID     string         // opencode part id (stable within a message)
	Type   string         // text | reasoning | tool | step-start | step-finish
	Text   string         // text/reasoning content
	Tool   string         // tool name (Type==tool)
	CallID string         // tool call id (Type==tool)
	Status string         // tool state: pending|running|completed|error
	Input  map[string]any // tool input arguments
	Output string         // tool stdout/result (completed)
	Error  string         // tool error (error)
	Title  string         // tool human title, when provided
}

// Message is one session turn (user or assistant) with its ordered parts.
type Message struct {
	ID    string
	Role  string
	Parts []Part
}

type ocSession struct {
	oc     *Opencode
	id     string
	dir    string
	system string
}

// Messages returns the full ordered message history of the session, including
// user prompts, assistant text, reasoning, and tool calls with input+output.
// Used by the recorder to render a live transcript without re-driving the model.
func (s *ocSession) Messages(ctx context.Context) ([]Message, error) {
	var raw []struct {
		Info struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Text   string `json:"text"`
			Tool   string `json:"tool"`
			CallID string `json:"callID"`
			State  struct {
				Status string         `json:"status"`
				Input  map[string]any `json:"input"`
				Output string         `json:"output"`
				Error  string         `json:"error"`
				Title  string         `json:"title"`
			} `json:"state"`
		} `json:"parts"`
	}
	if err := s.oc.get(ctx, "/session/"+s.id+"/message", s.dir, &raw); err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(raw))
	for _, m := range raw {
		msg := Message{ID: m.Info.ID, Role: m.Info.Role}
		for _, p := range m.Parts {
			msg.Parts = append(msg.Parts, Part{
				ID: p.ID, Type: p.Type, Text: p.Text, Tool: p.Tool, CallID: p.CallID,
				Status: p.State.Status, Input: p.State.Input,
				Output: p.State.Output, Error: p.State.Error, Title: p.State.Title,
			})
		}
		out = append(out, msg)
	}
	return out, nil
}

// Prompt sends text and blocks until the assistant turn goes idle, returning the
// assistant's concatenated text parts.
//
// opencode's POST /session/{id}/message returns as soon as the turn is ACCEPTED
// (fire-and-forget), echoing the assistant message stub but NOT waiting for the
// model to act. Returning there would let the caller — and, in the runner, the
// deterministic completion check — race ahead of the agent, so every turn would
// "finish" in milliseconds. We therefore capture the assistant message id +
// completion marker from the accept reply; if it is already finished (a server
// that ran the turn synchronously) we use it directly, otherwise we poll the
// message list until that assistant message reports completed. ctx (the runner's
// per-turn timeout / WallCap) bounds the wait and poll errors are surfaced, never
// swallowed.
//
// When Stream is set, Prompt first tries the /event SSE path (live token deltas,
// stream-end == turn-idle); a server without a usable event stream falls back to
// the exact post+poll path below, so streaming never changes the contract.
func (s *ocSession) Prompt(ctx context.Context, text string) (string, error) {
	if s.oc.Stream {
		reply, err := s.streamPrompt(ctx, text)
		if err == nil {
			return reply, nil
		}
		if !errors.Is(err, errNoStream) {
			return "", err // a real failure mid-stream — surface it, do not re-drive
		}
		// No usable event stream: degrade gracefully to the proven poll path. The
		// stream was abandoned BEFORE any message was posted (openEvents runs first),
		// so falling through posts exactly once.
	}
	acc, err := s.postPrompt(ctx, text)
	if err != nil {
		return "", err
	}
	// Synchronous server: the accept reply already carries time.completed, so the
	// turn is done and its parts are authoritative — no need to poll.
	if acc.completed {
		return acc.text, nil
	}
	// Fire-and-forget accept: wait for the assistant message to settle.
	return s.awaitTurn(ctx, acc.assistantID)
}

// acceptResult is what opencode echoes when it ACCEPTS a turn: the assistant
// message id to track, whether the turn was already completed synchronously, and
// (only then) its settled text.
type acceptResult struct {
	assistantID string
	text        string
	completed   bool
}

// postPrompt POSTs the turn to opencode and returns the accept echo. The request
// body is built identically to the original Prompt (same agent/system/model
// split and the same only-when-set model knobs), so the poll path stays
// byte-identical to before this feature.
func (s *ocSession) postPrompt(ctx context.Context, text string) (acceptResult, error) {
	prov, model, _ := strings.Cut(s.oc.Model, "/")
	body := map[string]any{
		"agent":  "build",
		"system": s.system,
		"model":  map[string]any{"providerID": prov, "modelID": model},
		"parts":  []map[string]any{{"type": "text", "text": text}},
	}
	// Model knobs from the resolved (layered) config. Only sent when explicitly
	// configured (non-zero): an unset knob leaves the request byte-identical to
	// the old default path, so the backend's own default applies.
	if s.oc.Temperature != 0 {
		body["temperature"] = s.oc.Temperature
	}
	if s.oc.MaxTokens != 0 {
		body["maxTokens"] = s.oc.MaxTokens
	}
	var reply struct {
		Info struct {
			ID   string `json:"id"`
			Time struct {
				Completed float64 `json:"completed"`
			} `json:"time"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := s.oc.post(ctx, "/session/"+s.id+"/message", s.dir, body, &reply); err != nil {
		return acceptResult{}, err
	}
	acc := acceptResult{assistantID: reply.Info.ID, completed: reply.Info.Time.Completed != 0}
	if acc.completed {
		var sb strings.Builder
		for _, p := range reply.Parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		acc.text = sb.String()
	}
	return acc, nil
}

// streamEvent is one frame from opencode's /event SSE bus. The bus is global, so
// every frame carries the session id of the thing it concerns: a text part
// update (cumulative text), an assistant message reaching completed, or the
// session going idle. Only the fields this reader needs are decoded.
type streamEvent struct {
	Type       string `json:"type"`
	Properties struct {
		Part struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			MessageID string `json:"messageID"`
			Type      string `json:"type"`
			Text      string `json:"text"`
		} `json:"part"`
		Info struct {
			ID        string `json:"id"`
			Role      string `json:"role"`
			SessionID string `json:"sessionID"`
			Time      struct {
				Completed float64 `json:"completed"`
			} `json:"time"`
		} `json:"info"`
		SessionID string `json:"sessionID"`
	} `json:"properties"`
}

// streamPrompt drives one turn over the event stream: it subscribes to /event
// BEFORE posting (so no early token is missed in the subscribe-after-post gap),
// posts the turn, and reads the stream until the turn goes idle. A server with no
// usable event stream returns errNoStream from openEvents, before any post, so
// the caller can fall back to the poll path with exactly one POST total.
func (s *ocSession) streamPrompt(ctx context.Context, text string) (string, error) {
	resp, err := s.openEvents(ctx)
	if err != nil {
		return "", err // errNoStream (fall back) or a surfaced ctx error
	}
	defer resp.Body.Close()
	acc, err := s.postPrompt(ctx, text)
	if err != nil {
		return "", err
	}
	// Synchronous server: it finished the turn in the accept reply, so there is
	// nothing to stream — return the settled text directly.
	if acc.completed {
		return acc.text, nil
	}
	return s.readStream(ctx, resp.Body, acc.assistantID)
}

// openEvents subscribes to opencode's /event SSE bus. A construction or ctx error
// is surfaced as-is; an unreachable endpoint or a non-2xx response is reported as
// errNoStream so Prompt falls back to the poll path rather than failing the turn.
// On success the caller owns resp.Body and must close it.
func (s *ocSession) openEvents(ctx context.Context) (*http.Response, error) {
	url := s.oc.Base + "/event"
	if s.dir != "" {
		url += "?directory=" + neturl.QueryEscape(s.dir)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	cl := s.oc.HTTP
	if cl == nil {
		cl = http.DefaultClient
	}
	if s.oc.Debug != nil {
		fmt.Fprintf(s.oc.Debug, "[opencode] GET %s (event stream)\n", url)
	}
	resp, err := cl.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err() // cancelled/timed out: a real ctx error, not a missing stream
		}
		return nil, fmt.Errorf("%w: %v", errNoStream, err) // endpoint unreachable: fall back
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: status %d", errNoStream, resp.StatusCode)
	}
	return resp, nil
}

// readStream consumes the SSE bus until the tracked turn goes idle, emitting each
// assistant text delta via OnDelta and returning the assembled reply. opencode
// sends CUMULATIVE part text, so the latest update for a part is its full text;
// deltas are the newly-appended suffix. The turn is idle on the session's
// session.idle, on its assistant message reaching completed, or on a clean EOF
// (stream end == turn-idle, consistent with the poll path). ctx cancellation
// unblocks the underlying read (the request carries ctx) and is surfaced, never
// swallowed; the reader runs in the caller's goroutine, so cancel leaks nothing.
func (s *ocSession) readStream(ctx context.Context, body io.Reader, assistantID string) (string, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), maxStreamLine)
	order := []string{} // text part ids in first-seen order, for deterministic assembly
	latest := map[string]string{}
	assemble := func() string {
		var sb strings.Builder
		for _, id := range order {
			sb.WriteString(latest[id])
		}
		return sb.String()
	}
	for sc.Scan() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		data, ok := sseData(sc.Bytes())
		if !ok {
			continue // comment, blank line, or a non-data field
		}
		var ev streamEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue // keep-alive / non-JSON frame: tolerate, don't fail the turn
		}
		switch ev.Type {
		case "message.part.updated":
			p := ev.Properties.Part
			if p.SessionID != s.id || p.Type != "text" {
				continue // another session, or non-text (reasoning/tool) part
			}
			prev, seen := latest[p.ID]
			if !seen {
				order = append(order, p.ID)
			}
			latest[p.ID] = p.Text
			if s.oc.OnDelta != nil {
				if d := suffixDelta(prev, p.Text); d != "" {
					s.oc.OnDelta(s.id, d)
				}
			}
		case "message.updated":
			in := ev.Properties.Info
			if in.SessionID == s.id && in.Role == "assistant" && in.Time.Completed != 0 &&
				(assistantID == "" || in.ID == assistantID) {
				return assemble(), nil
			}
		case "session.idle":
			if ev.Properties.SessionID == s.id {
				return assemble(), nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("opencode stream: %w", err)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// Clean EOF with no explicit idle marker: the server closed the stream, which
	// signals the turn is done. Return whatever assembled.
	return assemble(), nil
}

// sseData extracts the payload of an SSE "data:" field line, trimming the single
// optional leading space per the spec. Non-data lines (comments, event:, id:,
// blanks) report ok=false.
func sseData(line []byte) ([]byte, bool) {
	const field = "data:"
	if !bytes.HasPrefix(line, []byte(field)) {
		return nil, false
	}
	d := line[len(field):]
	if len(d) > 0 && d[0] == ' ' {
		d = d[1:]
	}
	return d, true
}

// suffixDelta returns the text newly appended to a cumulative part: the suffix of
// cur after prev when cur extends prev (the normal monotonic case), else the
// whole of cur (a non-monotonic rewrite, so the consumer still sees the content).
func suffixDelta(prev, cur string) string {
	if strings.HasPrefix(cur, prev) {
		return cur[len(prev):]
	}
	return cur
}

// awaitTurn polls the session message list until the assistant message for the
// just-accepted turn reports completed (info.time.completed set), then returns
// its concatenated text. assistantID is the id opencode echoed for this turn;
// when empty (a server that did not echo it) it falls back to the last assistant
// message in the list. Bounded geometric backoff; honors ctx cancellation; poll
// errors are surfaced, not swallowed.
func (s *ocSession) awaitTurn(ctx context.Context, assistantID string) (string, error) {
	wait, max := s.oc.pollMin, s.oc.pollMax
	if wait <= 0 {
		wait = defaultPollMin
	}
	if max <= 0 {
		max = defaultPollMax
	}
	if max < wait {
		max = wait
	}
	for {
		text, done, err := s.turnText(ctx, assistantID)
		if err != nil {
			return "", err
		}
		if done {
			return text, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
		if wait *= 2; wait > max {
			wait = max
		}
	}
}

// turnText fetches the session message list and reports whether the tracked
// assistant turn has completed, returning its concatenated text when so. opencode
// stamps a message's info.time.completed when its turn finishes; an in-flight
// assistant message has no completed timestamp. A turn that has not yet created
// its assistant message (or whose message is still in flight) reports done=false.
func (s *ocSession) turnText(ctx context.Context, assistantID string) (text string, done bool, err error) {
	var raw []struct {
		Info struct {
			ID   string `json:"id"`
			Role string `json:"role"`
			Time struct {
				Completed float64 `json:"completed"`
			} `json:"time"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := s.oc.get(ctx, "/session/"+s.id+"/message", s.dir, &raw); err != nil {
		return "", false, err
	}
	idx := -1
	for i := range raw {
		if raw[i].Info.Role != "assistant" {
			continue
		}
		if assistantID != "" {
			if raw[i].Info.ID == assistantID {
				idx = i
				break
			}
			continue
		}
		idx = i // no id to track: follow the last assistant message
	}
	if idx < 0 || raw[idx].Info.Time.Completed == 0 {
		return "", false, nil // not created yet, or still in flight
	}
	var sb strings.Builder
	for _, p := range raw[idx].Parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String(), true, nil
}

func (s *ocSession) Close() error { return nil }

// get issues a JSON GET with the ?directory= query and decodes into out.
func (o *Opencode) get(ctx context.Context, path, dir string, out any) error {
	url := o.Base + path
	if dir != "" {
		url += "?directory=" + neturl.QueryEscape(dir)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	cl := o.HTTP
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("opencode GET %s: %d", path, resp.StatusCode)
	}
	return json.Unmarshal(rb, out)
}

// post issues a JSON POST. dir, when set, is sent as the ?directory= query that
// opencode uses to choose the working directory for the session/turn.
func (o *Opencode) post(ctx context.Context, path, dir string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := o.Base + path
	if dir != "" {
		url += "?directory=" + neturl.QueryEscape(dir)
	}
	if o.Debug != nil {
		fmt.Fprintf(o.Debug, "[opencode] POST %s ...\n", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	cl := o.HTTP
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		if o.Debug != nil {
			fmt.Fprintf(o.Debug, "[opencode] POST %s error: %v\n", path, err)
		}
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if o.Debug != nil {
		fmt.Fprintf(o.Debug, "[opencode] POST %s -> %d (%d bytes)\n", path, resp.StatusCode, len(rb))
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("opencode %s: %d: %s", path, resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	if out != nil {
		if err := json.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("opencode %s decode: %w", path, err)
		}
	}
	return nil
}
