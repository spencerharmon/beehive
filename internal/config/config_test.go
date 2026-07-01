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

// TestResolveBuildEnvLayering checks the build_env map layers per key: a key set
// in a more specific layer overrides the same key from a lower layer, while keys
// only a lower layer sets fall through. This is the map analogue of the scalar
// precedence in TestResolveLayering.
func TestResolveBuildEnvLayering(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Host: the base toolchain env. CGO_ENABLED falls through untouched;
	// GOCACHE and GOTMPDIR get overridden by more specific layers.
	write(t, filepath.Join(hostDir, "config.yaml"), `
build_env:
  CGO_ENABLED: "0"
  GOCACHE: /host/cache
  GOTMPDIR: /host/tmp
`)
	// Global: overrides GOCACHE, adds GOFLAGS.
	write(t, filepath.Join(root, "config.yaml"), `
build_env:
  GOCACHE: /global/cache
  GOFLAGS: -mod=mod
`)
	// Submodule: overrides GOTMPDIR, adds TMPDIR.
	write(t, filepath.Join(root, "submodules", "x", "config.yaml"), `
build_env:
  GOTMPDIR: /sub/tmp
  TMPDIR: /sub/tmp
`)

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"CGO_ENABLED": "0",             // host, falls through every layer
		"GOCACHE":     "/global/cache", // global overrides host
		"GOTMPDIR":    "/sub/tmp",      // submodule overrides host
		"GOFLAGS":     "-mod=mod",      // only global sets it
		"TMPDIR":      "/sub/tmp",      // only submodule sets it
	}
	if !reflect.DeepEqual(c.BuildEnv, want) {
		t.Fatalf("BuildEnv = %#v, want %#v", c.BuildEnv, want)
	}
}

// TestResolveBuildEnvInertWhenUnset confirms that config files present but not
// setting build_env leave BuildEnv nil (not an empty map): the feature is a true
// no-op on a host that never opts in, so a normal host is byte-unaffected.
func TestResolveBuildEnvInertWhenUnset(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	write(t, filepath.Join(hostDir, "config.yaml"), "model: host/model\n")
	write(t, filepath.Join(root, "config.yaml"), "max_turns: 9\n")

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	if c.BuildEnv != nil {
		t.Fatalf("BuildEnv = %#v, want nil (inert when unset)", c.BuildEnv)
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
