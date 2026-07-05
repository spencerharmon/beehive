// Streaming read path for an opencode session. opencode's POST /session/{id}/
// message is fire-and-forget (see Prompt), so the poll path (recorder.pollLoop /
// awaitTurn) settles a turn by re-fetching the message list. This file adds the
// alternative: subscribe to opencode's server-sent /event channel and assemble
// the transcript from deltas in near real time (token by token) instead of at the
// poll cadence. It is strictly additive — a Session that cannot stream (every test
// mock) is transparently polled, and a stream that drops mid-session falls back to
// polling — so the proven Prompt/awaitTurn turn-idle detection is never altered.
package swarm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
)

// ErrNoStream reports that the opencode server has no usable event channel (the
// /event request was refused, or answered with something other than an SSE
// stream). It is NOT a failure: the caller falls back to polling, which produces
// the identical transcript at the poll cadence.
var ErrNoStream = errors.New("opencode: event stream unsupported")

// maxEventBytes bounds a single SSE event so a malformed or unbounded stream can
// never exhaust memory. opencode re-sends a part's FULL cumulative text on each
// update, so one event can be large (a big tool output); the cap is generous but
// finite. An event past the cap ends the stream with an error and the recorder
// falls back to polling (which handles any size via the message list).
const maxEventBytes = 8 << 20 // 8 MiB

// streamer is an optional Session capability: consume opencode's event channel
// and render the transcript as it is produced rather than by polling. A Session
// that does not implement it is polled instead (the recorder type-asserts), so
// mocks need no streaming support.
//
// stream blocks until ctx is done or the stream fails. It calls onUpdate with the
// FULL current message list whenever the transcript changes, and onIdle when a
// turn settles (opencode's session.idle) — the streaming analogue of the poll
// path's time.completed turn-idle. Both callbacks run synchronously on stream's
// own goroutine, so a caller mutating shared state from them needs no extra lock.
// It returns ErrNoStream when the server has no event channel.
type streamer interface {
	stream(ctx context.Context, onUpdate func([]Message), onIdle func()) error
}

// ocSession implements streamer. Compile-time assertion so a signature drift is a
// build error, not a silent fall-through to polling.
var _ streamer = (*ocSession)(nil)

// stream subscribes to the opencode server's global /event SSE channel and feeds
// the recorder deltas for THIS session (filtered by sessionID). It maintains a
// message store assembled from the events and invokes onUpdate/onIdle as content
// arrives and turns settle. The read runs inline on the caller's goroutine and is
// bounded by ctx: cancelling ctx cancels the in-flight HTTP request, unblocking
// the read so this returns promptly and leaks no goroutine.
func (s *ocSession) stream(ctx context.Context, onUpdate func([]Message), onIdle func()) error {
	url := s.oc.Base + "/event"
	if s.dir != "" {
		url += "?directory=" + neturl.QueryEscape(s.dir)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	cl := s.oc.HTTP
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		// A cancelled ctx surfaces here as a request error; report it as the ctx
		// error so the recorder treats it as a clean shutdown, not a stream fault.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrNoStream
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return ErrNoStream
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return ErrNoStream
	}

	store := newMsgStore()
	dec := newSSEDecoder(resp.Body)
	for {
		data, err := dec.next()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // ctx cancel unblocked the read: clean shutdown
			}
			return err // real read/parse error or server EOF -> recorder polls on
		}
		if len(data) == 0 {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue // ignore an unparseable frame rather than abandon the stream
		}
		switch ev.Type {
		case "message.updated":
			if ev.Properties.Info == nil || ev.Properties.Info.SessionID != s.id {
				continue
			}
			store.putInfo(ev.Properties.Info.ID, ev.Properties.Info.Role)
			onUpdate(store.snapshot())
		case "message.part.updated":
			p := ev.Properties.Part
			if p == nil || p.SessionID != s.id {
				continue
			}
			store.putPart(p.MessageID, part(p))
			onUpdate(store.snapshot())
		case "session.idle":
			if ev.Properties.SessionID != s.id {
				continue
			}
			onIdle()
		}
	}
}

// sseEvent is the opencode event envelope. Every event has a type and a
// properties object; the fields used per type are a subset (a nil pointer means
// the event was a different type). Unknown fields are ignored.
type sseEvent struct {
	Type       string `json:"type"`
	Properties struct {
		Info      *eventInfo `json:"info"`      // message.updated
		Part      *eventPart `json:"part"`      // message.part.updated
		SessionID string     `json:"sessionID"` // session.idle (and echoed elsewhere)
	} `json:"properties"`
}

type eventInfo struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	SessionID string `json:"sessionID"`
}

