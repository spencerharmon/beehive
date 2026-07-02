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

// TestResolveBuildEnvLayering checks the build_env map merges per-KEY across
// layers (most-specific key wins) and that unset keys fall through — the runner
// exports these into the agent so a honeybee never re-derives CGO_ENABLED=0 +
// root-fs GOTMPDIR/GOCACHE. It also confirms the bare/default case leaves
// BuildEnv nil (inert; a normal host is unaffected).
func TestResolveBuildEnvLayering(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Host layer sets the two host-wide keys (CGO_ENABLED kept; GOCACHE overridden).
	write(t, filepath.Join(hostDir, "config.yaml"), `
build_env:
  CGO_ENABLED: "0"
  GOCACHE: /host/cache
`)
	// In-repo global adds GOFLAGS (kept) and redirects the tmp dirs.
	write(t, filepath.Join(root, "config.yaml"), `
build_env:
  GOFLAGS: -mod=mod
  GOTMPDIR: /global/tmp
  TMPDIR: /global/tmp
`)
	// Per-submodule overrides only GOCACHE; every other key falls through.
	write(t, filepath.Join(root, "submodules", "x", "config.yaml"), `
build_env:
  GOCACHE: /sub/cache
`)

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"CGO_ENABLED": "0",           // host, falls all the way through
		"GOFLAGS":     "-mod=mod",    // only global sets it
		"GOTMPDIR":    "/global/tmp", // global sets it, submodule leaves it
		"TMPDIR":      "/global/tmp",
		"GOCACHE":     "/sub/cache", // submodule wins over host
	}
	if !reflect.DeepEqual(c.BuildEnv, want) {
		t.Fatalf("BuildEnv = %#v, want %#v", c.BuildEnv, want)
	}

	// A layer that sets no build_env must not clobber a lower layer's map.
	c2, err := Resolve(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.BuildEnv["GOCACHE"] != "/host/cache" {
		t.Errorf("no-submodule GOCACHE = %q, want /host/cache (global has no build_env, host shows through)", c2.BuildEnv["GOCACHE"])
	}
	if c2.BuildEnv["GOFLAGS"] != "-mod=mod" {
		t.Errorf("no-submodule GOFLAGS = %q, want -mod=mod (global key merged per-key, not wholesale)", c2.BuildEnv["GOFLAGS"])
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
	// Inert build env: a normal (unconfigured) host carries no build_env, so the
	// runner exports nothing and the injected preamble is byte-identical.
	if c.BuildEnv != nil {
		t.Errorf("bare BuildEnv = %#v, want nil (inert default)", c.BuildEnv)
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
