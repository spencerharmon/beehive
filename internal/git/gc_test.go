package git

import (
	"context"
	"os"
	"path/filepath"
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

func TestSweepStaleGCTemp(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()
	packDir := filepath.Join(r.Dir, ".git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(name string, age time.Duration) string {
		p := filepath.Join(packDir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}

	stalePack := write("tmp_pack_STALE", 5*time.Hour)   // older than gate -> removed
	staleIdx := write("tmp_idx_STALE", 5*time.Hour)     // older than gate -> removed
	staleRev := write("tmp_rev_STALE", 5*time.Hour)     // older than gate -> removed
	freshPack := write("tmp_pack_FRESH", 1*time.Minute) // in-flight -> kept
	realPack := write("pack-abc123.pack", 5*time.Hour)  // not a temp -> kept

	n := r.sweepStaleGCTemp(ctx, gcStaleTempAge)
	if n != 3 {
		t.Errorf("swept %d, want 3 (the three stale tmp_* files)", n)
	}
	for _, p := range []string{stalePack, staleIdx, staleRev} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale temp not removed: %s", filepath.Base(p))
		}
	}
	for _, p := range []string{freshPack, realPack} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("non-stale file wrongly removed: %s (%v)", filepath.Base(p), err)
		}
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

// TestPackDirStat proves the read-only object-store stat behind the hygiene panel:
// a fresh repo reports a zero PackStat (no error even if objects/pack is absent),
// and a fabricated pack dir with N live pack-*.pack (each with its .idx), M repack
// temps (tmp_pack_/tmp_idx_/tmp_rev_) reports Packs==N (only .pack, never .idx),
// Temps==M, and SizeBytes == the sum of every file. It STATS ONLY: every seeded
// file is still present afterward.
func TestPackDirStat(t *testing.T) {
	r := initRepo(t)
	ctx := context.Background()

	// Fresh repo: a zero stat, never an error (objects/pack empty or unpopulated).
	st, err := r.PackDirStat(ctx)
	if err != nil {
		t.Fatalf("PackDirStat (fresh): %v", err)
	}
	if st.Packs != 0 || st.Temps != 0 {
		t.Fatalf("fresh repo: packs=%d temps=%d, want 0/0", st.Packs, st.Temps)
	}

	packDir := filepath.Join(r.Dir, ".git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(packDir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 2 live packs (each .pack + .idx), plus one leftover of each temp class.
	write("pack-aaaaaaaa.pack", 100)
	write("pack-aaaaaaaa.idx", 20)
	write("pack-bbbbbbbb.pack", 200)
	write("pack-bbbbbbbb.idx", 20)
	write("tmp_pack_1", 10)
	write("tmp_idx_1", 5)
	write("tmp_rev_1", 5)
	const wantSize = 100 + 20 + 200 + 20 + 10 + 5 + 5

	st, err = r.PackDirStat(ctx)
	if err != nil {
		t.Fatalf("PackDirStat: %v", err)
	}
	if st.Packs != 2 {
		t.Errorf("packs = %d, want 2 (pack-*.pack only, not .idx)", st.Packs)
	}
	if st.Temps != 3 {
		t.Errorf("temps = %d, want 3 (tmp_pack_/tmp_idx_/tmp_rev_)", st.Temps)
	}
	if st.SizeBytes != wantSize {
		t.Errorf("size = %d, want %d (sum of every file)", st.SizeBytes, wantSize)
	}

	// Read-only: nothing the stat touched was removed.
	for _, n := range []string{"pack-aaaaaaaa.pack", "tmp_pack_1", "tmp_idx_1", "tmp_rev_1"} {
		if _, err := os.Stat(filepath.Join(packDir, n)); err != nil {
			t.Errorf("stat removed %s: %v", n, err)
		}
	}
}
