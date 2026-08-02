package swarm

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/spencerharmon/beehive/internal/secrets"
)

// newIsolationKeyring generates an ephemeral gpg key in a temp homedir; skips the
// test if gpg is unavailable. Mirrors the secrets package's own test helper so the
// runner-boundary test can encrypt real per-submodule SECRETS.yaml.gpg files.
func newIsolationKeyring(t *testing.T) (home, recipient string) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	home = t.TempDir()
	os.Chmod(home, 0o700)
	recipient = "beehive-isolation-test@example.com"
	batch := "Key-Type: RSA\nKey-Length: 2048\nName-Real: bh\nName-Email: " +
		recipient + "\nExpire-Date: 0\n%no-protection\n%commit\n"
	cmd := exec.Command("gpg", "--homedir", home, "--batch", "--gen-key")
	r, w, _ := os.Pipe()
	go func() { w.WriteString(batch); w.Close() }()
	cmd.Stdin = r
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("gpg gen-key failed: %v: %s", err, out)
	}
	return
}

// TestRunnerMaterializationSecretIsolation is the end-to-end regression that
// enforces the per-submodule secret isolation INVARIANT across the runner's
// materialization boundary: it wires the REAL secrets.ScopedEnv into the Runner's
// SecretsFor (exactly as cmd/honeybee does) and drives the actual
// materializeSecrets path, so it proves the property on the production wiring
// rather than a mock.
//
// The invariant, stated three ways, all of which must hold and would FAIL if the
// scoping regressed to a global merge or a wrong-submodule lookup:
//   - a pass for target A materializes A's OWN secrets plus the GLOBAL ones;
//   - a same-name collision resolves to A's own value (or, absent an A override,
//     the global value) — NEVER a sibling B's value;
//   - a name that exists ONLY in sibling B is INVISIBLE to A's materialization.
func TestRunnerMaterializationSecretIsolation(t *testing.T) {
	home, rcpt := newIsolationKeyring(t)
	root := t.TempDir()
	ctx := context.Background()

	// Global scope: a key shared with both submodules, and a global-only key.
	global := secrets.Store{Path: secrets.GlobalPath(root), GPGHome: home, Recipient: rcpt}
	if err := global.Save(ctx, map[string]any{
		"SHARED":      "global-value",
		"GLOBAL_ONLY": "g",
	}); err != nil {
		t.Fatal(err)
	}
	// Submodule A ("foo"): overrides SHARED and owns a private key.
	foo := secrets.Store{Path: secrets.SubmodulePath(root, "foo"), GPGHome: home, Recipient: rcpt}
	if err := foo.Save(ctx, map[string]any{
		"SHARED":   "foo-value",
		"FOO_ONLY": "f",
	}); err != nil {
		t.Fatal(err)
	}
	// Submodule B ("bar"): a DIFFERENT SHARED value plus a B-private key. Neither
	// may ever surface in A's materialization.
	bar := secrets.Store{Path: secrets.SubmodulePath(root, "bar"), GPGHome: home, Recipient: rcpt}
	if err := bar.Save(ctx, map[string]any{
		"SHARED":   "bar-value",
		"BAR_ONLY": "b",
	}); err != nil {
		t.Fatal(err)
	}

	// Wire the runner EXACTLY as production does: SecretsFor = the real ScopedEnv
	// bound to this root+keyring; capture the materialized env through the
	// ExportSecretEnv seam so the real process env is never mutated.
	var exported map[string]string
	r := &Runner{
		SecretsFor: func(ctx context.Context, submodule string) (map[string]string, error) {
			return secrets.ScopedEnv(ctx, root, submodule, home)
		},
		ExportSecretEnv: func(m map[string]string) { exported = m },
	}

	// --- Pass for target A ("foo") ---
	exported = nil
	r.materializeSecrets(ctx, "foo")
	if exported == nil {
		t.Fatal("foo pass materialized nothing; want its scoped secrets")
	}
	if exported["SHARED"] != "foo-value" {
		t.Fatalf("foo SHARED = %q, want foo-value (own override wins, never bar's)", exported["SHARED"])
	}
	if exported["GLOBAL_ONLY"] != "g" {
		t.Fatalf("foo GLOBAL_ONLY = %q, want inherited global value", exported["GLOBAL_ONLY"])
	}
	if exported["FOO_ONLY"] != "f" {
		t.Fatalf("foo FOO_ONLY = %q, want foo's own secret", exported["FOO_ONLY"])
	}
	if v, ok := exported["BAR_ONLY"]; ok {
		t.Fatalf("foo materialization leaked sibling secret BAR_ONLY=%q — isolation regressed", v)
	}
	if exported["SHARED"] == "bar-value" {
		t.Fatal("foo SHARED resolved to bar's value — cross-submodule leak")
	}

	// --- Pass for target B ("bar") ---
	exported = nil
	r.materializeSecrets(ctx, "bar")
	if exported == nil {
		t.Fatal("bar pass materialized nothing; want its scoped secrets")
	}
	if exported["SHARED"] != "bar-value" {
		t.Fatalf("bar SHARED = %q, want bar-value (own override wins, never foo's)", exported["SHARED"])
	}
	if exported["GLOBAL_ONLY"] != "g" {
		t.Fatalf("bar GLOBAL_ONLY = %q, want inherited global value", exported["GLOBAL_ONLY"])
	}
	if exported["BAR_ONLY"] != "b" {
		t.Fatalf("bar BAR_ONLY = %q, want bar's own secret", exported["BAR_ONLY"])
	}
	if v, ok := exported["FOO_ONLY"]; ok {
		t.Fatalf("bar materialization leaked sibling secret FOO_ONLY=%q — isolation regressed", v)
	}

	// --- A collision resolving to GLOBAL when the pass submodule has no override ---
	// "baz" has no per-submodule store, so SHARED must resolve to the global value,
	// never to any sibling's — proving the merge names exactly one submodule scope.
	exported = nil
	r.materializeSecrets(ctx, "baz")
	if exported["SHARED"] != "global-value" {
		t.Fatalf("baz SHARED = %q, want global-value (no override, no sibling leak)", exported["SHARED"])
	}
	if _, ok := exported["FOO_ONLY"]; ok {
		t.Fatal("baz materialization leaked FOO_ONLY — isolation regressed")
	}
	if _, ok := exported["BAR_ONLY"]; ok {
		t.Fatal("baz materialization leaked BAR_ONLY — isolation regressed")
	}
}