// eventPart mirrors the message-endpoint part shape (see ocSession.Messages) so a
// streamed part and a polled part render identically. Text/Output are cumulative:
// opencode re-sends the whole part on each update, not a delta.
type eventPart struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	Tool      string `json:"tool"`
	CallID    string `json:"callID"`
	State     struct {
		Status string         `json:"status"`
		Input  map[string]any `json:"input"`
		Output string         `json:"output"`
		Error  string         `json:"error"`
		Title  string         `json:"title"`
	} `json:"state"`
}

// part converts a streamed event part into the shared Part type.
func part(p *eventPart) Part {
	return Part{
		ID: p.ID, Type: p.Type, Text: p.Text, Tool: p.Tool, CallID: p.CallID,
		Status: p.State.Status, Input: p.State.Input,
		Output: p.State.Output, Error: p.State.Error, Title: p.State.Title,
	}
}

// msgStore assembles the session transcript from opencode event deltas. Messages
// and their parts are kept in first-seen order (opencode ids are creation-ordered)
// so a streamed transcript matches the poll path's. A part update carries the FULL
// part, so re-seeing a part id replaces the prior value. A message whose role is
// not yet known (a part arrived before its message.updated) is held back from
// snapshots until the role is set, so the transcript never shows a headless turn;
// role events effectively always precede parts in practice.
type msgStore struct {
	order []string             // message ids, first-seen order
	byID  map[string]*storeMsg // id -> message
}

type storeMsg struct {
	id        string
	role      string
	partOrder []string        // part ids, first-seen order
	parts     map[string]Part // part id -> latest part
}

func newMsgStore() *msgStore {
	return &msgStore{byID: map[string]*storeMsg{}}
}

// msg returns the tracked message for id, creating it (in first-seen order) when
// absent so a part can attach before its message.updated arrives.
func (s *msgStore) msg(id string) *storeMsg {
	m := s.byID[id]
	if m == nil {
		m = &storeMsg{id: id, parts: map[string]Part{}}
		s.byID[id] = m
		s.order = append(s.order, id)
	}
	return m
}

// putInfo records a message's role (from message.updated).
func (s *msgStore) putInfo(id, role string) {
	if id == "" {
		return
	}
	m := s.msg(id)
	if role != "" {
		m.role = role
	}
}

// putPart records or replaces a part on its message (from message.part.updated).
func (s *msgStore) putPart(messageID string, p Part) {
	if messageID == "" || p.ID == "" {
		return
	}
	m := s.msg(messageID)
	if _, seen := m.parts[p.ID]; !seen {
		m.partOrder = append(m.partOrder, p.ID)
	}
	m.parts[p.ID] = p
}

// snapshot renders the current store as an ordered message list for the recorder.
// Messages without a known role yet are skipped (see the type comment).
func (s *msgStore) snapshot() []Message {
	out := make([]Message, 0, len(s.order))
	for _, id := range s.order {
		m := s.byID[id]
		if m.role == "" {
			continue
		}
		msg := Message{ID: m.id, Role: m.role}
		for _, pid := range m.partOrder {
			msg.Parts = append(msg.Parts, m.parts[pid])
		}
		out = append(out, msg)
	}
	return out
}

// sseDecoder reads server-sent events: it accumulates each event's `data:` lines
// and returns the joined payload at the event boundary (a blank line). Other SSE
// fields (event:, id:, retry:) and comment/heartbeat lines are ignored — opencode
// carries the event type inside the JSON payload. The buffer is bounded by
// maxEventBytes so an unbounded line cannot exhaust memory.
type sseDecoder struct{ sc *bufio.Scanner }

func newSSEDecoder(r io.Reader) *sseDecoder {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventBytes)
	return &sseDecoder{sc: sc}
}

// next returns the next event's data payload, or an error (io.EOF at a clean end,
// bufio.ErrTooLong past the cap, or the underlying read error — e.g. ctx cancel).
func (d *sseDecoder) next() ([]byte, error) {
	var data []byte
	sawData := false
	for d.sc.Scan() {
		line := d.sc.Bytes()
		if len(line) == 0 { // blank line: event boundary
			if sawData {
				return data, nil
			}
			continue // stray blank between events
		}
		if line[0] == ':' {
			continue // comment / keep-alive
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		if string(field) != "data" {
			continue // ignore event:/id:/retry:
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		if sawData {
			data = append(data, '\n') // SSE joins multiple data lines with \n
		}
		data = append(data, value...)
		sawData = true
	}
	if err := d.sc.Err(); err != nil {
		return nil, err
	}
	if sawData {
		return data, nil // stream ended right after a data event, no trailing blank
	}
	return nil, io.EOF
}
