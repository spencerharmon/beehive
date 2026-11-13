// Package alertmanager emits Prometheus Alertmanager v2 alerts derived from
// COMMITTED beehive repo state.
//
// This is the one narrow, OUTBOUND-ONLY exception to beehive's repo-only rule:
// all notification state is computed from the git repository (today: tasks whose
// committed PLAN.md status is NEEDS-HUMAN); Alertmanager is only ever POSTed to,
// never read back, never a data source, and never on a honeybee pass path. A
// failed or slow receiver is logged and retried with bounded backoff from a
// background goroutine in beehived; it can never block or fail a pass.
//
// Identity: an alert's dedup identity is its label set only — alertname,
// service=beehive, severity, instance (the stable registry repo name), and for
// BeehiveNeedsHuman submodule, task and category. Volatile data (the escalation
// summary, timestamps, session ids) lives in annotations / startsAt / endsAt and
// never in labels, so a re-worded reason or a restart never forks a new alert.
//
// Lifecycle: every firing alert is re-sent each Refresh interval with
// endsAt = now + 4*Refresh (the Prometheus convention), so Alertmanager keeps it
// active while beehived is healthy and auto-resolves it if beehived disappears.
// When a task leaves NEEDS-HUMAN its alert is sent once with endsAt = now; a
// failed resolution is retained and retried until delivered.
//
// Restart/recovery: on start the notifier reconciles from the repository:
// every currently-desired alert is (re)fired immediately (Alertmanager merges it
// into the existing alert by fingerprint). With StatePath set (beehived sets it
// to <git-common-dir>/beehive/alertmanager-sent.json) the last delivered firing
// set is reloaded, so an alert whose condition cleared while beehived was down
// is explicitly resolved on the first tick; without it, such an alert resolves
// by expiry of the endsAt its last refresh carried (<= 4*Refresh later).
//
// Sources: BeehiveNeedsHuman (committed PLAN.md) plus BeehiveSwarmWedged and
// BeehivePassFailing (repo-local pass outcome evidence, see health.go), all
// through this one emitter.
package alertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spencerharmon/beehive/internal/fsatomic"
)

// Alert is one Alertmanager v2 postable alert.
type Alert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

// Key is the stable dedup identity of an alert: its sorted label set.
func Key(labels map[string]string) string {
	ks := make([]string, 0, len(labels))
	for k := range labels {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b strings.Builder
	for _, k := range ks {
		fmt.Fprintf(&b, "%s=%q,", k, labels[k])
	}
	return b.String()
}

// Poster delivers a batch of alerts.
type Poster interface {
	Post(ctx context.Context, alerts []Alert) error
}

// Client is the HTTP Alertmanager v2 client.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client with a bounded per-request timeout.
func NewClient(base string, timeout time.Duration) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: timeout}}
}

