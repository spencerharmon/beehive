package alertmanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *hive) commitConfig(body string) {
	h.t.Helper()
	writeConfig(h.t, h.root, body)
	h.git("add", "config.yaml")
	h.git("commit", "-q", "-m", "config")
}

func TestReloadCommittedConfigLifecycle(t *testing.T) {
	t.Setenv("BEEHIVE_CONFIG_DIR", t.TempDir())
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	ctx := context.Background()
	var logs []string
	loop := &reloadingNotifier{root: h.root, instance: "hive-a", logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
	loop.tick(ctx)
	if loop.n != nil || r.count() != 0 || len(logs) != 1 || !strings.Contains(logs[0], "disabled") {
		t.Fatalf("unset should disable delivery: %v", logs)
	}
	writeConfig(t, h.root, "alertmanager_url: "+r.srv.URL+"\n")
	h.git("add", "config.yaml") // even STAGED edits must not activate
	loop.tick(ctx)
	if loop.n != nil || len(logs) != 1 {
		t.Fatal("uncommitted config activated or disabled logging repeated")
	}
	h.git("commit", "-q", "-m", "enable")
	if err := loop.reload(ctx); err != nil {
		t.Fatal(err)
	}
	c := newClock()
	loop.n.Now = c.Now
	loop.tick(ctx)
	if r.count() != 1 || len(logs) != 2 || !strings.Contains(logs[1], "config revision") {
		t.Fatalf("activation missing: %v", logs)
	}
	original := loop.n
	first := r.last(t)[0]
	h.commitConfig("alertmanager_url: " + r.srv.URL + "\npi_thinking: high\n")
	c.Add(15 * time.Second)
	loop.tick(ctx)
	if loop.n != original || r.count() != 1 {
		t.Fatal("unrelated commit reset emitter or refreshed prematurely")
	}
	c.Add(time.Minute)
	loop.tick(ctx)
	second := r.last(t)[0]
	if first["startsAt"] != second["startsAt"] || first["endsAt"] == second["endsAt"] || Key(toStr(labels(first))) != Key(toStr(labels(second))) {
		t.Fatalf("refresh changed identity/start or failed to advance expiry: %v -> %v", first, second)
	}

	// Malformed config must not silently disable and resolve genuine alerts.
	for _, bad := range []string{"alertmanager_url: [", "alertmanager_url: /", "alertmanager_url: ftp://example.com", "alertmanager_url: https://example.com/?x=1"} {
		h.commitConfig(bad)
		c.Add(time.Minute)
		before := r.count()
		loop.tick(ctx)
		if r.count() != before+1 || loop.n != original || loop.lastError == "" {
			t.Fatal("bad config failed to keep the last good emitter refreshing")
		}
	}
	h.commitConfig("# disabled; no host fallback\n")
	before := r.count()
	c.Add(time.Minute)
	loop.tick(ctx)
	if r.count() != before || loop.currentURL != "" {
		t.Fatal("disable must stop delivery without sending resolutions")
	}
	h.commitConfig("alertmanager_url: " + r.srv.URL + "\n")
	c.Add(time.Second)
	loop.tick(ctx)
	if r.count() != before+1 || r.last(t)[0]["startsAt"] != first["startsAt"] {
		t.Fatal("re-enable lost the stable startsAt")
	}
	// A new receiver gets one immediate batch, the old one expires naturally.
	r2 := newReceiver(t)
	h.commitConfig("alertmanager_url: " + r2.srv.URL + "\n")
	loop.tick(ctx)
	if r2.count() != 1 || r.count() != before+1 || loop.n != original {
		t.Fatal("endpoint switch created a parallel emitter or failed to deliver")
	}
	r2.setFail(true)
	c.Add(time.Minute)
	loop.tick(ctx)
	retryAt := loop.n.retryAt
	h.commitConfig("alertmanager_url: " + r2.srv.URL + "\npi_thinking: high\n")
	c.Add(time.Second)
	loop.tick(ctx)
	if loop.n.failures != 1 || !loop.n.retryAt.Equal(retryAt) {
		t.Fatal("unrelated committed config reset delivery backoff")
	}
}

func TestReloadHostFallbackAndStartupRepair(t *testing.T) {
	host := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", host)
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	r2 := newReceiver(t)
	writeConfig(t, host, "alertmanager_url: [")
	loop := &reloadingNotifier{root: h.root, instance: "hive-a", logf: quiet}
	loop.tick(context.Background())
	if loop.n != nil || loop.lastError == "" {
		t.Fatal("invalid startup config must not start a notifier")
	}
	writeConfig(t, host, "alertmanager_url: "+r.srv.URL+"\n")
	loop.tick(context.Background())
	if r.count() != 1 || loop.lastError != "" {
		t.Fatal("host repair did not activate without restart")
	}
	h.commitConfig("alertmanager_url: " + r2.srv.URL + "\n")
	loop.tick(context.Background())
	if r2.count() != 1 {
		t.Fatal("committed global config did not override host")
	}
	h.git("rm", "config.yaml")
	h.git("commit", "-q", "-m", "fall back to host")
	loop.tick(context.Background())
	if r.count() != 2 || loop.currentURL != r.srv.URL {
		t.Fatal("removing global config did not fall back to host")
	}
}

func TestStartReloadIsAsyncAndCancellable(t *testing.T) {
	t.Setenv("BEEHIVE_CONFIG_DIR", t.TempDir())
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	r := newReceiver(t)
	r.block = make(chan struct{})
	defer close(r.block)
	h.commitConfig("alertmanager_url: " + r.srv.URL + "\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := time.Now()
	done := Start(ctx, h.root, "hive-a", "")
	if time.Since(before) > time.Second {
		t.Fatal("Start waited for blocked delivery")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel config/delivery work")
	}
}
