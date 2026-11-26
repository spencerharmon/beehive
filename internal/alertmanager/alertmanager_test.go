package alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time      { return c.t }
func (c *fakeClock) Add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }
func quiet(string, ...any)               {}

// receiver is an httptest Alertmanager that records every POSTed batch.
type receiver struct {
	mu      sync.Mutex
	batches [][]map[string]any
	fail    bool
	block   chan struct{}
	srv     *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method != http.MethodPost || q.URL.Path != "/api/v2/alerts" || q.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request shape", 400)
			return
		}
		if r.block != nil {
			select {
			case <-r.block:
			case <-q.Context().Done():
				return
			}
		}
		b, _ := io.ReadAll(q.Body)
		var arr []map[string]any
		if err := json.Unmarshal(b, &arr); err != nil {
			http.Error(w, "not a JSON array", 400)
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.fail {
			http.Error(w, "down", 503)
			return
		}
		r.batches = append(r.batches, arr)
		w.WriteHeader(200)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) last(t *testing.T) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.batches) == 0 {
		t.Fatal("no batch received")
	}
	return r.batches[len(r.batches)-1]
}
func (r *receiver) count() int     { r.mu.Lock(); defer r.mu.Unlock(); return len(r.batches) }
func (r *receiver) setFail(f bool) { r.mu.Lock(); r.fail = f; r.mu.Unlock() }

// hive is a real git repo with committed PLAN.md files.
type hive struct {
	t    *testing.T
	root string
}

func newHive(t *testing.T) *hive {
	h := &hive{t: t, root: t.TempDir()}
	h.git("init", "-q", "-b", "main")
	h.git("config", "user.email", "t@example.com")
	h.git("config", "user.name", "t")
	return h
}

