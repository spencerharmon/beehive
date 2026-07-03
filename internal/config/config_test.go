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
	// Config carries a map field (Models), so it is no longer == comparable; a
	// bare resolve must still deep-equal the built-in Defaults (Models nil,
	// MaxIdleTurns 0 — routing inert on an unconfigured host).
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

// TestModelRoutingLayering proves per-kind model routes layer PER KEY across
// scopes: a more specific layer overrides only the kinds it names and inherits
// the rest, and MaxIdleTurns follows the same most-specific-wins rule. This is
// the layered-config knob honeybee-model-routing adds on top of config-layered.
func TestModelRoutingLayering(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Host: a cheap model for the trivial kinds + the strong default, and an
	// idle cap the submodule will tighten.
	write(t, filepath.Join(hostDir, "config.yaml"), `
model: strong/default
max_idle_turns: 5
models:
  reconcile: cheap/mini
  bootstrap: cheap/mini
  work: strong/default
`)
	// In-repo global: raise Work to a bigger model (kept unless submodule overrides).
	write(t, filepath.Join(root, "config.yaml"), `
models:
  work: strong/big
`)
	// Per-submodule (most specific): override Work again and add a review route,
	// and tighten the idle cap. reconcile/bootstrap fall through from host.
	write(t, filepath.Join(root, "submodules", "x", "config.yaml"), `
max_idle_turns: 2
models:
  work: sub/huge
  review: cheap/mini
`)

	c, err := Resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	// Per-key precedence: submodule > global > host, unset keys fall through.
	if got := c.Models["work"]; got != "sub/huge" {
		t.Errorf("Models[work] = %q, want sub/huge (submodule wins)", got)
	}
	if got := c.Models["review"]; got != "cheap/mini" {
		t.Errorf("Models[review] = %q, want cheap/mini (only submodule sets it)", got)
	}
	if got := c.Models["reconcile"]; got != "cheap/mini" {
		t.Errorf("Models[reconcile] = %q, want cheap/mini (host falls through)", got)
	}
	if got := c.Models["bootstrap"]; got != "cheap/mini" {
		t.Errorf("Models[bootstrap] = %q, want cheap/mini (host falls through)", got)
	}
	if c.Model != "strong/default" {
		t.Errorf("Model = %q, want strong/default (default not overridden)", c.Model)
	}
	if c.MaxIdleTurns != 2 {
		t.Errorf("MaxIdleTurns = %d, want 2 (submodule tightens host's 5)", c.MaxIdleTurns)
	}

	// The host layer's own Models map must be untouched by the merge (clone-on-
	// write): resolving a DIFFERENT submodule with no override still sees the host
	// route for work, not the leaked global/submodule value.
	c2, err := Resolve(root, "")
	if err != nil {
		t.Fatal(err)
	}
	// submodule="" resolves host+global, so work is global/big here — proving the
	// host map was not mutated to sub/huge by the "x" resolve above.
	if got := c2.Models["work"]; got != "strong/big" {
		t.Errorf("Models[work] = %q, want strong/big (global over host; no cross-resolve mutation)", got)
	}
	if got := c2.Models["reconcile"]; got != "cheap/mini" {
		t.Errorf("Models[reconcile] = %q, want cheap/mini (host)", got)
	}
}

// TestModelForKindFallThrough proves ModelForKind returns the per-kind override
// when set and falls back to the default Model otherwise — the runtime dispatch
// the runner uses to pick a session's model.
func TestModelForKindFallThrough(t *testing.T) {
	c := Config{Model: "strong/default", Models: map[string]string{
		"reconcile": "cheap/mini",
		"work":      "", // present-but-empty must not shadow the default
	}}
	if got := c.ModelForKind("reconcile"); got != "cheap/mini" {
		t.Errorf("ModelForKind(reconcile) = %q, want cheap/mini", got)
	}
	if got := c.ModelForKind("work"); got != "strong/default" {
		t.Errorf("ModelForKind(work) = %q, want strong/default (empty route falls through)", got)
	}
	if got := c.ModelForKind("review"); got != "strong/default" {
		t.Errorf("ModelForKind(review) = %q, want strong/default (no route falls through)", got)
	}
	// A nil Models map is safe and always yields the default.
	bare := Config{Model: "only/model"}
	if got := bare.ModelForKind("work"); got != "only/model" {
		t.Errorf("ModelForKind(work) on nil Models = %q, want only/model", got)
	}
}
