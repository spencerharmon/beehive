package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestLoadRegistryMissing confirms a host with no repos.yaml resolves to an empty
// (legacy single-repo) registry with no error, so bare installs keep working.
func TestLoadRegistryMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", dir)
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if !reg.Empty() {
		t.Fatalf("missing repos.yaml should be empty registry, got %+v", reg)
	}
	if names := reg.Names(); len(names) != 0 {
		t.Fatalf("empty registry Names = %v, want none", names)
	}
}

// TestLoadRegistryParses confirms a two-repo repos.yaml parses, exposes sorted
// names, and resolves each entry by name with its own keyring.
func TestLoadRegistryParses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", dir)
	write(t, filepath.Join(dir, RegistryFile), `
repos:
  - name: zeta
    root: /srv/zeta
    gpg_home: /srv/zeta/gnupg
    gpg_recipient: zeta@example.com
    model: zeta/model
  - name: alpha
    root: /srv/alpha
    gpg_home: /srv/alpha/gnupg
    gpg_recipient: alpha@example.com
`)
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if reg.Empty() {
		t.Fatal("registry should not be empty")
	}
	if got, want := reg.Names(), []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v (sorted)", got, want)
	}
	z, ok := reg.Repo("zeta")
	if !ok {
		t.Fatal("Repo(zeta) not found")
	}
	if z.Root != "/srv/zeta" || z.GPGHome != "/srv/zeta/gnupg" || z.GPGRecipient != "zeta@example.com" || z.Model != "zeta/model" {
		t.Fatalf("zeta entry mis-parsed: %+v", z)
	}
	if _, ok := reg.Repo("missing"); ok {
		t.Fatal("Repo(missing) should not be found")
	}
}

// TestLoadRegistryMalformed confirms a present-but-malformed registry surfaces an
// error instead of loading silently.
func TestLoadRegistryMalformed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", dir)
	write(t, filepath.Join(dir, RegistryFile), "repos: : : not yaml\n\t- broken")
	if _, err := LoadRegistry(); err == nil {
		t.Fatal("expected error for malformed registry, got nil")
	}
}

// TestLoadRegistryValidatesOnLoad confirms LoadRegistry rejects a registry that
// violates secret isolation (shared keyring) at load time, not just via an
// explicit Validate call.
func TestLoadRegistryValidatesOnLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", dir)
	write(t, filepath.Join(dir, RegistryFile), `
repos:
  - name: a
    root: /srv/a
    gpg_home: /shared/gnupg
    gpg_recipient: a@example.com
  - name: b
    root: /srv/b
    gpg_home: /shared/gnupg
    gpg_recipient: b@example.com
`)
	if _, err := LoadRegistry(); err == nil {
		t.Fatal("LoadRegistry should reject a shared gpg_home, got nil")
	}
}

// TestRegistryValidateOK confirms a well-formed multi-repo registry (distinct
// roots, homes, and recipients) validates.
func TestRegistryValidateOK(t *testing.T) {
	reg := Registry{Repos: []RepoEntry{
		{Name: "a", Root: "/srv/a", GPGHome: "/srv/a/gnupg", GPGRecipient: "a@example.com"},
		{Name: "b", Root: "/srv/b", GPGHome: "/srv/b/gnupg", GPGRecipient: "b@example.com"},
	}}
	if err := reg.Validate(); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
}

// TestRegistryValidateEmpty confirms the empty (legacy single-repo) registry is
// always valid.
func TestRegistryValidateEmpty(t *testing.T) {
	if err := (Registry{}).Validate(); err != nil {
		t.Fatalf("empty registry should validate: %v", err)
	}
}

