package alertmanager

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func harnessPath(t *testing.T) string {
	p, err := filepath.Abs("../../tests/alertmanager-live.test.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func runHarness(t *testing.T, env ...string) (string, error) {
	cmd := exec.Command("bash", harnessPath(t))
	base := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ALERTMANAGER_URL=") && !strings.HasPrefix(kv, "BEEHIVE_CONFIG_DIR=") {
			base = append(base, kv)
		}
	}
	cmd.Env = append(base, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestLiveHarnessFailsWithoutEndpoint(t *testing.T) {
	out, err := runHarness(t)
	if err == nil || !strings.Contains(out, "ALERTMANAGER_URL is not set") {
		t.Fatalf("harness must FAIL (not skip) without ALERTMANAGER_URL; err=%v out=%s", err, out)
	}
}

func TestLiveHarnessFailsOnUnreachableEndpoint(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	out, err := runHarness(t, "ALERTMANAGER_URL=http://"+addr)
	if err == nil || !strings.Contains(out, "unreachable") {
		t.Fatalf("harness must fail on an unreachable endpoint; err=%v out=%s", err, out)
	}
}

func TestLiveHarnessFailsOnMissingPrerequisite(t *testing.T) {
	// PATH with only bash/env: go/git/curl/jq absent.
	dir := t.TempDir()
	for _, b := range []string{"bash", "dirname"} {
		p, err := exec.LookPath(b)
		if err != nil {
			t.Skip(b + " unavailable")
		}
		if err := os.Symlink(p, filepath.Join(dir, b)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(filepath.Join(dir, "bash"), harnessPath(t))
	cmd.Env = []string{"PATH=" + dir, "ALERTMANAGER_URL=http://192.0.2.1"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "required prerequisite") {
		t.Fatalf("harness must fail on a missing prerequisite; err=%v out=%s", err, out)
	}
}

// fakeAM is a minimal Alertmanager v2 (POST merge by label set, GET active with
// an exact-label filter) used to prove the harness's happy path end to end.
type fakeAM struct {
	mu     sync.Mutex
	alerts map[string]map[string]any
	posts  []map[string]any
}

var filterRE = regexp.MustCompile(`^(\w+)="(.*)"$`)

func (f *fakeAM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	switch {
	case r.URL.Path == "/api/v2/status":
		w.Write([]byte(`{}`))
	case r.URL.Path == "/api/v2/alerts" && r.Method == http.MethodPost:
		b, _ := io.ReadAll(r.Body)
		var in []map[string]any
		if err := json.Unmarshal(b, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for _, a := range in {
			f.posts = append(f.posts, a)
			lb := map[string]string{}
			for k, v := range a["labels"].(map[string]any) {
				lb[k] = v.(string)
			}
			k := Key(lb)
			if prev, ok := f.alerts[k]; ok {
				if e, _ := time.Parse(time.RFC3339Nano, prev["endsAt"].(string)); e.After(now) {
					a["startsAt"] = prev["startsAt"]
				}
			}
			f.alerts[k] = a
		}
	case r.URL.Path == "/api/v2/alerts":
		var want []string
		for _, fl := range r.URL.Query()["filter"] {
			if m := filterRE.FindStringSubmatch(fl); m != nil {
				want = append(want, m[1], m[2])
			}
		}
		out := []map[string]any{}
	next:
		for _, a := range f.alerts {
			if e, _ := time.Parse(time.RFC3339Nano, a["endsAt"].(string)); !e.After(now) {
				continue
			}
			lb := a["labels"].(map[string]any)
			for i := 0; i < len(want); i += 2 {
				if lb[want[i]] != want[i+1] {
					continue next
				}
			}
			out = append(out, a)
		}
		json.NewEncoder(w).Encode(out)
	default:
		http.NotFound(w, r)
	}
}

func TestLiveHarnessPassesAgainstFakeAlertmanager(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries")
	}
	for _, b := range []string{"go", "git", "curl", "python3"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skip(b + " unavailable for the in-process harness run")
		}
	}
	f := &fakeAM{alerts: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	out, err := runHarness(t, "ALERTMANAGER_URL="+srv.URL)
	t.Log(out)
	if err != nil || !strings.Contains(out, "PASS") {
		t.Fatalf("harness: err=%v\n%s", err, out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Assert the harness actually exercised ALL families through background
	// refresh and explicit resolution, rather than merely printing PASS after
	// two one-shot invocations. Cleanup cannot manufacture refresh evidence.
	for _, name := range []string{AlertNeedsHuman, AlertSwarmWedged, AlertPassFailing} {
		var starts, firstEnd, latestEnd time.Time
		resolved := false
		for _, a := range f.posts {
			if a["labels"].(map[string]any)["alertname"] != name {
				continue
			}
			s, _ := time.Parse(time.RFC3339Nano, a["startsAt"].(string))
			e, _ := time.Parse(time.RFC3339Nano, a["endsAt"].(string))
			if starts.IsZero() {
				starts, firstEnd = s, e
			}
			if !s.Equal(starts) {
				t.Fatalf("%s startsAt changed: %v -> %v", name, starts, s)
			}
			if e.After(latestEnd) {
				latestEnd = e
			}
			if e.Before(firstEnd) {
				resolved = true // explicit endsAt before the original natural expiry
			}
		}
		if starts.IsZero() || latestEnd.Sub(firstEnd) < 55*time.Second || !resolved {
			t.Errorf("%s: missing firing/background refresh/explicit resolution: start=%v extension=%v resolved=%v", name, starts, latestEnd.Sub(firstEnd), resolved)
		}
	}
	for _, a := range f.alerts {
		if e, _ := time.Parse(time.RFC3339Nano, a["endsAt"].(string)); e.After(time.Now()) {
			t.Fatalf("harness left an active alert: %v", a)
		}
	}
}
