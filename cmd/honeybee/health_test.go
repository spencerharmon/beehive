package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/alertmanager"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/swarm"
)

func evidence(t *testing.T, root string) *alertmanager.Health {
	t.Helper()
	p, err := alertmanager.HealthPath(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	h, err := alertmanager.LoadHealth(p)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Real honeybee processes failing preflight (ff-only pull of an unreachable
// remote) accumulate evidence across process boundaries BEFORE any session
// exists; one failure is not an alert; a later healthy preflight clears it.
func TestPreflightEvidenceAcrossHoneybeeProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds honeybee")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "honeybee")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	hive := gitInit(t, filepath.Join(tmp, "hive"))
	commitFile(t, hive, "AGENTS.md", "# fixture\n")
	if _, err := hive.Run(context.Background(), "remote", "add", "origin", filepath.Join(tmp, "missing.git")); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(tmp, "cfg")
	_ = os.MkdirAll(cfg, 0o755)
	run := func() (string, error) {
		c := exec.Command(bin, hive.Dir)
		c.Env = append(os.Environ(), "BEEHIVE_CONFIG_DIR="+cfg)
		out, err := c.CombinedOutput()
		return string(out), err
	}
	for i := 1; i <= alertmanager.WedgedThreshold; i++ {
		out, err := run()
		if err == nil || !strings.Contains(out, "preflight") {
			t.Fatalf("run %d: want preflight failure, got err=%v %s", i, err, out)
		}
		h := evidence(t, hive.Dir)
		if h.Preflight.Consecutive != i || !strings.Contains(h.Preflight.LastError, "pull origin main") {
			t.Fatalf("run %d evidence: %+v", i, h.Preflight)
		}
		alerts := alertmanager.HealthAlerts(h, "hive", "")
		if (len(alerts) == 1) != (i >= alertmanager.WedgedThreshold) {
			t.Fatalf("run %d: alerts=%v", i, alerts)
		}
	}
	if _, err := hive.Run(context.Background(), "remote", "remove", "origin"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err != nil {
		t.Fatalf("healthy run: %v %s", err, out)
	}
	if h := evidence(t, hive.Dir); h.Preflight.Consecutive != 0 || len(alertmanager.HealthAlerts(h, "hive", "")) != 0 {
		t.Fatalf("healthy preflight must clear: %+v", h.Preflight)
	}
}

// A real failing publish (git PublishToMain against an unknown remote) is
// counted, its error returned unchanged; a landed publish clears.
func TestWrapPublishRecordsRealOutcome(t *testing.T) {
	hive := gitInit(t, t.TempDir())
	commitFile(t, hive, "AGENTS.md", "x\n")
	ph := newHealth(context.Background(), hive.Dir)
	if ph.rec == nil {
		t.Fatal("recorder not initialised")
	}
	var orig error
	pub := ph.wrapPublish("s1", func(ctx context.Context) error {
		orig = git.New(hive.Dir).PublishToMain(ctx, "no-such-remote")
		return orig
	})
	for i := 0; i < 3; i++ {
		if err := pub(context.Background()); err == nil || err != orig {
			t.Fatalf("publish error must pass through unchanged: %v vs %v", err, orig)
		}
	}
	if h := evidence(t, hive.Dir); h.Publish.Consecutive != 3 {
		t.Fatalf("want 3 publish failures, got %+v", h.Publish)
	}
	ok := ph.wrapPublish("s2", func(context.Context) error { return nil })
	_ = ok(context.Background())
	if h := evidence(t, hive.Dir); h.Publish.Consecutive != 0 {
		t.Fatalf("success must clear: %+v", h.Publish)
	}
}

func TestPersistFailureWarnsButKeepsOutcome(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "f")
	_ = os.WriteFile(blocker, nil, 0o644)
	var buf bytes.Buffer
	ph := &passHealth{rec: &alertmanager.Recorder{Path: filepath.Join(blocker, "x", "health.json")}, warn: &buf}
	want := errors.New("original publish failure")
	err := ph.wrapPublish("s", func(context.Context) error { return want })(context.Background())
	if err != want {
		t.Fatalf("persistence failure replaced the original error: %v", err)
	}
	ph.pass("sm", "s", swarm.Result{}, want)
	ph.preflight("p", want)
	if n := strings.Count(buf.String(), "WARNING could not persist"); n != 3 {
		t.Fatalf("want 3 surfaced persistence warnings, got %d: %s", n, buf.String())
	}
}

func TestPassOutcomeClassification(t *testing.T) {
	cases := []struct {
		name             string
		res              swarm.Result
		err              error
		record, progress bool
	}{
		{"error", swarm.Result{}, errors.New("x"), true, false},
		{"lost-neutral", swarm.Result{Lost: true}, nil, false, false},
		{"completed", swarm.Result{Completed: true}, nil, true, true},
		{"timeout", swarm.Result{GCMarked: true, Turns: 40}, nil, true, false},
		{"no-progress", swarm.Result{Turns: 2, Warning: "task removed"}, nil, true, false},
	}
	for _, c := range cases {
		rec, prog, cause := passOutcome(c.res, c.err)
		if rec != c.record || prog != c.progress || (rec && !prog && cause == nil) {
			t.Errorf("%s: got record=%v progress=%v cause=%v", c.name, rec, prog, cause)
		}
	}
}