// TestRegistryValidateRejects table-drives every isolation/uniqueness/required
// invariant, asserting each misconfiguration is rejected.
func TestRegistryValidateRejects(t *testing.T) {
	cases := map[string]Registry{
		"missing name": {Repos: []RepoEntry{
			{Name: "", Root: "/srv/a", GPGHome: "/srv/a/gnupg", GPGRecipient: "a@example.com"},
		}},
		"missing root": {Repos: []RepoEntry{
			{Name: "a", Root: "", GPGHome: "/srv/a/gnupg", GPGRecipient: "a@example.com"},
		}},
		"missing gpg_home": {Repos: []RepoEntry{
			{Name: "a", Root: "/srv/a", GPGHome: "", GPGRecipient: "a@example.com"},
		}},
		"missing gpg_recipient": {Repos: []RepoEntry{
			{Name: "a", Root: "/srv/a", GPGHome: "/srv/a/gnupg", GPGRecipient: ""},
		}},
		"duplicate name": {Repos: []RepoEntry{
			{Name: "a", Root: "/srv/a", GPGHome: "/srv/a/gnupg", GPGRecipient: "a@example.com"},
			{Name: "a", Root: "/srv/b", GPGHome: "/srv/b/gnupg", GPGRecipient: "b@example.com"},
		}},
		"duplicate root": {Repos: []RepoEntry{
			{Name: "a", Root: "/srv/x", GPGHome: "/srv/a/gnupg", GPGRecipient: "a@example.com"},
			{Name: "b", Root: "/srv/x/", GPGHome: "/srv/b/gnupg", GPGRecipient: "b@example.com"},
		}},
		"shared gpg_home (isolation)": {Repos: []RepoEntry{
			{Name: "a", Root: "/srv/a", GPGHome: "/shared/gnupg", GPGRecipient: "a@example.com"},
			{Name: "b", Root: "/srv/b", GPGHome: "/shared/gnupg/", GPGRecipient: "b@example.com"},
		}},
		"reused gpg_recipient (key reuse)": {Repos: []RepoEntry{
			{Name: "a", Root: "/srv/a", GPGHome: "/srv/a/gnupg", GPGRecipient: "shared@example.com"},
			{Name: "b", Root: "/srv/b", GPGHome: "/srv/b/gnupg", GPGRecipient: "shared@example.com"},
		}},
	}
	for name, reg := range cases {
		if err := reg.Validate(); err == nil {
			t.Errorf("%s: expected Validate error, got nil", name)
		}
	}
}

// TestRepoEntryConfigPerKeyring confirms RepoEntry.Config always scopes the
// keyring to this repo (never inheriting base's) and overlays agent overrides
// only when set, and that two entries' projected configs have DISTINCT keyrings —
// the structural guarantee that one repo's secrets.Store can never use another's.
func TestRepoEntryConfigPerKeyring(t *testing.T) {
	base := Config{
		GPGHome:      "/base/gnupg",
		GPGRecipient: "base@example.com",
		AgentCmd:     "opencode",
		AgentURL:     "http://base:1",
		Model:        "base/model",
		TTLMinutes:   60,
	}
	a := RepoEntry{Name: "a", Root: "/srv/a", GPGHome: "/srv/a/gnupg", GPGRecipient: "a@example.com", Model: "a/model"}
	b := RepoEntry{Name: "b", Root: "/srv/b", GPGHome: "/srv/b/gnupg", GPGRecipient: "b@example.com"}

	ca := a.Config(base)
	cb := b.Config(base)

	// Keyring is always the repo's own, never base's.
	if ca.GPGHome != "/srv/a/gnupg" || ca.GPGRecipient != "a@example.com" {
		t.Fatalf("entry a keyring = %q/%q, want /srv/a/gnupg/a@example.com", ca.GPGHome, ca.GPGRecipient)
	}
	if cb.GPGHome != "/srv/b/gnupg" || cb.GPGRecipient != "b@example.com" {
		t.Fatalf("entry b keyring = %q/%q, want /srv/b/gnupg/b@example.com", cb.GPGHome, cb.GPGRecipient)
	}
	// Strict isolation: the two repos' effective keyrings differ.
	if ca.GPGHome == cb.GPGHome || ca.GPGRecipient == cb.GPGRecipient {
		t.Fatalf("repos must not share a keyring: a=%q/%q b=%q/%q", ca.GPGHome, ca.GPGRecipient, cb.GPGHome, cb.GPGRecipient)
	}
	// Override set on a wins; unset on b falls through to base.
	if ca.Model != "a/model" {
		t.Errorf("entry a Model = %q, want a/model (override)", ca.Model)
	}
	if cb.Model != "base/model" {
		t.Errorf("entry b Model = %q, want base/model (falls through)", cb.Model)
	}
	// Unrelated base fields are preserved.
	if ca.AgentCmd != "opencode" || ca.TTLMinutes != 60 {
		t.Errorf("base fields not preserved: %+v", ca)
	}
}