// Post sends alerts as a JSON array to <base>/api/v2/alerts.
func (c *Client) Post(ctx context.Context, alerts []Alert) error {
	body, err := json.Marshal(alerts)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v2/alerts", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("alertmanager: POST %s/api/v2/alerts: %s: %s", c.BaseURL, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Desired is an alert that SHOULD currently be firing (from repo state).
type Desired struct {
	Labels       map[string]string
	Annotations  map[string]string
	GeneratorURL string
}

// Source computes the currently-desired firing alerts from repository state.
type Source func(ctx context.Context) ([]Desired, error)

// Defaults for the notifier loop.
const (
	DefaultRefresh    = time.Minute // well under Alertmanager's 5m default resolve_timeout
	DefaultTimeout    = 10 * time.Second
	DefaultMinBackoff = 5 * time.Second
	DefaultMaxBackoff = 5 * time.Minute
)

// Notifier reconciles Source against what it has sent and POSTs the delta.
// Tick is the deterministic single step (tests drive it with a fake clock);
// Run loops it.
type Notifier struct {
	Poster     Poster
	Source     Source
	Now        func() time.Time
	Refresh    time.Duration
	Timeout    time.Duration
	MinBackoff time.Duration
	MaxBackoff time.Duration
	Logf       func(format string, args ...any)
	// StatePath, when set, persists the firing set after each delivery so a
	// restarted (or one-shot) notifier still sends an explicit endsAt=now for an
	// alert whose condition cleared while it was not running. Best-effort: a
	// load/save failure is logged and never blocks delivery.
	StatePath string

	mu       sync.Mutex
	active   map[string]Alert // key -> last firing alert (startsAt preserved)
	resolve  map[string]Alert // key -> pending resolution (retained until delivered)
	lastSent time.Time
	dirty    bool
	failures int
	retryAt  time.Time
}

func (n *Notifier) defaults() {
	if n.Now == nil {
		n.Now = time.Now
	}
	if n.Refresh <= 0 {
		n.Refresh = DefaultRefresh
	}
	if n.Timeout <= 0 {
		n.Timeout = DefaultTimeout
	}
	if n.MinBackoff <= 0 {
		n.MinBackoff = DefaultMinBackoff
	}
	if n.MaxBackoff <= 0 {
		n.MaxBackoff = DefaultMaxBackoff
	}
	if n.Logf == nil {
		n.Logf = log.Printf
	}
	if n.active == nil {
		n.active = map[string]Alert{}
		n.resolve = map[string]Alert{}
		n.dirty = true // first tick after (re)start always reconciles
		n.loadState()
	}
}

// Tick runs one reconcile+delivery step. It returns the delivery error (also
// logged); a nil error with nothing due is a no-op.
func (n *Notifier) Tick(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.defaults()
	now := n.Now()

	desired, err := n.Source(ctx)
	if err != nil {
		n.Logf("alertmanager: read repo state: %v", err)
		return err
	}
	seen := map[string]bool{}
	for _, d := range desired {
		k := Key(d.Labels)
		seen[k] = true
		prev, ok := n.active[k]
		a := Alert{Labels: d.Labels, Annotations: d.Annotations, GeneratorURL: d.GeneratorURL, StartsAt: now}
		if ok {
			a.StartsAt = prev.StartsAt
			if !equalMap(prev.Annotations, a.Annotations) {
				n.dirty = true
			}
		} else {
			n.dirty = true
		}
		delete(n.resolve, k) // re-fired before its resolution landed
		n.active[k] = a
	}
	for k, a := range n.active {
		if !seen[k] {
			a.EndsAt = now
			n.resolve[k] = a
			delete(n.active, k)
			n.dirty = true
		}
	}

	due := n.dirty || now.Sub(n.lastSent) >= n.Refresh
	if !due {
		return nil
	}
	if n.failures > 0 && now.Before(n.retryAt) {
		return nil // backing off
	}
	batch := make([]Alert, 0, len(n.active)+len(n.resolve))
	for _, a := range n.active {
		a.EndsAt = now.Add(4 * n.Refresh)
		batch = append(batch, a)
	}
	for _, a := range n.resolve {
		batch = append(batch, a)
	}
	sort.Slice(batch, func(i, j int) bool { return Key(batch[i].Labels) < Key(batch[j].Labels) })
	if len(batch) == 0 {
		n.dirty = false
		n.lastSent = now
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, n.Timeout)
	err = n.Poster.Post(pctx, batch)
	cancel()
	if err != nil {
		n.failures++
		b := n.MinBackoff << (n.failures - 1)
		if b > n.MaxBackoff || b <= 0 {
			b = n.MaxBackoff
		}
		n.retryAt = now.Add(b)
		n.Logf("alertmanager: deliver %d alert(s) failed (attempt %d, retry in %s): %v", len(batch), n.failures, b, err)
		return err
	}
	n.failures = 0
	n.dirty = false
	n.lastSent = now
	n.resolve = map[string]Alert{}
	n.saveState()
	return nil
}

func (n *Notifier) loadState() {
	if n.StatePath == "" {
		return
	}
	b, err := os.ReadFile(n.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var prev []Alert
	if err == nil {
		err = json.Unmarshal(b, &prev)
	}
	if err != nil {
		n.Logf("alertmanager: load notifier state %s: %v", n.StatePath, err)
		return
	}
	for _, a := range prev {
		n.active[Key(a.Labels)] = a
	}
}

func (n *Notifier) saveState() {
	if n.StatePath == "" {
		return
	}
	list := make([]Alert, 0, len(n.active))
	for _, a := range n.active {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return Key(list[i].Labels) < Key(list[j].Labels) })
	b, err := json.Marshal(list)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(n.StatePath), 0o755); err == nil {
			err = fsatomic.WriteFile(n.StatePath, b, 0o644)
		}
	}
	if err != nil {
		n.Logf("alertmanager: save notifier state %s: %v", n.StatePath, err)
	}
}

// Run ticks every interval until ctx is done. interval should be well under
// Refresh so retries/changes are prompt.
func (n *Notifier) Run(ctx context.Context, interval time.Duration) {
	_ = n.Tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = n.Tick(ctx)
		}
	}
}

func equalMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
