package secrets

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newKeyring generates an ephemeral gpg key in a temp homedir; skips if no gpg.
func newKeyring(t *testing.T) (home, recipient string) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	home = t.TempDir()
	os.Chmod(home, 0o700)
	recipient = "beehive-test@example.com"
	batch := "Key-Type: RSA\nKey-Length: 2048\nName-Real: bh\nName-Email: " +
		recipient + "\nExpire-Date: 0\n%no-protection\n%commit\n"
	cmd := exec.Command("gpg", "--homedir", home, "--batch", "--gen-key")
	cmd.Stdin = stringReader(batch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("gpg gen-key failed: %v: %s", err, out)
	}
	return
}

func stringReader(s string) *os.File {
	r, w, _ := os.Pipe()
	go func() { w.WriteString(s); w.Close() }()
	return r
}

func TestRoundTrip(t *testing.T) {
	home, rcpt := newKeyring(t)
	s := Store{Path: filepath.Join(t.TempDir(), "SECRETS.yaml.gpg"), GPGHome: home, Recipient: rcpt}
	ctx := context.Background()

	d, err := s.Load(ctx)
	if err != nil || len(d) != 0 {
		t.Fatalf("empty load: %v %v", d, err)
	}
	if err := s.Save(ctx, map[string]any{"db_pw": "hunter2", "n": 3}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["db_pw"] != "hunter2" || got["n"] != 3 {
		t.Fatalf("roundtrip got %v", got)
	}
}

func TestAddUpdate(t *testing.T) {
	home, rcpt := newKeyring(t)
	s := Store{Path: filepath.Join(t.TempDir(), "SECRETS.yaml.gpg"), GPGHome: home, Recipient: rcpt}
	ctx := context.Background()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.yaml")
	os.WriteFile(f, []byte("k: v\n"), 0o600)
	if err := s.Add(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(ctx, f); err == nil {
		t.Fatal("collision not rejected")
	}
	os.WriteFile(f, []byte("k: v2\n"), 0o600)
	if err := s.Update(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load(ctx)
	if got["k"] != "v2" {
		t.Fatalf("update got %v", got)
	}
}

func TestMultiDocRejected(t *testing.T) {
	doc := map[string]any{}
	if err := yamlSingleDoc([]byte("a: 1\n---\nb: 2\n"), &doc); err == nil {
		t.Fatal("multi-doc not rejected")
	}
}

// TestEmptyGPGHomeFailsLoudly confirms a Store with no keyring configured refuses
// every gpg operation instead of silently falling through to gpg's process-
// default keyring (the shared-keyring fallback that would break per-repo secret
// isolation). Load of a NON-EMPTY file and any Save must error; the missing-file
// fast path (no gpg invoked) stays a benign empty map.
func TestEmptyGPGHomeFailsLoudly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "SECRETS.yaml.gpg")
	s := Store{Path: path, GPGHome: "", Recipient: "x@example.com"}

	// Missing file: still no gpg call, so the empty-map contract is preserved.
	if d, err := s.Load(ctx); err != nil || len(d) != 0 {
		t.Fatalf("missing-file Load with empty home = %v, %v; want empty map, nil", d, err)
	}
	// A present, non-empty file would need gpg to decrypt: must fail loudly.
	if err := os.WriteFile(path, []byte("not-empty-ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx); err == nil {
		t.Fatal("Load with empty GPGHome must fail loudly (no shared-keyring fallback)")
	}
	// Save always needs gpg: must fail loudly regardless of file state.
	if err := s.Save(ctx, map[string]any{"k": "v"}); err == nil {
		t.Fatal("Save with empty GPGHome must fail loudly (no shared-keyring fallback)")
	}
}