func (h *hive) git(args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = h.root
	if out, err := cmd.CombinedOutput(); err != nil {
		h.t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func (h *hive) commitPlan(sm, body string) {
	p := filepath.Join(h.root, "submodules", sm, "PLAN.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
	h.git("add", "-A")
	h.git("commit", "-q", "-m", "plan")
}

const planHuman = `# Plan
<!-- Beehive-ROI: abc -->

## t1 [NEEDS-HUMAN] <!-- attempts=1 deps= weight=1 category=secret -->
Do a thing.
Human-needed: populate key FOO in the secret store

## t2 [TODO] <!-- attempts=0 deps= weight=1 -->
Other.
`

const planReworded = `# Plan
<!-- Beehive-ROI: abc -->

## t1 [NEEDS-HUMAN] <!-- attempts=1 deps= weight=1 category=secret session=s-2 -->
Do a thing.
Human-needed: populate key FOO (reworded) in the secret store

## t2 [TODO] <!-- attempts=0 deps= weight=1 -->
Other.
`

const planResolved = `# Plan
<!-- Beehive-ROI: abc -->

## t1 [TODO] <!-- attempts=1 deps= weight=1 -->
Do a thing.

## t2 [TODO] <!-- attempts=0 deps= weight=1 -->
Other.
`

func notifier(h *hive, r *receiver, c *fakeClock) *Notifier {
	return &Notifier{
		Poster:  NewClient(r.srv.URL, time.Second),
		Source:  NeedsHumanSource(h.root, "HEAD", "hive-a", "https://beehive.example.com"),
		Now:     c.Now,
		Refresh: time.Minute,
		Logf:    quiet,
	}
}

func labels(a map[string]any) map[string]any { return a["labels"].(map[string]any) }

func TestSchemaLabelsAndFire(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	c := newClock()
	n := notifier(h, r, c)
	if err := n.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := r.last(t)
	if len(b) != 1 {
		t.Fatalf("want 1 alert (only NEEDS-HUMAN), got %d", len(b))
	}
	a := b[0]
	want := map[string]string{"alertname": AlertNeedsHuman, "service": "beehive", "severity": "critical",
		"instance": "hive-a", "submodule": "sm", "task": "t1", "category": "secret"}
	l := labels(a)
	if len(l) != len(want) {
		t.Fatalf("labels %v", l)
	}
	for k, v := range want {
		if l[k] != v {
			t.Fatalf("label %s=%v want %s", k, l[k], v)
		}
	}
	ann := a["annotations"].(map[string]any)
	if ann["escalation_summary"] != "populate key FOO in the secret store" {
		t.Fatalf("annotation %v", ann)
	}
	if a["generatorURL"] != "https://beehive.example.com/submodule/sm/plan#t1" {
		t.Fatalf("generatorURL %v", a["generatorURL"])
	}
	s, _ := time.Parse(time.RFC3339, a["startsAt"].(string))
	e, _ := time.Parse(time.RFC3339, a["endsAt"].(string))
	if !s.Equal(c.t) || !e.Equal(c.t.Add(4*time.Minute)) {
		t.Fatalf("startsAt %v endsAt %v", s, e)
	}
}

func TestStableIdentityAndRefresh(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	c := newClock()
	n := notifier(h, r, c)
	_ = n.Tick(context.Background())
	first := labels(r.last(t)[0])
	c.Add(10 * time.Second)
	_ = n.Tick(context.Background())
	if r.count() != 1 {
		t.Fatal("unchanged state must not resend before refresh")
	}
	h.commitPlan("sm", planReworded) // volatile summary + session changed
	_ = n.Tick(context.Background())
	if r.count() != 2 {
		t.Fatal("annotation change should resend")
	}
	b := r.last(t)
	if len(b) != 1 || Key(toStr(labels(b[0]))) != Key(toStr(first)) {
		t.Fatalf("dedup identity changed: %v vs %v", labels(b[0]), first)
	}
	s, _ := time.Parse(time.RFC3339, b[0]["startsAt"].(string))
	if !s.Equal(newClock().t) {
		t.Fatal("startsAt must be preserved")
	}
	c.Add(time.Minute)
	_ = n.Tick(context.Background())
	if r.count() != 3 {
		t.Fatal("must refresh active alerts each Refresh interval")
	}
	e, _ := time.Parse(time.RFC3339, r.last(t)[0]["endsAt"].(string))
	if !e.Equal(c.t.Add(4 * time.Minute)) {
		t.Fatal("refresh must extend endsAt")
	}
}

func toStr(m map[string]any) map[string]string {
	o := map[string]string{}
	for k, v := range m {
		o[k] = v.(string)
	}
	return o
}

func TestResolveAndRetryResolution(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	c := newClock()
	n := notifier(h, r, c)
	_ = n.Tick(context.Background())
	h.commitPlan("sm", planResolved)
	r.setFail(true)
	c.Add(time.Second)
	if err := n.Tick(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	c.Add(time.Second) // still within backoff
	_ = n.Tick(context.Background())
	r.setFail(false)
	c.Add(DefaultMinBackoff)
	if err := n.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := r.last(t)
	if len(b) != 1 {
		t.Fatalf("want retained resolution, got %v", b)
	}
	e, _ := time.Parse(time.RFC3339, b[0]["endsAt"].(string))
	if !e.Equal(newClock().t.Add(time.Second)) {
		t.Fatalf("resolve endsAt %v must be the time it left NEEDS-HUMAN", e)
	}
	c.Add(2 * time.Minute)
	_ = n.Tick(context.Background())
	if r.count() != 2 {
		t.Fatal("resolution must be delivered once and then dropped")
	}
}

func TestRetryFiringWithBoundedBackoff(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	r.setFail(true)
	c := newClock()
	n := notifier(h, r, c)
	n.MinBackoff, n.MaxBackoff = time.Second, 4*time.Second
	var attempts []time.Time
	for i := 0; i < 20; i++ {
		before := n.failures
		_ = n.Tick(context.Background())
		if n.failures != before {
			attempts = append(attempts, c.t)
		}
		c.Add(time.Second)
	}
	// backoff 1,2,4,4,4... seconds
	for i := 1; i < len(attempts); i++ {
		if gap := attempts[i].Sub(attempts[i-1]); gap > 4*time.Second {
			t.Fatalf("backoff unbounded: gap %v", gap)
		}
	}
	if len(attempts) < 4 || attempts[2].Sub(attempts[1]) != 2*time.Second {
		t.Fatalf("expected exponential backoff, attempts %v", attempts)
	}
	r.setFail(false)
	c.Add(5 * time.Second)
	if err := n.Tick(context.Background()); err != nil || r.count() != 1 {
		t.Fatalf("firing retry not delivered: %v", err)
	}
}

func TestRestartReconcilesFromRepo(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	c := newClock()
	_ = notifier(h, r, c).Tick(context.Background())
	// a fresh process (restart) with no memory re-fires the same identity
	c.Add(time.Second)
	_ = notifier(h, r, c).Tick(context.Background())
	if r.count() != 2 || Key(toStr(labels(r.batches[0][0]))) != Key(toStr(labels(r.batches[1][0]))) {
		t.Fatal("restart must reconcile committed NEEDS-HUMAN tasks with the same identity")
	}
}

func TestSlowReceiverBoundedOffPassPath(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	r.block = make(chan struct{})
	defer close(r.block)
	c := newClock()
	n := notifier(h, r, c)
	n.Timeout = 50 * time.Millisecond
	err := n.Tick(context.Background())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) && n.failures != 1 {
		t.Fatalf("slow receiver must time out and count as failure: %v", err)
	}
	// unavailable receiver
	n2 := &Notifier{Poster: NewClient("http://127.0.0.1:1", 50*time.Millisecond), Source: n.Source, Now: c.Now, Logf: quiet}
	if err := n2.Tick(context.Background()); err == nil {
		t.Fatal("unavailable receiver must surface error to the notifier only")
	}
}
