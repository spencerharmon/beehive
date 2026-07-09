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

// FileName is the basename of every config layer (host dir, repo root, submodule
// dir). Exported so callers that only need to detect a config's presence (e.g.
// the frontend's bootstrap-state check) resolve the same path this package uses.
const FileName = "config.yaml"

// Config is the parsed beehive config. Zero-valued fields are treated as "unset"
// when layering (see merge): a layer only overrides the fields it actually sets.
type Config struct {
	Dir          string `yaml:"-"`
	GPGHome      string `yaml:"gpg_home"`      // dir containing the keyring
	GPGRecipient string `yaml:"gpg_recipient"` // recipient for SECRETS.yaml.gpg
	AgentCmd     string `yaml:"agent_cmd"`     // opencode binary
	AgentURL     string `yaml:"agent_url"`     // opencode server base URL
	Model        string `yaml:"model"`         // provider/model for opencode (the fallback for every kind)
	// Models routes the agent model per task kind ("work", "reconcile", "review",
	// "arbitrate", "bootstrap"), so a near-deterministic kind can run on a cheap
	// model while real code Work stays on the strong one (ROI: cut tokens per
	// honeybee). A kind absent here falls through to Model (see ModelFor). Merged
	// key-by-key across layers, so a submodule can override a single kind. Unset =
	// no routing: every kind resolves to Model, byte-identical to the single-model
	// path.
	Models       map[string]string `yaml:"models"`
	Temperature  float64           `yaml:"temperature"`   // sampling temperature for the agent model
	MaxTokens    int               `yaml:"max_tokens"`    // max output tokens per turn (0 = backend default)
	TTLMinutes   int               `yaml:"ttl_minutes"`   // GC heartbeat TTL
	MaxTurns     int               `yaml:"max_turns"`     // per-honeybee turn cap
	MergeRetries int               `yaml:"merge_retries"` // publish conflict-resolution attempts before deferring (default 8)
	RejectLimit  int               `yaml:"reject_limit"`  // rejections before NEEDS-HUMAN
	// StallTurns bounds idle churn: if a Work pass produces an identical code-
	// worktree fingerprint for this many consecutive turns without reaching
	// completion, the runner abandons it for GC instead of burning the whole
	// turn/wall budget on a provably stuck session. 0 = off (the default), so a
	// host that has not opted in behaves exactly as before.
	StallTurns int `yaml:"stall_turns"`
	// TurnTimeoutMinutes is the ABSOLUTE per-turn ceiling (one opencode call): a
	// hard wall-clock backstop so a turn can never run past it regardless of
	// progress, short of the systemd RuntimeMaxSec. Day-to-day stall detection is
	// the finer-grained TurnIdleTimeoutMinutes progress watchdog; this ceiling only
	// bounds a pathological turn that keeps trickling progress forever. 0 = no
	// absolute cap (the whole-run WallCap/TTL still applies between turns).
	TurnTimeoutMinutes int `yaml:"turn_timeout_minutes"`

	// TurnIdleTimeoutMinutes is the per-turn PROGRESS watchdog: a turn that produces
	// no new transcript activity (no new tool call, streamed tool output, or text)
	// for this long is abandoned for GC as a genuine stall. Unlike
	// TurnTimeoutMinutes — an absolute wall-clock ceiling that kills a turn even
	// while it is still making steady progress — this distinguishes a wedged agent
	// from a long but productive one, so a big task's multi-step turn runs to
	// completion while a dead HTTP socket is cut promptly. 0 = disabled (only the
	// absolute ceiling applies). It is wired into the opencode client
	// (Opencode.IdleTimeout); the runner mirrors it only for the GC warning wording.
	TurnIdleTimeoutMinutes int `yaml:"turn_idle_timeout_minutes"`

	// TurnIdleRetries is how many times a single pass RECOVERS in-place from an idle
	// stall before giving up. The idle watchdog catches a genuinely wedged upstream
	// turn (github-copilot occasionally holds the streaming request open with zero
	// output and no error, and opencode has no provider read-timeout), but these
	// hangs are PROBABILISTIC: aborting the wedged turn server-side and re-driving
	// the SAME session (its investigation/context is preserved) usually clears it,
	// so a big task no longer loses a whole pass — and every retry from scratch — to
	// one transient hang. Each retry costs up to TurnIdleTimeoutMinutes of wall time,
	// so the total is bounded by both this count and the run WallCap/TTL. Only after
	// this many cumulative idle stalls in a pass does it abandon the task for GC. 0 =
	// the historical behavior (first idle stall abandons immediately).
	TurnIdleRetries int `yaml:"turn_idle_retries"`

	// BuildEnv is the host-specific Go build/test environment the runner OWNS so no
	// honeybee re-derives it (audit session-audit-001 F1: e.g. a broken host cgo
	// linker forces CGO_ENABLED=0, a quota-limited /tmp forces a root-fs GOTMPDIR/
	// GOCACHE). It is EXPORTED into the honeybee process env at agent spawn (so
	// build/test subprocesses the honeybee itself spawns inherit it) AND stated
	// once in the injected prompt preamble as the mandated invocation — both
	// sourced from this one map so they never drift. Layered per KEY (see merge): a
	// more specific layer overrides individual keys; unset keys fall through; an
	// empty value is unset. Inert (nil) by default so a normal host is unaffected;
	// LOCALS.md is the human record of what to put here. Adding a map field makes
	// Config non-comparable with ==; callers/tests use reflect.DeepEqual.
	BuildEnv map[string]string `yaml:"build_env"`
	// SessionPullSeconds is how often the frontend fast-forwards local main from
	// the remote to follow off-box honeybee sessions (session stubs + final
	// transcripts an agent on another host published). It coalesces the polled
	// session panes so many open viewers make at most one `git pull --ff-only` per
	// interval. 0 = the 2s default. Ignored on a single-host repo (no remote).
	SessionPullSeconds int `yaml:"session_pull_seconds"`

	// Tags DECLARES config-driven session tags for the /stats tag model
	// (stats-tag-model): extra key->value labels attached, on read, to any session
	// whose built-in FACET matches. It is an OPEN, three-level map
	//
	//	facet -> facet-value -> (tag-key -> tag-value)
	//
	// e.g. Tags["submodule"]["frontend"]["cohort"] = "A" tags every session in the
	// frontend submodule cohort=A, and
	// Tags["model"]["github-copilot/claude-opus-4.8"]["tier"] = "frontier" tags every
	// opus session tier=frontier. The facet is any BUILT-IN tag key (submodule,
	// kind, branch, model, or a future built-in); the tag key and value are
	// arbitrary — nothing is a fixed schema, so an operator marks cohorts/experiments
	// with no code change. Merged three levels DEEP across config layers (see
	// mergeTags): a more specific layer adds or overrides a single leaf label while
	// the rest fall through. Inert (nil) by default so an install that declares no
	// tags is unaffected. Adding a map field keeps Config non-comparable with ==;
	// callers/tests use reflect.DeepEqual.
	Tags map[string]map[string]map[string]string `yaml:"tags"`

	// AbortOnRemoteFailure governs whether a configured remote (e.g. a gitea
	// backup) that CANNOT BE REACHED at a honeybee's startup preflight is fatal.
	//
	//   - true (the default, and the historical behavior): a remote-sharing pass
	//     whose startup `fetch <remote> main` fails ABORTS before spending any
	//     tokens — work done while unable to catch up to main is invalid, so the
	//     swarm intentionally makes no progress until the remote is healthy.
	//   - false: the SAME unreachable-remote preflight is NON-fatal. The pass
	//     DEGRADES to local-only convergence (it treats the remote as absent for
	//     its whole lifetime: publishes to the local checked-out main via
	//     updateInstead, does no remote fetch/push), logs a WARNING, and proceeds.
	//     This is the deliberate operator escape hatch for a remote OUTAGE: flip it
	//     false so honeybees keep working locally while the remote is down, then
	//     flip it back true once the remote is healthy. During a full outage every
	//     pass degrades uniformly, so convergence is pure local-sharing (the
	//     documented single-host mode) with no divergence; the remote's replica is
	//     re-synced by the backup push / next healthy pass on recovery. See
	//     docs/sharing-modes.md for the caveat under PARTIAL (flaky) connectivity.
	//
	// It is a *bool so "unset" (nil, => the true default) is distinguishable from an
	// explicit `abort_on_remote_failure: false`; read it through
	// AbortsOnRemoteFailure(). Only the startup preflight consults it — a remote
	// that is reachable at preflight but fails MID-pass still fails that individual
	// pass (then retries), independent of this flag.
	AbortOnRemoteFailure *bool `yaml:"abort_on_remote_failure"`
}

