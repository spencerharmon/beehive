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
	// DeepEqual (not ==): Config now carries a map field (ModelByKind), which is
	// not comparable with ==. Both sides have a nil map here, so this still asserts
	// exact equality with the built-in Defaults.
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

// TestResolveModelByKindLayering proves the per-kind model map merges KEY-BY-KEY
// across layers (a more specific layer retiers one kind without dropping kinds a
// lower layer set), unlike the scalar "whole value wins" fields, and that a
// per-kind route survives alongside the untiered strong Model.
func TestResolveModelByKindLayering(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Host sets the strong default model and tiers two trivial kinds cheaply.
	write(t, filepath.Join(hostDir, "config.yaml"), `
model: strong/model
model_by_kind:
  reconcile: cheap/host
  bootstrap: cheap/host
`)
	// Global retiers reconcile only; bootstrap must survive from the host layer.
	write(t, filepath.Join(root, "config.yaml"), `
model_by_kind:
  reconcile: cheap/global
`)
	// Submodule adds a new kind (review) without disturbing the inherited ones.
	write(t, filepath.Join(root, "submodules", "x", "config.yaml"), `
model_by_kind:
  review: cheap/sub
`)

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "strong/model" {
		t.Errorf("Model = %q, want strong/model (untiered default kept)", c.Model)
	}
	want := map[string]string{
		"reconcile": "cheap/global", // global retiered over host
		"bootstrap": "cheap/host",   // survives from host (key-by-key merge)
		"review":    "cheap/sub",    // added by the submodule layer
	}
	if !reflect.DeepEqual(c.ModelByKind, want) {
		t.Fatalf("ModelByKind = %v, want %v (must merge per-key, not replace)", c.ModelByKind, want)
	}
}

// TestResolveModelByKindNoAlias proves merge allocates a fresh map so a resolved
// config never aliases (and thus can never mutate) a lower layer's map.
func TestResolveModelByKindNoAlias(t *testing.T) {
	base := Config{ModelByKind: map[string]string{"work": "strong"}}
	over := Config{ModelByKind: map[string]string{"reconcile": "cheap"}}
	out := merge(base, over)
	out.ModelByKind["reconcile"] = "mutated"
	if over.ModelByKind["reconcile"] != "cheap" {
		t.Fatalf("merge aliased the over map: %v", over.ModelByKind)
	}
	if _, ok := base.ModelByKind["reconcile"]; ok {
		t.Fatalf("merge mutated the base map: %v", base.ModelByKind)
	}
}

// TestResolveMaxIdleTurns confirms max_idle_turns layers like the other scalar
// caps (most-specific non-zero wins) and defaults to 0 (idle detection disabled)
// on an unconfigured host.
func TestResolveMaxIdleTurns(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Default: no file sets it -> 0 (disabled), leaving a bare host unchanged.
	if c, err := Resolve(root, "x"); err != nil {
		t.Fatal(err)
	} else if c.MaxIdleTurns != 0 {
		t.Fatalf("MaxIdleTurns default = %d, want 0 (idle detection off unless configured)", c.MaxIdleTurns)
	}

	write(t, filepath.Join(hostDir, "config.yaml"), "max_idle_turns: 3\n")
	write(t, filepath.Join(root, "submodules", "x", "config.yaml"), "max_idle_turns: 5\n")
	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxIdleTurns != 5 {
		t.Fatalf("MaxIdleTurns = %d, want 5 (submodule overrides host)", c.MaxIdleTurns)
	}
}
