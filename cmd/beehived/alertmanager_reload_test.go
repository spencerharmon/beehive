package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/alertmanager"
)

// This exercises the REAL main wiring, not just a controller which the daemon
// could forget to start. No live service is contacted: the process, repository,
// host config and HTTP receiver all belong to this test.
func TestDaemonReloadsCommittedAlertmanagerConfig(t *testing.T) {
	work := t.TempDir()
	bin := filepath.Join(work, "beehived")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	root := filepath.Join(work, "hive")
	host := filepath.Join(work, "host")
	for _, dir := range []string{root, host, filepath.Join(root, "submodules", "fixture")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "test")
	git("config", "user.email", "test@example.com")
	write("AGENTS.md", "# fixture hive\n")
	write("submodules/fixture/PLAN.md", "# Plan\n\n## blocked [NEEDS-HUMAN] <!-- attempts=0 deps= weight=1 category=secret -->\nHuman-needed: fixture secret\n")
	git("add", "-A")
	git("commit", "-qm", "fixture")
	posts := make(chan []alertmanager.Alert, 8)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/alerts" {
			http.Error(w, "unexpected request", 400)
			return
		}
		var alerts []alertmanager.Alert
		if err := json.NewDecoder(r.Body).Decode(&alerts); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		select {
		case posts <- alerts:
		default:
			http.Error(w, "unexpected extra delivery", 500)
		}
	}))
	defer receiver.Close()
	logPath := filepath.Join(work, "daemon.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-repo", root, "-addr", "127.0.0.1:0")
	cmd.Env = append(os.Environ(), "BEEHIVE_CONFIG_DIR="+host)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel() // only the test-owned subprocess; never a deployed daemon
		_ = cmd.Wait()
		b, _ := os.ReadFile(logPath)
		t.Logf("daemon log:\n%s", b)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "alertmanager: disabled") && strings.Contains(string(b), "beehived listening") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon not ready and disabled:\n%s", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	write("config.yaml", "alertmanager_url: "+receiver.URL+"\n")
	git("add", "config.yaml")
	git("commit", "-qm", "enable after daemon startup")
	select {
	case alerts := <-posts:
		if len(alerts) != 1 || alerts[0].Labels["alertname"] != "BeehiveNeedsHuman" || alerts[0].Labels["task"] != "blocked" || alerts[0].Labels["instance"] != "hive" {
			t.Fatalf("wrong committed condition: %+v", alerts)
		}
	case <-time.After(22 * time.Second):
		t.Fatal("running production binary did not load committed config within its 15s poll interval; startup-only wiring regression")
	}
}
