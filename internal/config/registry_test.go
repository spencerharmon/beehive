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