// TestRepoByRootMatches confirms RepoByRoot resolves the entry that owns a given
// filesystem root — the CLI's bridge from "the repo I'm invoked in" to that
// repo's isolated keyring — matching regardless of a trailing slash or a
// non-cleaned path, and reporting not-found for a root no entry owns (which the
// caller turns into a fail-loud "not registered" rather than a shared-keyring
// fallback). The empty registry owns no root.
func TestRepoByRootMatches(t *testing.T) {
	// Use real directories so the symlink-resolving normalization has targets.
	rootA := t.TempDir()
	rootB := t.TempDir()
	reg := Registry{Repos: []RepoEntry{
		{Name: "a", Root: rootA, GPGHome: filepath.Join(rootA, "gnupg"), GPGRecipient: "a@example.com"},
		{Name: "b", Root: rootB, GPGHome: filepath.Join(rootB, "gnupg"), GPGRecipient: "b@example.com"},
	}}
	if e, ok := reg.RepoByRoot(rootA); !ok || e.Name != "a" {
		t.Fatalf("RepoByRoot(rootA) = %+v ok=%v, want entry a", e, ok)
	}
	// A trailing slash and an un-cleaned path still resolve to the same entry.
	if e, ok := reg.RepoByRoot(rootB + "/"); !ok || e.Name != "b" {
		t.Fatalf("RepoByRoot(rootB/) = %+v ok=%v, want entry b", e, ok)
	}
	if e, ok := reg.RepoByRoot(filepath.Join(rootA, "sub", "..")); !ok || e.Name != "a" {
		t.Fatalf("RepoByRoot(rootA/sub/..) = %+v ok=%v, want entry a", e, ok)
	}
	// A root no entry owns is not found (caller must fail loudly).
	if _, ok := reg.RepoByRoot(t.TempDir()); ok {
		t.Fatal("RepoByRoot(unregistered root) should be not-found")
	}
	// The empty (legacy) registry owns no root.
	if _, ok := (Registry{}).RepoByRoot(rootA); ok {
		t.Fatal("empty registry RepoByRoot should be not-found")
	}
}

