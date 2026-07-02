package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// RegistryFile is the basename of the host-scoped multi-repo registry. It lives
// in the same host config dir as config.yaml (BEEHIVE_CONFIG_DIR or DefaultDir)
// and declares the set of beehive repos a single daemon manages.
const RegistryFile = "repos.yaml"

// RepoEntry declares one managed beehive repo for the multi-beehive frontend.
//
// Each entry carries its OWN gpg keyring (GPGHome + GPGRecipient). The keyring is
// per-repo by construction so one registered repo can never decrypt another's
// SECRETS.yaml.gpg: this is the strict secret-isolation requirement, and Validate
// enforces that no two entries share a GPGHome or a GPGRecipient (no shared gpg
// home, no key reuse). The agent fields are optional per-repo overrides; an unset
// (zero) field falls through to that repo's own layered config (host -> in-repo
// global) when projected via Config.
type RepoEntry struct {
	Name         string  `yaml:"name"`          // unique handle used to select/switch repos
	Root         string  `yaml:"root"`          // beehive repo root path
	GPGHome      string  `yaml:"gpg_home"`      // per-repo keyring dir (required, unique)
	GPGRecipient string  `yaml:"gpg_recipient"` // per-repo recipient key (required, unique)
	AgentURL     string  `yaml:"agent_url"`     // optional per-repo agent base URL override
	Model        string  `yaml:"model"`         // optional per-repo provider/model override
	Temperature  float64 `yaml:"temperature"`   // optional per-repo sampling temperature override
	MaxTokens    int     `yaml:"max_tokens"`    // optional per-repo max output tokens override
}

// Registry is the set of beehive repos one daemon manages. An empty Registry is
// legacy single-repo mode: the daemon serves a single --repo root with the
// layered config's single keyring, exactly as before. A non-empty Registry must
// pass Validate, which guarantees strict per-repo keyring isolation.
type Registry struct {
	Repos []RepoEntry `yaml:"repos"`
}

// LoadRegistry reads the host-scoped registry from <dir>/repos.yaml where dir is
// BEEHIVE_CONFIG_DIR or DefaultDir. A missing file is not an error: it yields an
// empty (legacy single-repo) Registry so bare installs keep working. A present
// file is parsed and Validate'd, so a misconfigured registry that would let one
// repo read another's secrets surfaces as an error instead of loading silently.
func LoadRegistry() (Registry, error) {
	return LoadRegistryFrom(resolveDir())
}

// LoadRegistryFrom reads the registry from <dir>/repos.yaml. It is the testable
// core of LoadRegistry: a missing file yields an empty Registry (nil error); a
// present file is parsed and Validate'd.
func LoadRegistryFrom(dir string) (Registry, error) {
	var reg Registry
	b, err := os.ReadFile(filepath.Join(dir, RegistryFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Registry{}, nil
		}
		return Registry{}, fmt.Errorf("read registry %s: %w", filepath.Join(dir, RegistryFile), err)
	}
	if err := yaml.Unmarshal(b, &reg); err != nil {
		return Registry{}, fmt.Errorf("parse registry %s: %w", filepath.Join(dir, RegistryFile), err)
	}
	if err := reg.Validate(); err != nil {
		return Registry{}, err
	}
	return reg, nil
}

// ResolveRegistry returns the registry a daemon rooted at root should serve. If
// a host repos.yaml is present it is loaded and Validate'd (the multi-repo case;
// an invalid registry — e.g. a shared keyring — is a startup error). If absent,
// a one-entry registry is synthesized from the single resolved config at root
// (SingleEntryRegistry) so an unconfigured single-host install is unchanged.
//
// This is the single bridge the daemon entrypoint calls to learn what it serves;
// web routing and secrets wiring consume the resolved registry in later subtasks.
func ResolveRegistry(root string) (Registry, error) {
	reg, err := LoadRegistry()
	if err != nil {
		return Registry{}, err
	}
	if !reg.Empty() {
		return reg, nil
	}
	cfg, err := Resolve(root, "")
	if err != nil {
		return Registry{}, err
	}
	return SingleEntryRegistry(cfg, root), nil
}

