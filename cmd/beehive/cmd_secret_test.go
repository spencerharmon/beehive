package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/secrets"
)

// TestSecretStorePerRepoKeyring: with a registry present, `beehive secret` in a
// given repo resolves that repo's OWN isolated keyring (RepoEntry.Config), never a
// process-global one. Two registered repos yield stores with distinct keyrings, an
// explicit --recipient overrides the default, and --submodule selects the
// per-submodule path under the same repo root and keyring.
func TestSecretStorePerRepoKeyring(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", cfgDir)
	rootA := t.TempDir()
	rootB := t.TempDir()
	homeA := filepath.Join(rootA, "gnupg")
	homeB := filepath.Join(rootB, "gnupg")
	repos := "repos:\n" +
		"  - name: alpha\n    root: " + rootA + "\n    gpg_home: " + homeA + "\n    gpg_recipient: alpha@example.com\n" +
		"  - name: bravo\n    root: " + rootB + "\n    gpg_home: " + homeB + "\n    gpg_recipient: bravo@example.com\n"
	if err := os.WriteFile(filepath.Join(cfgDir, config.RegistryFile), []byte(repos), 0o644); err != nil {
		t.Fatal(err)
	}

	sa, err := secretStore(rootA, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sa.GPGHome != homeA || sa.Recipient != "alpha@example.com" {
		t.Fatalf("alpha store keyring = %q/%q, want %q/alpha@example.com", sa.GPGHome, sa.Recipient, homeA)
	}
	if sa.Path != secrets.GlobalPath(rootA) {
		t.Fatalf("alpha path = %q, want %q", sa.Path, secrets.GlobalPath(rootA))
	}

	sb, err := secretStore(rootB, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sb.GPGHome != homeB || sb.Recipient != "bravo@example.com" {
		t.Fatalf("bravo store keyring = %q/%q, want %q/bravo@example.com", sb.GPGHome, sb.Recipient, homeB)
	}

	// Strict isolation: the two repos' CLI stores never share a keyring or a key.
	if sa.GPGHome == sb.GPGHome || sa.Recipient == sb.Recipient {
		t.Fatalf("CLI stores must not share a keyring: a=%q/%q b=%q/%q",
			sa.GPGHome, sa.Recipient, sb.GPGHome, sb.Recipient)
	}

	// --submodule selects the per-submodule path; --recipient overrides the
	// recipient; the keyring HOME stays the active repo's own.
	ss, err := secretStore(rootA, "redteam", "override@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if ss.Path != secrets.SubmodulePath(rootA, "redteam") {
		t.Fatalf("submodule path = %q, want %q", ss.Path, secrets.SubmodulePath(rootA, "redteam"))
	}
	if ss.GPGHome != homeA || ss.Recipient != "override@example.com" {
		t.Fatalf("override store = %q/%q, want %q/override@example.com", ss.GPGHome, ss.Recipient, homeA)
	}
}

// TestSecretStoreUnregisteredRootFailsLoudly: with a registry present, a repo root
// that is not registered must ERROR rather than fall back to a shared/global
// keyring — the isolation guarantee the CLI must not silently break.
func TestSecretStoreUnregisteredRootFailsLoudly(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", cfgDir)
	rootA := t.TempDir()
	repos := "repos:\n  - {name: alpha, root: " + rootA +
		", gpg_home: " + filepath.Join(rootA, "gnupg") + ", gpg_recipient: alpha@example.com}\n"
	if err := os.WriteFile(filepath.Join(cfgDir, config.RegistryFile), []byte(repos), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := secretStore(t.TempDir(), "", ""); err == nil {
		t.Fatal("unregistered root must fail loudly (no shared-keyring fallback), got nil")
	}
}

// TestSecretStoreLegacyUsesHostConfig: with NO repos.yaml the legacy single-repo
// keyring resolution is unchanged — the host-layer config.Load keyring is used and
// the path is the repo-root global secrets file. This is the "empty-registry path
// unchanged" guarantee.
func TestSecretStoreLegacyUsesHostConfig(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", cfgDir)
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName),
		[]byte("gpg_home: "+filepath.Join(cfgDir, "gnupg")+"\ngpg_recipient: host@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s, err := secretStore(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.GPGHome != filepath.Join(cfgDir, "gnupg") || s.Recipient != "host@example.com" {
		t.Fatalf("legacy store keyring = %q/%q, want host config keyring", s.GPGHome, s.Recipient)
	}
	if s.Path != secrets.GlobalPath(root) {
		t.Fatalf("legacy path = %q, want %q", s.Path, secrets.GlobalPath(root))
	}
}