// boolPtr returns a pointer to b, for *bool config fields whose default is not the
// bool zero value (so "unset" must be nil, not false).
func boolPtr(b bool) *bool { return &b }

// Defaults are the lowest layer, applied when no file sets a field.
func Defaults(dir string) Config {
	return Config{
		Dir:                    dir,
		GPGHome:                filepath.Join(dir, "gnupg"),
		AgentCmd:               "opencode",
		AgentURL:               "http://127.0.0.1:4096",
		TTLMinutes:             60,
		MaxTurns:               15,
		MergeRetries:           8,
		RejectLimit:            3,
		AbortOnRemoteFailure:   boolPtr(true),
		TurnTimeoutMinutes:     180,
		TurnIdleTimeoutMinutes: 15,
		TurnIdleRetries:        2,
		SessionPullSeconds:     2,
	}
}

// resolveDir resolves the host config/keyring dir USER-FIRST — the first location
// that is usable, in this order:
//
//  1. $BEEHIVE_CONFIG_DIR — an explicit override, honored VERBATIM even when it
//     does not yet exist, so a fresh scaffold can create the dir at exactly that
//     path.
//  2. ${XDG_CONFIG_HOME:-~/.config}/beehive — the per-user config dir, but only
//     when it already EXISTS, so a plain user install is picked up without having
//     to export BEEHIVE_CONFIG_DIR into every process (systemd user units,
//     transient passes, shells) and `beehive secret` reads the right keyring. A
//     relative XDG_CONFIG_HOME is invalid per the XDG Base Directory spec and is
//     ignored (fall back to ~/.config); with neither an absolute XDG_CONFIG_HOME
//     nor HOME set this scope is skipped.
//  3. DefaultDir (/etc/beehive) — the final, UNCONDITIONAL fallback (never
//     stat-probed), so a bare system install is byte-identical to before.
func resolveDir() string {
	if d := os.Getenv("BEEHIVE_CONFIG_DIR"); d != "" {
		return d // explicit override, used verbatim (may not exist yet)
	}
	if d := userConfigDir(); d != "" {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d // an existing per-user dir wins over the system default
		}
	}
	return DefaultDir // unconditional fallback; /etc is never stat-probed
}

