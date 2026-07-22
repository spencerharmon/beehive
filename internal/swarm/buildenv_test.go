package swarm

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// hostBuildEnv is a representative host build env (the LOCALS.md-documented fix:
// static link + tmp/cache redirected off a broken /tmp/cgo linker).
var hostBuildEnv = map[string]string{
	"CGO_ENABLED": "0",
	"GOTMPDIR":    "/n/gotmp",
	"TMPDIR":      "/n/gotmp",
	"GOCACHE":     "/n/gocache",
}

// TestBuildEnvExportedAndStated proves BOTH runner-owned levers fire on a Work
// dispatch when BuildEnv is set: (1) the env is EXPORTED into the honeybee
// process (captured via the ExportEnv seam) so build/test subprocesses inherit
// CGO_ENABLED=0 + the redirected tmp/cache, and (2) the told-once mandated
// invocation is STATED in the injected first prompt — both sourced from the one
// map, so the agent never re-derives the build env.
func TestBuildEnvExportedAndStated(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	ctx := context.Background()
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(ctx, "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	var firstPrompt string
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("<!-- Beehive-Commits: none -->\n\ndoc\n"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= commits=none -->\ngo\n"), 0o644)
	}}}
	cl.sess.capture = &firstPrompt

	var exported map[string]string
	exportCalls := 0
	r := &Runner{
		Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		BuildEnv:  hostBuildEnv,
		ExportEnv: func(m map[string]string) { exportCalls++; exported = m },
	}
	if _, err := r.Run(ctx, sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// (1) Export: the child-process env carries the resolved keys.
	if exportCalls != 1 {
		t.Fatalf("ExportEnv called %d times, want exactly 1 (once at agent spawn)", exportCalls)
	}
	if !reflect.DeepEqual(exported, hostBuildEnv) {
		t.Fatalf("exported env = %#v, want %#v", exported, hostBuildEnv)
	}
	for _, k := range []string{"CGO_ENABLED", "GOTMPDIR", "TMPDIR", "GOCACHE"} {
		if exported[k] != hostBuildEnv[k] {
			t.Fatalf("exported[%q] = %q, want %q", k, exported[k], hostBuildEnv[k])
		}
	}

	// (2) Preamble: the told-once mandated invocation, with keys SORTED into a
	// deterministic prefix, plus the language-neutral example.
	wants := []string{
		"# Build/test environment (host-mandated",
		"CGO_ENABLED=0 GOCACHE=/n/gocache GOTMPDIR=/n/gotmp TMPDIR=/n/gotmp", // sorted, drift-free
		"<your build/test command>",
	}
	for _, w := range wants {
		if !contains(firstPrompt, w) {
			t.Fatalf("first prompt missing %q; got:\n%s", w, firstPrompt)
		}
	}
}

// TestBuildEnvExportInertWhenUnconfigured proves the feature ships inert: with no
// BuildEnv (the default), ExportEnv is never called and no build-env line appears
// in the prompt, so a normal host is byte-identical to the historical path.
func TestBuildEnvExportInertWhenUnconfigured(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	ctx := context.Background()
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(ctx, "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	var firstPrompt string
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("<!-- Beehive-Commits: none -->\n\ndoc\n"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= commits=none -->\ngo\n"), 0o644)
	}}}
	cl.sess.capture = &firstPrompt

	exportCalled := false
	r := &Runner{
		Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		// BuildEnv unset (nil). ExportEnv wired to prove it is NOT invoked.
		ExportEnv: func(map[string]string) { exportCalled = true },
	}
	if _, err := r.Run(ctx, sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if exportCalled {
		t.Fatal("ExportEnv was called with no BuildEnv configured (must be inert)")
	}
	if contains(firstPrompt, "# Build/test environment (host-mandated") {
		t.Fatalf("build-env preamble injected with no BuildEnv; got:\n%s", firstPrompt)
	}
	// The historical Context preamble is still present unchanged.
	if !contains(firstPrompt, "submodules/sm/docs/bee-T1-T1.md") {
		t.Fatalf("default preamble missing doc path; got:\n%s", firstPrompt)
	}
}

// TestBuildEnvPreamble unit-tests the rendered block: empty ⇒ "" (byte-identical
// default), configured ⇒ a sorted/deterministic KEY=VALUE prefix + the mandated
// invocation, and byte-stable across calls (Go's randomized map iteration must
// not leak into the line).
func TestBuildEnvPreamble(t *testing.T) {
	if got := buildEnvPreamble(nil); got != "" {
		t.Fatalf("buildEnvPreamble(nil) = %q, want \"\"", got)
	}
	if got := buildEnvPreamble(map[string]string{}); got != "" {
		t.Fatalf("buildEnvPreamble(empty) = %q, want \"\"", got)
	}

	env := map[string]string{"GOTMPDIR": "/n/gotmp", "CGO_ENABLED": "0", "GOCACHE": "/n/gocache", "TMPDIR": "/n/gotmp"}
	got := buildEnvPreamble(env)
	// Keys sorted lexically ⇒ deterministic prefix regardless of map order.
	wantPrefix := "CGO_ENABLED=0 GOCACHE=/n/gocache GOTMPDIR=/n/gotmp TMPDIR=/n/gotmp"
	if !contains(got, wantPrefix) {
		t.Fatalf("preamble missing sorted prefix %q; got:\n%s", wantPrefix, got)
	}
	if !contains(got, "<your build/test command>") {
		t.Fatalf("preamble missing mandated invocation example; got:\n%s", got)
	}

	// Byte-stable across repeated renders (defends against map-order drift, which
	// would silently churn the injected prompt turn to turn).
	for i := 0; i < 20; i++ {
		if again := buildEnvPreamble(env); again != got {
			t.Fatalf("buildEnvPreamble not byte-stable across calls:\n first: %q\n again: %q", got, again)
		}
	}
}
