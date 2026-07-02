// Package config loads beehive runtime config, shared by cli, frontend, and
// honeybees. Holds the gpg keyring used for secrets and the agent (opencode)
// settings. Config is layered, most-specific wins (see Resolve): built-in
// Defaults -> host file (/etc/beehive) -> in-repo global -> per-submodule
// override. Single host, config-managed, or bind-mount.
//
// # Multi-repo registry
//
// An optional host file <dir>/repos.yaml (RegistryFile) declares the set of
// beehive repos one frontend daemon manages, each with its OWN gpg keyring for
// strict secret isolation. Shape:
//
//	repos:
//	  - name: alpha                      # unique handle (frontend switcher)
//	    root: /srv/alpha                 # beehive repo root
//	    gpg_home: /srv/alpha/gnupg       # per-repo keyring dir (unique)
//	    gpg_recipient: alpha@example.com # per-repo recipient key (unique)
//	    model: anthropic/claude          # optional per-repo agent overrides
//	  - name: beta
//	    root: /srv/beta
//	    gpg_home: /srv/beta/gnupg
//	    gpg_recipient: beta@example.com
//
// Single -> multi migration: with no repos.yaml the daemon synthesizes a
// one-entry registry from the single --repo root + resolved keyring
// (SingleEntryRegistry, via ResolveRegistry), so an unconfigured host stays
// byte-identical to the legacy single-repo path. To go multi-repo, write
// repos.yaml listing each repo with a DISTINCT gpg_home and gpg_recipient —
// Registry.Validate rejects a shared keyring or reused key so no secret can
// ever cross repos.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultDir is the shared config directory.
const DefaultDir = "/etc/beehive"

// fileName is the basename of every config layer (host dir, repo root, submodule dir).
const fileName = "config.yaml"

// Config is the parsed beehive config. Zero-valued fields are treated as "unset"
// when layering (see merge): a layer only overrides the fields it actually sets.
type Config struct {
	Dir          string  `yaml:"-"`
	GPGHome      string  `yaml:"gpg_home"`      // dir containing the keyring
	GPGRecipient string  `yaml:"gpg_recipient"` // recipient for SECRETS.yaml.gpg
	AgentCmd     string  `yaml:"agent_cmd"`     // opencode binary
	AgentURL     string  `yaml:"agent_url"`     // opencode server base URL
	Model        string  `yaml:"model"`         // provider/model for opencode
	Temperature  float64 `yaml:"temperature"`   // sampling temperature for the agent model
	MaxTokens    int     `yaml:"max_tokens"`    // max output tokens per turn (0 = backend default)
	TTLMinutes   int     `yaml:"ttl_minutes"`   // GC heartbeat TTL
	MaxTurns     int     `yaml:"max_turns"`     // per-honeybee turn cap
	RejectLimit  int     `yaml:"reject_limit"`  // rejections before NEEDS-HUMAN
	// TurnTimeoutMinutes bounds a single agent turn (one opencode call). A stalled
	// session is canceled at this cap so the honeybee abandons the task for GC
	// instead of wedging until the systemd RuntimeMaxSec backstop. 0 = no per-turn
	// cap (the whole-run WallCap/TTL still applies between turns).
	TurnTimeoutMinutes int `yaml:"turn_timeout_minutes"`
	// BuildEnv is the host build/test environment the runner exports into the agent
	// so a honeybee never re-derives it: the mandated static invocation (e.g.
	// CGO_ENABLED=0 plus a root-fs GOTMPDIR/TMPDIR/GOCACHE) that LOCALS.md
	// documents. The runner exports these into the agent process (so every tool
	// shell inherits them) and states them once in the injected preamble. Layered
	// per-KEY, most-specific wins: a nil/empty map is unset and falls through, and
	// a more specific layer overrides only the individual keys it sets (see merge).
	// Empty by default (inert) so an unconfigured host is unaffected.
	BuildEnv map[string]string `yaml:"build_env"`
}

// Defaults are the lowest layer, applied when no file sets a field.
func Defaults(dir string) Config {
	return Config{
		Dir:                dir,
		GPGHome:            filepath.Join(dir, "gnupg"),
		AgentCmd:           "opencode",
		AgentURL:           "http://127.0.0.1:4096",
		TTLMinutes:         60,
		MaxTurns:           15,
		RejectLimit:        3,
		TurnTimeoutMinutes: 60,
	}
}

// resolveDir resolves the host config dir from BEEHIVE_CONFIG_DIR or DefaultDir.
func resolveDir() string {
	if d := os.Getenv("BEEHIVE_CONFIG_DIR"); d != "" {
		return d
	}
	return DefaultDir
}

