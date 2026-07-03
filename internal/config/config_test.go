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

// TestResolveModelsLayering checks the per-kind model map (honeybee-model-routing):
// a kind set in a more specific layer wins, layers merge key-by-key (an unset kind
// falls through rather than the whole map being replaced), and a kind with no entry
// anywhere falls through to the single Model — so code Work keeps the strong model
// while trivial kinds route cheap.
func TestResolveModelsLayering(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Host: the fallback Model (strong) + cheap review/reconcile.
	write(t, filepath.Join(hostDir, "config.yaml"), `
model: strong/model
models:
  review: cheap/host
  reconcile: cheap/host
`)
	// Global: override reconcile, add arbitrate (review untouched -> host shows through).
	write(t, filepath.Join(root, "config.yaml"), `
models:
  reconcile: cheap/global
  arbitrate: cheap/global
`)
	// Submodule (most specific): override review only.
	write(t, filepath.Join(root, "submodules", "x", "config.yaml"), `
models:
  review: cheap/sub
`)

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	// submodule wins for review (set in the most specific layer)
	if got := c.ModelFor("review"); got != "cheap/sub" {
		t.Errorf("ModelFor(review) = %q, want cheap/sub (submodule wins)", got)
	}
	// global wins for reconcile (over host); merged key-by-key so review survives
	if got := c.ModelFor("reconcile"); got != "cheap/global" {
		t.Errorf("ModelFor(reconcile) = %q, want cheap/global (global over host)", got)
	}
	// arbitrate only set at global (host/submodule unset for it)
	if got := c.ModelFor("arbitrate"); got != "cheap/global" {
		t.Errorf("ModelFor(arbitrate) = %q, want cheap/global", got)
	}
	// work has no per-kind entry: falls through to the strong fallback Model
	if got := c.ModelFor("work"); got != "strong/model" {
		t.Errorf("ModelFor(work) = %q, want strong/model (fall through to Model)", got)
	}
}

// TestModelForInert confirms the inert defaults: an empty config routes nothing
// ("" for every kind — the runner keeps the client's own model), and a host that
// set only a single Model resolves every kind to it (single-model host unchanged).
func TestModelForInert(t *testing.T) {
	var empty Config
	if got := empty.ModelFor("work"); got != "" {
		t.Errorf("empty ModelFor(work) = %q, want \"\" (inert: no routing configured)", got)
	}
	solo := Config{Model: "solo/model"}
	for _, kind := range []string{"work", "reconcile", "review", "arbitrate", "bootstrap"} {
		if got := solo.ModelFor(kind); got != "solo/model" {
			t.Errorf("single-model ModelFor(%s) = %q, want solo/model", kind, got)
		}
	}
}
