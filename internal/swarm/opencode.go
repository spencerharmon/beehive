// HTTP client for an opencode server. Provider-agnostic: the model is chosen in
// /etc/beehive config and split into provider/model. One session per honeybee;
// "continue" turns reuse the same session so context persists.
package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// Opencode talks to an opencode server's session API.
type Opencode struct {
	Base  string // server base URL
	Model string // "provider/model"
	HTTP  *http.Client
	Debug io.Writer // non-nil: log each HTTP request/response
}

// NewSession creates a server session for the working directory dir (an absolute
// path; opencode takes the cwd from the ?directory= query, not a body field) and
// seeds the first user prompt under the system prompt. Returns the first reply.
func (o *Opencode) NewSession(ctx context.Context, dir, system, first string) (Session, string, error) {
	body := map[string]any{
		"agent": "build", // primary agent that can edit/run, not read-only chat
		// auto-approve all tool actions so the honeybee runs autonomously
		"permission": []map[string]any{{"permission": "*", "pattern": "**", "action": "allow"}},
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := o.post(ctx, "/session", dir, body, &created); err != nil {
		return nil, "", err
	}
	if created.ID == "" {
		return nil, "", fmt.Errorf("opencode: empty session id")
	}
	s := &ocSession{oc: o, id: created.ID, dir: dir, system: system}
	if o.Debug != nil {
		tapCtx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		go o.tap(tapCtx, dir, created.ID)
	}
	reply, err := s.Prompt(ctx, first)
	if err != nil {
		return nil, "", err
	}
	return s, reply, nil
}

type ocSession struct {
	oc     *Opencode
	id     string
	dir    string
	system string
	cancel context.CancelFunc
}

// Prompt sends text and blocks until the assistant turn completes, returning the
// assistant's concatenated text parts.
func (s *ocSession) Prompt(ctx context.Context, text string) (string, error) {
	prov, model, _ := strings.Cut(s.oc.Model, "/")
	body := map[string]any{
		"agent":  "build",
		"system": s.system,
		"model":  map[string]any{"providerID": prov, "modelID": model},
		"parts":  []map[string]any{{"type": "text", "text": text}},
	}
	var reply struct {
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := s.oc.post(ctx, "/session/"+s.id+"/message", s.dir, body, &reply); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, p := range reply.Parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String(), nil
}

func (s *ocSession) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// tap polls this session's latest assistant message and prints live activity
// (assistant text as it grows, tool-call state transitions) to Debug. opencode's
// /event HTTP stream does not emit per-turn events in this server build, so we
// poll instead. Runs until ctx is cancelled (session Close); best-effort.
func (o *Opencode) tap(ctx context.Context, dir, sid string) {
	path := "/session/" + sid + "/message"
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	printedText := 0
	lastMsg := ""
	toolStatus := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var msgs []struct {
			Info struct {
				ID   string `json:"id"`
				Role string `json:"role"`
			} `json:"info"`
			Parts []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Tool   string `json:"tool"`
				CallID string `json:"callID"`
				State  struct {
					Status string `json:"status"`
				} `json:"state"`
			} `json:"parts"`
		}
		if err := o.get(ctx, path, dir, &msgs); err != nil {
			continue
		}
		if len(msgs) == 0 {
			continue
		}
		last := msgs[len(msgs)-1]
		if last.Info.Role != "assistant" {
			continue
		}
		if last.Info.ID != lastMsg {
			lastMsg = last.Info.ID
			printedText = 0
		}
		var sb strings.Builder
		for _, p := range last.Parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		if full := sb.String(); len(full) > printedText {
			fmt.Fprint(o.Debug, full[printedText:])
			printedText = len(full)
		}
		for _, p := range last.Parts {
			if p.Type != "tool" {
				continue
			}
			if toolStatus[p.CallID] == p.State.Status {
				continue
			}
			toolStatus[p.CallID] = p.State.Status
			switch p.State.Status {
			case "pending", "running":
				fmt.Fprintf(o.Debug, "\n  \u00b7 %s ...\n", p.Tool)
			case "completed":
				fmt.Fprintf(o.Debug, "  \u2713 %s\n", p.Tool)
			case "error":
				fmt.Fprintf(o.Debug, "  \u2717 %s\n", p.Tool)
			}
		}
	}
}

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