// TestLoadRegistryFromExplicitDir confirms LoadRegistryFrom reads an explicit dir
// (the BEEHIVE_CONFIG_DIR-independent core), and a missing dir file is empty.
func TestLoadRegistryFromExplicitDir(t *testing.T) {
	dir := t.TempDir()
	reg, err := LoadRegistryFrom(dir)
	if err != nil || !reg.Empty() {
		t.Fatalf("empty dir: reg=%+v err=%v", reg, err)
	}
	if err := os.WriteFile(filepath.Join(dir, RegistryFile),
		[]byte("repos:\n  - {name: a, root: /srv/a, gpg_home: /srv/a/gnupg, gpg_recipient: a@example.com}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err = LoadRegistryFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Names(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("Names = %v, want [a]", got)
	}
}

// TestSingleEntryRegistryRoundTrip confirms SingleEntryRegistry is the inverse of
// RepoEntry.Config: the synthesized entry carries the resolved config's own
// keyring + agent settings, and projecting it back over that same config
// reproduces it exactly (the byte-identical legacy guarantee).
func TestSingleEntryRegistryRoundTrip(t *testing.T) {
	cfg := Config{
		Dir:          "/etc/beehive",
		GPGHome:      "/etc/beehive/gnupg",
		GPGRecipient: "host@example.com",
		AgentCmd:     "opencode",
		AgentURL:     "http://127.0.0.1:4096",
		Model:        "host/model",
		Temperature:  0.4,
		MaxTokens:    1234,
		TTLMinutes:   60,
		MaxTurns:     15,
		RejectLimit:  3,
	}
	reg := SingleEntryRegistry(cfg, "/srv/myhive")
	if len(reg.Repos) != 1 {
		t.Fatalf("want 1 entry, got %d", len(reg.Repos))
	}
	e := reg.Repos[0]
	if e.Name != "myhive" {
		t.Errorf("Name = %q, want myhive (root base name)", e.Name)
	}
	if e.Root != "/srv/myhive" {
		t.Errorf("Root = %q, want /srv/myhive", e.Root)
	}
	// Legacy keyring preserved on the entry.
	if e.GPGHome != cfg.GPGHome || e.GPGRecipient != cfg.GPGRecipient {
		t.Errorf("entry keyring = %q/%q, want %q/%q", e.GPGHome, e.GPGRecipient, cfg.GPGHome, cfg.GPGRecipient)
	}
	// Inverse of RepoEntry.Config: projecting the entry back over the same base
	// config reproduces it field-for-field.
	if got := e.Config(cfg); !reflect.DeepEqual(got, cfg) {
		t.Fatalf("Config round-trip not byte-identical:\n got %+v\nwant %+v", got, cfg)
	}
}

// TestSingleEntryRegistryNameFallback confirms a root with no usable base name
// falls back to "default" (a blank handle would break the frontend switcher).
func TestSingleEntryRegistryNameFallback(t *testing.T) {
	for _, root := range []string{".", "", "/", ".."} {
		if got := SingleEntryRegistry(Config{}, root).Repos[0].Name; got != "default" {
			t.Errorf("root %q: Name = %q, want default", root, got)
		}
	}
}

// TestResolveRegistryBareSynthesizes confirms a host with no repos.yaml resolves
// to a one-entry registry equal to today's single resolved config: legacy keyring
// preserved and the entry projects back to that config byte-identically.
func TestResolveRegistryBareSynthesizes(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)
	write(t, filepath.Join(hostDir, "config.yaml"), "gpg_recipient: host@example.com\nmodel: host/model\n")

	reg, err := ResolveRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Empty() || len(reg.Repos) != 1 {
		t.Fatalf("want synthesized one-entry registry, got %+v", reg)
	}
	cfg, err := Resolve(root, "")
	if err != nil {
		t.Fatal(err)
	}
	e := reg.Repos[0]
	if e.Root != root {
		t.Errorf("entry Root = %q, want %q", e.Root, root)
	}
	if e.GPGHome != cfg.GPGHome || e.GPGRecipient != cfg.GPGRecipient {
		t.Errorf("legacy keyring not preserved: entry %q/%q, cfg %q/%q", e.GPGHome, e.GPGRecipient, cfg.GPGHome, cfg.GPGRecipient)
	}
	if got := e.Config(cfg); !reflect.DeepEqual(got, cfg) {
		t.Fatalf("synthesized entry not byte-identical:\n got %+v\nwant %+v", got, cfg)
	}
}

// TestResolveRegistryPresentFileUsed confirms a present repos.yaml is loaded and
// used as-is (not synthesized): the daemon serves the declared multi-repo set.
func TestResolveRegistryPresentFileUsed(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)
	write(t, filepath.Join(hostDir, RegistryFile), `
repos:
  - name: zeta
    root: /srv/zeta
    gpg_home: /srv/zeta/gnupg
    gpg_recipient: zeta@example.com
  - name: alpha
    root: /srv/alpha
    gpg_home: /srv/alpha/gnupg
    gpg_recipient: alpha@example.com
`)
	reg, err := ResolveRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reg.Names(), []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v (declared registry, not synthesized)", got, want)
	}
}

// TestResolveRegistryInvalidFails confirms a present-but-invalid registry (shared
// keyring) fails resolution, so the daemon refuses to start instead of loading a
// secret-isolation violation.
func TestResolveRegistryInvalidFails(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)
	write(t, filepath.Join(hostDir, RegistryFile), `
repos:
  - name: a
    root: /srv/a
    gpg_home: /shared/gnupg
    gpg_recipient: a@example.com
  - name: b
    root: /srv/b
    gpg_home: /shared/gnupg
    gpg_recipient: b@example.com
`)
	if _, err := ResolveRegistry(root); err == nil {
		t.Fatal("ResolveRegistry should reject a shared gpg_home, got nil")
	}
}
