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
	"strings"
)

// Opencode talks to an opencode server's session API.
type Opencode struct {
	Base  string // server base URL
	Model string // "provider/model"
	HTTP  *http.Client
	Debug io.Writer // non-nil: log each HTTP request/response
}

// NewSession creates a server session rooted at cwd and seeds the system prompt
// (AGENTS.md) plus the first user prompt. Returns the assistant's first reply.
func (o *Opencode) NewSession(ctx context.Context, cwd, system, first string) (Session, string, error) {
	body := map[string]any{"directory": cwd}
	var created struct {
		ID string `json:"id"`
	}
	if err := o.post(ctx, "/session", body, &created); err != nil {
		return nil, "", err
	}
	if created.ID == "" {
		return nil, "", fmt.Errorf("opencode: empty session id")
	}
	s := &ocSession{oc: o, id: created.ID, cwd: cwd}
	reply, err := s.Prompt(ctx, system+"\n\n"+first)
	if err != nil {
		return nil, "", err
	}
	return s, reply, nil
}

type ocSession struct {
	oc  *Opencode
	id  string
	cwd string
}

// Prompt sends text and blocks until the assistant turn completes, returning the
// assistant's concatenated text parts.
func (s *ocSession) Prompt(ctx context.Context, text string) (string, error) {
	prov, model, _ := strings.Cut(s.oc.Model, "/")
	body := map[string]any{
		"providerID": prov,
		"modelID":    model,
		"parts":      []map[string]any{{"type": "text", "text": text}},
	}
	var reply struct {
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := s.oc.post(ctx, "/session/"+s.id+"/message", body, &reply); err != nil {
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

func (s *ocSession) Close() error { return nil }

func (o *Opencode) post(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if o.Debug != nil {
		fmt.Fprintf(o.Debug, "[opencode] POST %s%s ...\n", o.Base, path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Base+path, bytes.NewReader(buf))
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
