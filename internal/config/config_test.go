package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaults(t *testing.T) {
	t.Setenv("BEEHIVE_CONFIG_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentCmd != "opencode" || c.TTLMinutes != 60 || c.MaxTurns != 15 || c.RejectLimit != 3 || c.TurnTimeoutMinutes != 60 {
		t.Fatalf("bad defaults: %+v", c)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveLayering checks per-scope precedence field-by-field: a field set in
// a more specific layer wins (submodule > global > host > default), and an unset
// field falls through to the next-lower layer.
func TestResolveLayering(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Host layer: sets agent_url (kept), and model/ttl/temperature/max_tokens
	// that more specific layers override.
	write(t, filepath.Join(hostDir, "config.yaml"), `
agent_url: http://host:1
model: host/model
ttl_minutes: 11
temperature: 0.1
max_tokens: 100
`)
	// In-repo global: overrides model+ttl, sets max_turns (kept), bumps temperature.
	write(t, filepath.Join(root, "config.yaml"), `
model: global/model
ttl_minutes: 22
max_turns: 7
temperature: 0.2
`)
	// Per-submodule: most specific; overrides model, temperature, max_tokens.
	write(t, filepath.Join(root, "submodules", "x", "config.yaml"), `
model: sub/model
temperature: 0.3
max_tokens: 500
`)

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}

	// submodule wins (set in the most specific layer)
	if c.Model != "sub/model" {
		t.Errorf("Model = %q, want sub/model (submodule wins)", c.Model)
	}
	if c.Temperature != 0.3 {
		t.Errorf("Temperature = %v, want 0.3 (submodule wins)", c.Temperature)
	}
	if c.MaxTokens != 500 {
		t.Errorf("MaxTokens = %d, want 500 (submodule wins)", c.MaxTokens)
	}
	// global wins (submodule unset, global set)
	if c.TTLMinutes != 22 {
		t.Errorf("TTLMinutes = %d, want 22 (global wins over host)", c.TTLMinutes)
	}
	if c.MaxTurns != 7 {
		t.Errorf("MaxTurns = %d, want 7 (only global sets it)", c.MaxTurns)
	}
	// host wins (global+submodule unset, host set)
	if c.AgentURL != "http://host:1" {
		t.Errorf("AgentURL = %q, want http://host:1 (only host sets it)", c.AgentURL)
	}
	// default falls through (no layer sets it)
	if c.AgentCmd != "opencode" {
		t.Errorf("AgentCmd = %q, want opencode (default falls through)", c.AgentCmd)
	}
	if c.RejectLimit != 3 {
		t.Errorf("RejectLimit = %d, want 3 (default falls through)", c.RejectLimit)
	}
	if c.TurnTimeoutMinutes != 60 {
		t.Errorf("TurnTimeoutMinutes = %d, want 60 (default falls through)", c.TurnTimeoutMinutes)
	}
}

// TestResolveModelByKindLayering checks the per-kind model routing knob layers
// PER KEY with the same most-specific-wins precedence + fall-through as scalar
// fields, and that MaxIdleTurns layers like the other int caps. A trivial kind
// set only in a lower layer is inherited; a kind re-set in a more specific layer
// wins; a kind set nowhere falls through to the strong Model at dispatch (not
// stored in the map).
func TestResolveModelByKindLayering(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Host: strong default model + cheap routes for reconcile and review, plus an
	// idle cap that a more specific layer overrides.
	write(t, filepath.Join(hostDir, "config.yaml"), `
model: strong/model
max_idle_turns: 9
model_by_kind:
  reconcile: cheap/host
  review: cheap/host
`)
	// Global: re-routes reconcile (wins over host) and adds bootstrap; leaves
	// review to fall through from host.
	write(t, filepath.Join(root, "config.yaml"), `
model_by_kind:
  reconcile: cheap/global
  bootstrap: cheap/global
`)
	// Submodule (most specific): re-routes bootstrap and adds arbitrate; tightens
	// the idle cap.
	write(t, filepath.Join(root, "submodules", "x", "config.yaml"), `
max_idle_turns: 3
model_by_kind:
  bootstrap: cheap/sub
  arbitrate: cheap/sub
`)

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "strong/model" {
		t.Errorf("Model = %q, want strong/model (host, never routed away)", c.Model)
	}
	// per-key precedence: submodule > global > host, unset keys inherited.
	want := map[string]string{
		"reconcile": "cheap/global", // global overrode host
		"review":    "cheap/host",   // only host set it; inherited through
		"bootstrap": "cheap/sub",    // submodule overrode global
		"arbitrate": "cheap/sub",    // only submodule set it
	}
	if !reflect.DeepEqual(c.ModelByKind, want) {
		t.Errorf("ModelByKind = %v, want %v", c.ModelByKind, want)
	}
	// "work" is set nowhere -> absent from the map -> falls through to Model at dispatch.
	if _, ok := c.ModelByKind["work"]; ok {
		t.Errorf("ModelByKind must not contain 'work' (unset kind falls through to Model)")
	}
	if c.MaxIdleTurns != 3 {
		t.Errorf("MaxIdleTurns = %d, want 3 (submodule wins over host)", c.MaxIdleTurns)
	}
}

// TestMergeDoesNotMutateLowerLayer confirms the per-key ModelByKind overlay is
// copy-on-write: overlaying a more specific layer must not mutate the map handed
// in as the base (maps are references; a naive overlay would corrupt a shared
// lower layer for later resolves).
func TestMergeDoesNotMutateLowerLayer(t *testing.T) {
	base := Config{ModelByKind: map[string]string{"reconcile": "cheap/base"}}
	over := Config{ModelByKind: map[string]string{"reconcile": "cheap/over", "review": "cheap/over"}}
	out := merge(base, over)
	if base.ModelByKind["reconcile"] != "cheap/base" {
		t.Errorf("merge mutated base ModelByKind: reconcile = %q, want cheap/base", base.ModelByKind["reconcile"])
	}
	if _, ok := base.ModelByKind["review"]; ok {
		t.Errorf("merge leaked 'review' into base ModelByKind")
	}
	if out.ModelByKind["reconcile"] != "cheap/over" || out.ModelByKind["review"] != "cheap/over" {
		t.Errorf("merge output wrong: %v", out.ModelByKind)
	}
}

// TestResolveNoSubmoduleLayer confirms submodule="" resolves only host+global,
// and that an absent submodule file is a skipped layer (global stays most
// specific) rather than an error.
func TestResolveNoSubmoduleLayer(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	write(t, filepath.Join(hostDir, "config.yaml"), "model: host/model\nagent_url: http://host:1\n")
	write(t, filepath.Join(root, "config.yaml"), "model: global/model\n")

	c, err := Resolve(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "global/model" {
		t.Errorf("Model = %q, want global/model (global wins, no submodule)", c.Model)
	}
	if c.AgentURL != "http://host:1" {
		t.Errorf("AgentURL = %q, want http://host:1 (host falls through)", c.AgentURL)
	}

	// A submodule with no override file falls through to global.
	c2, err := Resolve(root, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Model != "global/model" {
		t.Errorf("Model = %q, want global/model (missing submodule file is a no-op)", c2.Model)
	}
}

// TestResolveBareInstall confirms a host with no config files at all resolves to
// the built-in Defaults (single-host install works unconfigured).
func TestResolveBareInstall(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults(hostDir)
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("bare Resolve = %+v, want Defaults %+v", c, want)
	}
}

// TestResolveMalformedErrors confirms a present-but-malformed layer surfaces an
// error instead of being silently swallowed.
func TestResolveMalformedErrors(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)
	write(t, filepath.Join(root, "config.yaml"), "model: : : not yaml\n\t- broken")

	if _, err := Resolve(root, ""); err == nil {
		t.Fatal("expected error for malformed config layer, got nil")
	}
}
