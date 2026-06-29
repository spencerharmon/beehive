// Package config loads beehive runtime config from the shared dir (/etc/beehive),
// shared by cli, frontend, and honeybees. Holds the gpg keyring used for secrets
// and the agent (opencode) settings. Single host, config-managed, or bind-mount.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultDir is the shared config directory.
const DefaultDir = "/etc/beehive"

// Config is the parsed beehive config.
type Config struct {
	Dir         string `yaml:"-"`
	GPGHome     string `yaml:"gpg_home"`     // dir containing the keyring
	AgentCmd    string `yaml:"agent_cmd"`    // opencode binary
	TTLMinutes  int    `yaml:"ttl_minutes"`  // GC heartbeat TTL
	MaxTurns    int    `yaml:"max_turns"`    // per-honeybee turn cap
	RejectLimit int    `yaml:"reject_limit"` // rejections before NEEDS-HUMAN
}

// Defaults are applied when the config file omits fields.
func Defaults(dir string) Config {
	return Config{
		Dir:         dir,
		GPGHome:     filepath.Join(dir, "gnupg"),
		AgentCmd:    "opencode",
		TTLMinutes:  60,
		MaxTurns:    15,
		RejectLimit: 3,
	}
}

// Dir resolves the config dir from BEEHIVE_CONFIG_DIR or DefaultDir.
func resolveDir() string {
	if d := os.Getenv("BEEHIVE_CONFIG_DIR"); d != "" {
		return d
	}
	return DefaultDir
}

// Load reads <dir>/config.yaml, applying defaults for missing fields. A missing
// file is not an error: defaults are returned so single-host installs work bare.
func Load() (Config, error) {
	dir := resolveDir()
	c := Defaults(dir)
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	c.Dir = dir
	if c.GPGHome == "" {
		c.GPGHome = filepath.Join(dir, "gnupg")
	}
	if c.AgentCmd == "" {
		c.AgentCmd = "opencode"
	}
	if c.TTLMinutes == 0 {
		c.TTLMinutes = 60
	}
	if c.MaxTurns == 0 {
		c.MaxTurns = 15
	}
	if c.RejectLimit == 0 {
		c.RejectLimit = 3
	}
	return c, nil
}
