package alertmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
)

func names(ds []Desired) []string {
	var out []string
	for _, d := range ds {
		n := d.Labels["alertname"]
		if s := d.Labels["submodule"]; s != "" {
			n += ":" + s
		}
		out = append(out, n)
	}
	return out
}

// freshRecorder simulates a NEW short-lived runner process: a brand-new
// Recorder resolved from the hive root, sharing nothing in memory.
func freshRecorder(t *testing.T, root string, clk *fakeClock) *Recorder {
	r, err := NewRecorder(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	r.Now = clk.Now
	return r
}

func healthAt(t *testing.T, root string) []Desired {
	d, err := HealthSource(root, "hive", "")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSwarmWedgedThresholdAndReset(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planResolved)
	clk := newClock()
	pf := errors.New("dirty-tree heal: still dirty")
	for i := 1; i < WedgedThreshold; i++ {
		if err := freshRecorder(t, h.root, clk).Preflight(fmt.Sprintf("pid-%d", i), pf); err != nil {
			t.Fatal(err)
		}
		if d := healthAt(t, h.root); len(d) != 0 {
			t.Fatalf("below threshold (%d) must not alert: %v", i, names(d))
		}
	}
	_ = freshRecorder(t, h.root, clk).Preflight("pid-x", pf)
	d := healthAt(t, h.root)
	if len(d) != 1 || d[0].Labels["alertname"] != AlertSwarmWedged || d[0].Annotations["stage"] != "preflight" {
		t.Fatalf("want one SwarmWedged(preflight), got %+v", d)
	}
	if len(d[0].Labels) != 4 || d[0].Labels["instance"] != "hive" || d[0].Labels["severity"] != "critical" {
		t.Fatalf("labels must be the bounded stable identity only: %v", d[0].Labels)
	}
	// A 4th failure updates annotations, never identity.
	_ = freshRecorder(t, h.root, clk).Preflight("pid-y", pf)
	d2 := healthAt(t, h.root)
	if Key(d2[0].Labels) != Key(d[0].Labels) || d2[0].Annotations["consecutive"] != "4" {
		t.Fatalf("repetition must keep identity and bump evidence: %v", d2[0])
	}
	// Preflight success resets.
	_ = freshRecorder(t, h.root, clk).Preflight("pid-z", nil)
	if d := healthAt(t, h.root); len(d) != 0 {
		t.Fatalf("successful preflight must clear: %v", names(d))
	}
}

func TestPublishWedgeConflictIgnoredSuccessClearsBoth(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planResolved)
	clk := newClock()
	pub := errors.New("ff-only pull: not possible to fast-forward")
	_ = freshRecorder(t, h.root, clk).Preflight("p", errors.New("pull origin main"))
	for i := 0; i < WedgedThreshold; i++ {
		_ = freshRecorder(t, h.root, clk).Publish("s", pub)
		// interleaved peer-race conflicts neither count nor reset
		_ = freshRecorder(t, h.root, clk).Publish("s", fmt.Errorf("merge: %w", git.ErrConflict))
	}
	d := healthAt(t, h.root)
	if len(d) != 1 || d[0].Annotations["stage"] != "publish" || d[0].Annotations["consecutive"] != "3" {
		t.Fatalf("want SwarmWedged(publish x3), got %+v", d)
	}
	_ = freshRecorder(t, h.root, clk).Publish("s", nil)
	hp, _ := HealthPath(context.Background(), h.root)
	st, _ := LoadHealth(hp)
	if st.Publish.Consecutive != 0 || st.Preflight.Consecutive != 0 || len(healthAt(t, h.root)) != 0 {
		t.Fatalf("landed publish must clear publish AND preflight: %+v", st)
	}
}

func TestPassFailingPerSubmoduleNoProgress(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planResolved)
	clk := newClock()
	causes := []error{errors.New("agent error"), errors.New("pass ended without progress after 40 turn(s) (timed out / capped; claim left for GC)"), errors.New("pass ended without progress after 3 turn(s)")}
	for i, c := range causes {
		_ = freshRecorder(t, h.root, clk).Pass("a", fmt.Sprintf("a-%d", i), false, c)
	}
	_ = freshRecorder(t, h.root, clk).Pass("b", "b-1", false, causes[0])
	d := healthAt(t, h.root)
	if got := names(d); len(got) != 1 || got[0] != AlertPassFailing+":a" {
		t.Fatalf("want only PassFailing:a (b had one failure), got %v", got)
	}
	if d[0].Annotations["last_session"] != "a-2" {
		t.Fatalf("evidence lost: %v", d[0].Annotations)
	}
	_ = freshRecorder(t, h.root, clk).Pass("a", "a-ok", true, nil)
	if d := healthAt(t, h.root); len(d) != 0 {
		t.Fatalf("progress must clear: %v", names(d))
	}
}

