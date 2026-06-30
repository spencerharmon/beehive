package main

import (
	"testing"

	"github.com/spencerharmon/beehive/internal/config"
)

// TestServeTargetSingleEntryByteIdentical confirms the single-repo bridge: the
// active entry of a synthesized one-entry registry projects to today's resolved
// config exactly, and serves the same root. This is the back-compat guarantee —
// an unconfigured host is unchanged by the registry indirection.
func TestServeTargetSingleEntryByteIdentical(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	cfg, err := config.Resolve(root, "")
	if err != nil {
		t.Fatal(err)
	}
	reg := config.SingleEntryRegistry(cfg, root)

	entry, served, err := serveTarget(reg)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Root != root {
		t.Errorf("served root = %q, want %q", entry.Root, root)
	}
	if served != cfg {
		t.Fatalf("served config not byte-identical to single config:\n got %+v\nwant %+v", served, cfg)
	}
}

// TestServeTargetMultiPicksSortedFirstOwnKeyring confirms a multi-repo registry
// serves the first repo by sorted name, projected onto ITS OWN keyring (never
// another repo's) — the per-repo isolation the daemon must preserve.
func TestServeTargetMultiPicksSortedFirstOwnKeyring(t *testing.T) {
	hostDir := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)
	alphaRoot := t.TempDir()
	zetaRoot := t.TempDir()
	reg := config.Registry{Repos: []config.RepoEntry{
		{Name: "zeta", Root: zetaRoot, GPGHome: "/srv/zeta/gnupg", GPGRecipient: "zeta@example.com"},
		{Name: "alpha", Root: alphaRoot, GPGHome: "/srv/alpha/gnupg", GPGRecipient: "alpha@example.com"},
	}}

	entry, served, err := serveTarget(reg)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Name != "alpha" {
		t.Fatalf("active entry = %q, want alpha (first by sorted name)", entry.Name)
	}
	if served.GPGHome != "/srv/alpha/gnupg" || served.GPGRecipient != "alpha@example.com" {
		t.Fatalf("served keyring = %q/%q, want alpha's own /srv/alpha/gnupg + alpha@example.com", served.GPGHome, served.GPGRecipient)
	}
	// Strict isolation: the served config never leaks the other repo's keyring.
	if served.GPGHome == "/srv/zeta/gnupg" || served.GPGRecipient == "zeta@example.com" {
		t.Fatalf("served config leaked zeta's keyring: %q/%q", served.GPGHome, served.GPGRecipient)
	}
}

// TestServeTargetEmptyErrors confirms an empty registry (no repos) is a startup
// error rather than a nil-deref or silent no-op.
func TestServeTargetEmptyErrors(t *testing.T) {
	if _, _, err := serveTarget(config.Registry{}); err == nil {
		t.Fatal("serveTarget(empty) should error, got nil")
	}
}
