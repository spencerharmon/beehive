package git

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestDisableAutoGC(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()
	if err := r.DisableAutoGC(ctx); err != nil {
		t.Fatalf("DisableAutoGC: %v", err)
	}
	for _, kv := range [][2]string{
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
		{"fetch.writeCommitGraph", "false"},
	} {
		if got := r.configGetLocal(ctx, kv[0]); got != kv[1] {
			t.Errorf("%s = %q, want %q", kv[0], got, kv[1])
		}
	}
	// Idempotent: a second call is a no-op and does not error.
	if err := r.DisableAutoGC(ctx); err != nil {
		t.Fatalf("DisableAutoGC (2nd): %v", err)
	}
}

func TestMaybeGCIntervalGate(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()

	// First call: no prior run recorded => gc runs.
	ran, err := r.MaybeGC(ctx, "sess-a", GCConfig{Interval: time.Hour})
	if err != nil {
		t.Fatalf("MaybeGC #1: %v", err)
	}
	if !ran {
		t.Fatal("MaybeGC #1: expected gc to run on a repo with no recorded lastrun")
	}
	// Interval seeded and lastrun recorded.
	if got := r.configGetLocal(ctx, cfgGCInterval); got != time.Hour.String() {
		t.Errorf("beehive.gc.interval = %q, want %q", got, time.Hour.String())
	}
	if r.configGetLocal(ctx, cfgGCLastRun) == "" {
		t.Error("beehive.gc.lastrun not recorded after a gc run")
	}
	// Lock released.
	if got := r.configGetLocal(ctx, cfgGCLockedBy); got != "" {
		t.Errorf("gc lock not released: lockedby=%q", got)
	}

	// Second call within the interval: gated off.
	ran, err = r.MaybeGC(ctx, "sess-a", GCConfig{Interval: time.Hour})
	if err != nil {
		t.Fatalf("MaybeGC #2: %v", err)
	}
	if ran {
		t.Error("MaybeGC #2: expected interval gate to skip gc")
	}

	// Force lastrun into the past: gate opens, gc runs again.
	if err := r.configSetLocal(ctx, cfgGCLastRun, strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)); err != nil {
		t.Fatal(err)
	}
	ran, err = r.MaybeGC(ctx, "sess-a", GCConfig{Interval: time.Hour})
	if err != nil {
		t.Fatalf("MaybeGC #3: %v", err)
	}
	if !ran {
		t.Error("MaybeGC #3: expected gc to run once the interval elapsed")
	}
}

func TestMaybeGCLockHeldByPeer(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()

	// A peer holds a FRESH lock; even though no gc has ever run, we must defer.
	if err := r.configSetLocal(ctx, cfgGCLockedBy, "sess-peer"); err != nil {
		t.Fatal(err)
	}
	if err := r.configSetLocal(ctx, cfgGCLockedAt, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		t.Fatal(err)
	}
	ran, err := r.MaybeGC(ctx, "sess-mine", GCConfig{Interval: time.Hour, LockTTL: time.Hour})
	if err != nil {
		t.Fatalf("MaybeGC (peer lock): %v", err)
	}
	if ran {
		t.Error("expected to defer to a peer holding a fresh gc lock")
	}
	// The peer's lock must be untouched (we didn't steal a fresh one).
	if got := r.configGetLocal(ctx, cfgGCLockedBy); got != "sess-peer" {
		t.Errorf("peer lock mutated: lockedby=%q", got)
	}

	// Now age the peer's lock past the TTL: it is stale and gets stolen; gc runs.
	if err := r.configSetLocal(ctx, cfgGCLockedAt, strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)); err != nil {
		t.Fatal(err)
	}
	ran, err = r.MaybeGC(ctx, "sess-mine", GCConfig{Interval: time.Hour, LockTTL: time.Hour})
	if err != nil {
		t.Fatalf("MaybeGC (stale peer lock): %v", err)
	}
	if !ran {
		t.Error("expected to steal a stale peer lock and run gc")
	}
	if got := r.configGetLocal(ctx, cfgGCLockedBy); got != "" {
		t.Errorf("gc lock not released after stealing: lockedby=%q", got)
	}
}