// loadFile reads one config layer from path. A missing file is not an error: it
// is a skipped layer (ok=false), so lower layers show through and bare installs
// work. A present-but-unreadable or malformed file is a real error.
func loadFile(path string) (layer Config, ok bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &layer); err != nil {
		return Config{}, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	return layer, true, nil
}

// merge overlays over onto base field-wise: a field set in over (non-zero) wins;
// an unset (zero) field falls through to base. This is the "most-specific wins,
// zero == unset" rule shared by every layer. Dir is metadata (yaml:"-") and is
// never carried from a file layer. BuildEnv is the one map field: it merges
// per-KEY (a fresh map is built so neither input is mutated) so a more specific
// layer overrides only the individual keys it sets and unset keys fall through —
// an empty/nil over.BuildEnv leaves base's map untouched.
func merge(base, over Config) Config {
	out := base
	if over.GPGHome != "" {
		out.GPGHome = over.GPGHome
	}
	if over.GPGRecipient != "" {
		out.GPGRecipient = over.GPGRecipient
	}
	if over.AgentCmd != "" {
		out.AgentCmd = over.AgentCmd
	}
	if over.AgentURL != "" {
		out.AgentURL = over.AgentURL
	}
	if over.Model != "" {
		out.Model = over.Model
	}
	if over.Temperature != 0 {
		out.Temperature = over.Temperature
	}
	if over.MaxTokens != 0 {
		out.MaxTokens = over.MaxTokens
	}
	if over.TTLMinutes != 0 {
		out.TTLMinutes = over.TTLMinutes
	}
	if over.MaxTurns != 0 {
		out.MaxTurns = over.MaxTurns
	}
	if over.RejectLimit != 0 {
		out.RejectLimit = over.RejectLimit
	}
	if over.TurnTimeoutMinutes != 0 {
		out.TurnTimeoutMinutes = over.TurnTimeoutMinutes
	}
	if len(over.BuildEnv) > 0 {
		// Per-key overlay into a FRESH map so neither base's nor over's map is
		// mutated (out aliases base's map header until we replace it): copy base
		// first, then let over's keys win. Unset keys stay from base (fall through).
		m := make(map[string]string, len(base.BuildEnv)+len(over.BuildEnv))
		for k, v := range base.BuildEnv {
			m[k] = v
		}
		for k, v := range over.BuildEnv {
			m[k] = v
		}
		out.BuildEnv = m
	}
	return out
}

// layerPaths returns the ordered config files (lowest precedence first, excluding
// built-in Defaults) for a submodule. submodule may be "" to resolve only the
// host + in-repo-global scopes.
func layerPaths(dir, root, submodule string) []string {
	paths := []string{
		filepath.Join(dir, fileName),  // host file: /etc/beehive/config.yaml
		filepath.Join(root, fileName), // in-repo global defaults
	}
	if submodule != "" {
		// per-submodule override (beehive layer, alongside ROI.md/PLAN.md)
		paths = append(paths, filepath.Join(root, "submodules", submodule, fileName))
	}
	return paths
}

// resolve merges the built-in Defaults with each present layer in order, most-
// specific wins. dir is the host config dir.
func resolve(dir, root, submodule string) (Config, error) {
	c := Defaults(dir)
	for _, p := range layerPaths(dir, root, submodule) {
		layer, ok, err := loadFile(p)
		if err != nil {
			return c, err
		}
		if ok {
			c = merge(c, layer)
		}
	}
	c.Dir = dir
	return c, nil
}

// Resolve computes the effective config for a submodule by merging four layers
// in increasing specificity (most-specific wins):
//
//  1. built-in Defaults()
//  2. host file:      <dir>/config.yaml           (dir = BEEHIVE_CONFIG_DIR or /etc/beehive)
//  3. in-repo global: <root>/config.yaml          (committed; overrides host)
//  4. per-submodule:  <root>/submodules/<sm>/config.yaml (committed; overrides global)
//
// Each higher layer overrides only the fields it sets (zero-value == unset). A
// missing file is a skipped layer, so a bare single-host install (no files)
// resolves to Defaults. submodule may be "" to resolve only the global scopes.
// Callers resolve the effective config per submodule at runtime.
func Resolve(root, submodule string) (Config, error) {
	return resolve(resolveDir(), root, submodule)
}

// Load reads only the host layer (<dir>/config.yaml) over Defaults. Retained for
// host-scoped callers (e.g. secrets, the GPG keyring) and bare single-host
// installs; a missing file returns Defaults so those installs work unconfigured.
// Prefer Resolve for agent settings that vary per submodule.
func Load() (Config, error) {
	dir := resolveDir()
	c := Defaults(dir)
	layer, ok, err := loadFile(filepath.Join(dir, fileName))
	if err != nil {
		return c, err
	}
	if ok {
		c = merge(c, layer)
	}
	c.Dir = dir
	return c, nil
}