// SingleEntryRegistry synthesizes the legacy single-repo registry: a one-entry
// Registry projecting an already-resolved single Config + repo root into the
// multi-repo shape. It is the inverse of RepoEntry.Config — for the synthesized
// entry e, e.Config(cfg) deep-equals cfg — so a bare single-host install (no
// repos.yaml) is served byte-identically to before, just through the registry
// abstraction.
//
// The entry carries the resolved config's OWN keyring (GPGHome + GPGRecipient,
// the "legacy keyring preserved" requirement) and its agent settings as per-repo
// values. The synthesized registry is intentionally NOT Validate'd: a single
// repo cannot violate cross-repo isolation, and a bare install legitimately has
// no GPGRecipient. Isolation is enforced only on a real, operator-authored
// repos.yaml (see LoadRegistry/Validate).
func SingleEntryRegistry(cfg Config, root string) Registry {
	return Registry{Repos: []RepoEntry{{
		Name:         legacyRepoName(root),
		Root:         root,
		GPGHome:      cfg.GPGHome,
		GPGRecipient: cfg.GPGRecipient,
		AgentURL:     cfg.AgentURL,
		Model:        cfg.Model,
		Temperature:  cfg.Temperature,
		MaxTokens:    cfg.MaxTokens,
	}}}
}

// legacyRepoName derives a stable handle for the synthesized single entry from
// the repo root's base name (a usable name for the future frontend switcher),
// falling back to "default" for roots with no usable base (".", "..", the path
// separator, or empty) so the handle is never blank.
func legacyRepoName(root string) string {
	switch name := filepath.Base(filepath.Clean(root)); name {
	case ".", "..", string(filepath.Separator), "":
		return "default"
	default:
		return name
	}
}

// Empty reports legacy single-repo mode (no registered repos).
func (r Registry) Empty() bool { return len(r.Repos) == 0 }

// Names returns the registered repo names sorted ascending (stable selection /
// listing order for the frontend switcher).
func (r Registry) Names() []string {
	out := make([]string, 0, len(r.Repos))
	for _, e := range r.Repos {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

// Repo returns the entry with the given name. ok is false if no repo is
// registered under that name (used to reject a switch to an unknown repo).
func (r Registry) Repo(name string) (RepoEntry, bool) {
	for _, e := range r.Repos {
		if e.Name == name {
			return e, true
		}
	}
	return RepoEntry{}, false
}

// Validate enforces the registry invariants. An empty Registry (legacy
// single-repo mode) is always valid. For a non-empty Registry every entry must
// set Name, Root, GPGHome and GPGRecipient, and across entries the Name, Root,
// GPGHome and GPGRecipient must each be unique. The GPGHome/GPGRecipient
// uniqueness is the security guarantee: two repos can never share a keyring or a
// key, so one can never decrypt the other's secrets.
func (r Registry) Validate() error {
	if r.Empty() {
		return nil
	}
	names := map[string]bool{}
	roots := map[string]bool{}
	homes := map[string]bool{}
	rcpts := map[string]bool{}
	for i, e := range r.Repos {
		switch {
		case e.Name == "":
			return fmt.Errorf("registry: repo[%d]: name is required", i)
		case e.Root == "":
			return fmt.Errorf("registry: repo %q: root is required", e.Name)
		case e.GPGHome == "":
			return fmt.Errorf("registry: repo %q: gpg_home is required (no shared keyring)", e.Name)
		case e.GPGRecipient == "":
			return fmt.Errorf("registry: repo %q: gpg_recipient is required (no key reuse)", e.Name)
		}
		root := filepath.Clean(e.Root)
		home := filepath.Clean(e.GPGHome)
		if names[e.Name] {
			return fmt.Errorf("registry: duplicate repo name %q", e.Name)
		}
		if roots[root] {
			return fmt.Errorf("registry: repo %q: duplicate root %q", e.Name, root)
		}
		if homes[home] {
			return fmt.Errorf("registry: repo %q: gpg_home %q is shared with another repo (secret isolation violated)", e.Name, home)
		}
		if rcpts[e.GPGRecipient] {
			return fmt.Errorf("registry: repo %q: gpg_recipient %q is reused by another repo (secret isolation violated)", e.Name, e.GPGRecipient)
		}
		names[e.Name] = true
		roots[root] = true
		homes[home] = true
		rcpts[e.GPGRecipient] = true
	}
	return nil
}

// Config projects this entry onto a base Config (the repo's own host -> in-repo
// global layered config, typically Resolve(entry.Root, "")). The per-repo keyring
// (GPGHome + GPGRecipient) is ALWAYS applied so the returned config is scoped to
// this repo's secrets and can never inherit a shared keyring; the agent overrides
// are applied only when set (zero == unset), falling through to base otherwise.
// The returned config is what a caller uses to build this repo's secrets.Store
// and agent client, keeping repos isolated.
func (e RepoEntry) Config(base Config) Config {
	out := base
	out.GPGHome = e.GPGHome
	out.GPGRecipient = e.GPGRecipient
	if e.AgentURL != "" {
		out.AgentURL = e.AgentURL
	}
	if e.Model != "" {
		out.Model = e.Model
	}
	if e.Temperature != 0 {
		out.Temperature = e.Temperature
	}
	if e.MaxTokens != 0 {
		out.MaxTokens = e.MaxTokens
	}
	return out
}
