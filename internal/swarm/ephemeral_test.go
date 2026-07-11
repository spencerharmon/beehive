package swarm

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestListeningReExtractsURL(t *testing.T) {
	cases := map[string]string{
		"opencode server listening on http://127.0.0.1:41273": "http://127.0.0.1:41273",
		"INFO listening on https://localhost:8080":            "https://localhost:8080",
		"listening on http://127.0.0.1:0 (picked 55123)":      "http://127.0.0.1:0",
		"no url here": "",
	}
	for line, want := range cases {
		if got := listeningRe.FindString(line); got != want {
			t.Errorf("listeningRe on %q = %q, want %q", line, got, want)
		}
	}
}

func TestWithoutEnv(t *testing.T) {
	in := []string{"PATH=/bin", "XDG_DATA_HOME=/old", "HOME=/h", "XDG_DATA_HOME=/older"}
	out := withoutEnv(in, "XDG_DATA_HOME")
	for _, kv := range out {
		if strings.HasPrefix(kv, "XDG_DATA_HOME=") {
			t.Fatalf("withoutEnv left an XDG_DATA_HOME assignment: %v", out)
		}
	}
	if len(out) != 2 {
		t.Fatalf("withoutEnv dropped the wrong count: %v", out)
	}
}

func TestRealOpencodeDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/abs/data")
	if got, want := realOpencodeDataDir(), filepath.Join("/abs/data", "opencode"); got != want {
		t.Fatalf("with abs XDG_DATA_HOME: got %q want %q", got, want)
	}
	// A relative XDG_DATA_HOME is invalid per XDG and must fall back to ~/.local/share.
	t.Setenv("XDG_DATA_HOME", "rel/data")
	t.Setenv("HOME", "/home/u")
	if got, want := realOpencodeDataDir(), filepath.Join("/home/u", ".local", "share", "opencode"); got != want {
		t.Fatalf("with relative XDG_DATA_HOME: got %q want %q", got, want)
	}
}

// TestSeedAgentAuth copies auth.json from the resolved real data dir into the
// ephemeral data dir, and treats an absent source as a no-op (credential-free
// setups are valid).
func TestSeedAgentAuth(t *testing.T) {
	realHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(realHome, "share"))
	realOC := filepath.Join(realHome, "share", "opencode")
	if err := os.MkdirAll(realOC, 0o700); err != nil {
		t.Fatal(err)
	}
	const cred = `{"github-copilot":{"type":"oauth","access":"tok"}}`
	if err := os.WriteFile(filepath.Join(realOC, "auth.json"), []byte(cred), 0o600); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	if err := seedAgentAuth(dataDir); err != nil {
		t.Fatalf("seedAgentAuth: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dataDir, "opencode", "auth.json"))
	if err != nil {
		t.Fatalf("seeded auth.json missing: %v", err)
	}
	if string(got) != cred {
		t.Fatalf("seeded auth.json mismatch: got %q want %q", got, cred)
	}

	// Missing source auth.json => no-op, no error, no file written.
	if err := os.Remove(filepath.Join(realOC, "auth.json")); err != nil {
		t.Fatal(err)
	}
	dataDir2 := t.TempDir()
	if err := seedAgentAuth(dataDir2); err != nil {
		t.Fatalf("seedAgentAuth with no source should be a no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir2, "opencode", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no seeded auth.json, stat err = %v", err)
	}
}

// TestSpawnEphemeralAgentLifecycle exercises the full spawn/ready/teardown path
// against a fake `opencode` that behaves like `opencode serve`: it starts an HTTP
// listener on an OS-picked port and prints the same "listening on <url>" line the
// real server does. It verifies the parsed Base is reachable, that XDG_DATA_HOME
// is isolated to a temp dir, and that Close stops the process group and removes
// the temp data dir.
func TestSpawnEphemeralAgentLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group teardown is POSIX-specific")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available for the fake opencode server")
	}
	fake := writeFakeOpencode(t, python)

	// Point the "real" data dir (auth source) at a temp tree with a seedable creds file.
	realHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(realHome, "share"))
	realOC := filepath.Join(realHome, "share", "opencode")
	if err := os.MkdirAll(realOC, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realOC, "auth.json"), []byte(`{"p":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ea, err := SpawnEphemeralAgent(ctx, fake, os.Stderr)
	if err != nil {
		t.Fatalf("SpawnEphemeralAgent: %v", err)
	}
	if !strings.HasPrefix(ea.Base, "http://127.0.0.1:") {
		t.Fatalf("unexpected Base %q", ea.Base)
	}
	// The server must actually answer on the parsed Base.
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(ea.Base + "/")
	if err != nil {
		t.Fatalf("GET %s: %v", ea.Base, err)
	}
	resp.Body.Close()
	// The isolated data dir must exist under a temp path (not the real one).
	if ea.dataDir == "" || strings.HasPrefix(ea.dataDir, realHome) {
		t.Fatalf("data dir not isolated to a fresh temp dir: %q", ea.dataDir)
	}
	if _, err := os.Stat(filepath.Join(ea.dataDir, "opencode", "auth.json")); err != nil {
		t.Fatalf("auth.json not seeded into ephemeral data dir: %v", err)
	}

	dataDir := ea.dataDir
	if err := ea.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Temp data dir removed.
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("Close did not remove data dir %s (stat err %v)", dataDir, err)
	}
	// Server no longer answers (process group killed).
	if resp, err := (&http.Client{Timeout: 1 * time.Second}).Get(ea.Base + "/"); err == nil {
		resp.Body.Close()
		t.Fatalf("server at %s still answering after Close", ea.Base)
	}
}

// writeFakeOpencode writes an executable shell script that mimics `opencode serve
// --port 0 --print-logs`: it launches a python HTTP server on an OS-picked port
// and prints the real server's listening line, then blocks until signalled.
func writeFakeOpencode(t *testing.T, python string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\n" +
		"# fake opencode serve for tests: prints a listening line then serves HTTP.\n" +
		"exec " + python + " -c '\n" +
		"import http.server, socketserver, sys\n" +
		"srv = socketserver.TCPServer((\"127.0.0.1\", 0), http.server.BaseHTTPRequestHandler)\n" +
		"port = srv.server_address[1]\n" +
		"print(\"opencode server listening on http://127.0.0.1:%d\" % port, flush=True)\n" +
		"srv.serve_forever()\n" +
		"'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