// userConfigDir returns ${XDG_CONFIG_HOME:-~/.config}/beehive, or "" when no
// usable base exists. Per the XDG Base Directory spec a relative XDG_CONFIG_HOME
// is invalid and ignored, falling back to ~/.config; with neither an absolute
// XDG_CONFIG_HOME nor HOME set there is no user scope, so "" is returned and the
// caller uses the system default. The path is NOT probed here — resolveDir owns
// the existence check.
func userConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) { // unset or relative -> invalid per XDG; use ~/.config
		home := os.Getenv("HOME")
		if home == "" {
			return "" // no HOME and no absolute XDG_CONFIG_HOME: no user scope
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "beehive")
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
// never carried from a file layer.
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
	// Models merges key-by-key (not whole-map replace): a more specific layer's
	// entry for a kind wins, unset kinds fall through, so a submodule can override
	// a single kind without restating the rest. A fresh map is allocated so no
	// layer's map is mutated in place (they alias through the `out := base` copy).
	if len(base.Models) > 0 || len(over.Models) > 0 {
		merged := make(map[string]string, len(base.Models)+len(over.Models))
		for k, v := range base.Models {
			merged[k] = v
		}
		for k, v := range over.Models {
			if v != "" {
				merged[k] = v
			}
		}
		out.Models = merged
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
	if over.MergeRetries != 0 {
		out.MergeRetries = over.MergeRetries
	}
	if over.RejectLimit != 0 {
		out.RejectLimit = over.RejectLimit
	}
	if over.StallTurns != 0 {
		out.StallTurns = over.StallTurns
	}
	if over.TurnTimeoutMinutes != 0 {
		out.TurnTimeoutMinutes = over.TurnTimeoutMinutes
	}
	if over.TurnIdleTimeoutMinutes != 0 {
		out.TurnIdleTimeoutMinutes = over.TurnIdleTimeoutMinutes
	}
	if over.TurnIdleRetries != 0 {
		out.TurnIdleRetries = over.TurnIdleRetries
	}
	out.BuildEnv = mergeEnv(base.BuildEnv, over.BuildEnv)
	if over.SessionPullSeconds != 0 {
		out.SessionPullSeconds = over.SessionPullSeconds
	}
	if over.AbortOnRemoteFailure != nil {
		out.AbortOnRemoteFailure = over.AbortOnRemoteFailure
	}
	out.Tags = mergeTags(base.Tags, over.Tags)
	return out
}

// ModelFor returns the agent model to use for a pass of the given task kind: the
// per-kind override from the layered Models map when set, otherwise the single
// resolved Model. kind is the selection kind string ("work", "reconcile",
// "review", "arbitrate", "bootstrap"). An empty return means "no model
// configured" — callers treat that as inert (keep the client's own default), so
// a host that sets neither models nor model is unaffected, and a single-model
// host resolves every kind to the same Model it always used.
func (c Config) ModelFor(kind string) string {
	if m := c.Models[kind]; m != "" {
		return m
	}
	return c.Model
}

// AbortsOnRemoteFailure reports the effective abort_on_remote_failure setting:
// true (the default) unless a config layer explicitly set it to false. When true,
// a configured remote that cannot be reached at a honeybee's startup preflight is
// fatal (the pass aborts before spending tokens); when false, that same failure
// degrades the pass to local-only convergence instead. A nil pointer (no layer
// set it) means the default, true.
func (c Config) AbortsOnRemoteFailure() bool {
	return c.AbortOnRemoteFailure == nil || *c.AbortOnRemoteFailure
}

// mergeEnv layers build_env per KEY (not whole-map): over's non-empty keys win,
// base keys fall through, and an empty value is treated as unset (never overrides
// a lower layer), mirroring the "zero == unset" rule used for every scalar field.
// When over contributes it returns a FRESH map, so a lower layer's map is never
// mutated; when over sets nothing it returns base unchanged (fall-through). So a
// submodule can retune one var without restating the whole host map.
func mergeEnv(base, over map[string]string) map[string]string {
	if len(over) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range over {
		if v == "" {
			continue // zero == unset: don't override a lower layer with a blank
		}
		merged[k] = v
	}
	return merged
}

// mergeTags deep-merges the config-declared session-tag map three levels down —
// facet -> facet-value -> (tag-key -> tag-value) — so a more specific layer adds
// or overrides a single LEAF label without restating the rest. over's non-empty
// leaves win, base leaves fall through, and an empty leaf value is treated as
// unset (never overrides a lower layer), mirroring the "zero == unset" rule used
// for every scalar and for mergeEnv. When over contributes it returns a FRESH
// nested map (base is deep-copied), so no lower layer's map is mutated; when over
// is empty it returns base unchanged (fall-through), keeping the inert-nil default.
func mergeTags(base, over map[string]map[string]map[string]string) map[string]map[string]map[string]string {
	if len(over) == 0 {
		return base
	}
	merged := make(map[string]map[string]map[string]string, len(base)+len(over))
	for facet, vals := range base {
		fm := make(map[string]map[string]string, len(vals))
		for val, kv := range vals {
			inner := make(map[string]string, len(kv))
			for k, v := range kv {
				inner[k] = v
			}
			fm[val] = inner
		}
		merged[facet] = fm
	}
	for facet, vals := range over {
		fm := merged[facet]
		if fm == nil {
			fm = make(map[string]map[string]string, len(vals))
			merged[facet] = fm
		}
		for val, kv := range vals {
			inner := fm[val]
			if inner == nil {
				inner = make(map[string]string, len(kv))
				fm[val] = inner
			}
			for k, v := range kv {
				if v == "" {
					continue // zero == unset: a blank leaf never overrides a lower layer
				}
				inner[k] = v
			}
		}
	}
	return merged
}

// layerPaths returns the ordered config files (lowest precedence first, excluding
// built-in Defaults) for a submodule. submodule may be "" to resolve only the
// host + in-repo-global scopes.
func layerPaths(dir, root, submodule string) []string {
	paths := []string{
		filepath.Join(dir, FileName),  // host file: /etc/beehive/config.yaml
		filepath.Join(root, FileName), // in-repo global defaults
	}
	if submodule != "" {
		// per-submodule override (beehive layer, alongside ROI.md/PLAN.md)
		paths = append(paths, filepath.Join(root, "submodules", submodule, FileName))
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
	layer, ok, err := loadFile(filepath.Join(dir, FileName))
	if err != nil {
		return c, err
	}
	if ok {
		c = merge(c, layer)
	}
	c.Dir = dir
	return c, nil
}