// End to end through the shared notifier: firing then explicit endsAt on
// recovery, and a restart (fresh notifier, persisted sent-set) still resolves.
func TestHealthAlertsResolveThroughNotifierAcrossRestart(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planHuman)
	rcv := newReceiver(t)
	clk := newClock()
	mk := func() *Notifier {
		n, err := NewHiveNotifier(context.Background(), rcv.srv.URL, h.root, "hive", "")
		if err != nil {
			t.Fatal(err)
		}
		n.Now, n.Logf = clk.Now, quiet
		return n
	}
	for i := 0; i < PassFailingThreshold; i++ {
		_ = freshRecorder(t, h.root, clk).Pass("sm", "s", false, errors.New("boom"))
	}
	if err := mk().Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(rcv.last(t)); got != 2 {
		t.Fatalf("want NeedsHuman + PassFailing in one batch, got %d", got)
	}
	// Recovery while "beehived" is down; a NEW notifier must still send endsAt=now.
	_ = freshRecorder(t, h.root, clk).Pass("sm", "s2", true, nil)
	clk.Add(time.Minute)
	if err := mk().Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var resolved bool
	for _, a := range rcv.last(t) {
		lb := a["labels"].(map[string]any)
		if lb["alertname"] == AlertPassFailing {
			e, _ := time.Parse(time.RFC3339, a["endsAt"].(string))
			resolved = e.Equal(clk.Now())
		}
	}
	if !resolved {
		t.Fatalf("restarted notifier must explicitly resolve the cleared alert: %v", rcv.last(t))
	}
}

// Recording never touches the network: a hung receiver cannot delay it, and a
// Tick against it is bounded by the client timeout.
func TestSlowReceiverNeverBlocksRecording(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planResolved)
	rcv := newReceiver(t)
	rcv.block = make(chan struct{})
	defer close(rcv.block)
	clk := newClock()
	for i := 0; i < WedgedThreshold; i++ {
		_ = freshRecorder(t, h.root, clk).Preflight("p", errors.New("x"))
	}
	n, _ := NewHiveNotifier(context.Background(), rcv.srv.URL, h.root, "hive", "")
	n.Now, n.Logf, n.Timeout = clk.Now, quiet, 200*time.Millisecond
	done := make(chan error, 1)
	go func() { done <- n.Tick(context.Background()) }()
	start := time.Now()
	if err := freshRecorder(t, h.root, clk).Pass("z", "s", false, errors.New("y")); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 150*time.Millisecond {
		t.Fatal("recording waited on the receiver")
	}
	if err := <-done; err == nil {
		t.Fatal("hung receiver must surface as a bounded delivery error")
	}
}

func TestPersistFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Recorder{Path: filepath.Join(blocker, "beehive", "health.json")}
	if err := r.Preflight("p", errors.New("orig")); err == nil {
		t.Fatal("unwritable evidence must surface an error")
	}
	var nilRec *Recorder
	if err := nilRec.Pass("a", "s", false, nil); err == nil {
		t.Fatal("nil recorder must surface an error")
	}
}

func TestCorruptEvidenceIsResetAndSurfaced(t *testing.T) {
	h := newHive(t)
	h.commitPlan("sm", planResolved)
	hp, _ := HealthPath(context.Background(), h.root)
	_ = os.MkdirAll(filepath.Dir(hp), 0o755)
	_ = os.WriteFile(hp, []byte("{not json"), 0o644)
	if _, err := HealthSource(h.root, "hive", "")(context.Background()); err == nil {
		t.Fatal("corrupt evidence must fail the read (notifier keeps last state)")
	}
	if err := freshRecorder(t, h.root, newClock()).Pass("a", "s", false, errors.New("x")); err == nil {
		t.Fatal("reset of corrupt evidence must be surfaced")
	}
	st, err := LoadHealth(hp)
	if err != nil || st.Passes["a"].Consecutive != 1 {
		t.Fatalf("recording must continue after reset: %v %+v", err, st)
	}
}
