// Per-pass ephemeral opencode server lifecycle. A honeybee pass with
// agent_ephemeral=true spawns its OWN `opencode serve` here — on an OS-picked
// port, backed by a fresh temp data dir seeded only with the provider
// credentials — uses it for the pass, and tears it down (process group + temp
// dir) on exit. The OS reclaims all of that pass's agent heap and its on-disk
// session store (SQLite DB + git snapshots) at teardown, so nothing accumulates
// across the thousands of sessions a busy swarm opens over days. This is the fix
// for the 2026-07-10 global OOM, where one long-lived shared server grew to
// ~40 GB RSS (its per-session state is never released) and the kernel killed it
// plus co-tenant workloads.
package swarm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// ephemeralStartTimeout bounds how long we wait for a freshly spawned
// `opencode serve` to print its listening URL. A cold start loads the runtime and
// any plugins, so it is generous; a server that has not announced by then is
// treated as failed to launch and torn down.
const ephemeralStartTimeout = 90 * time.Second

// listeningRe extracts the base URL from opencode's startup line, e.g.
// "opencode server listening on http://127.0.0.1:41273".
var listeningRe = regexp.MustCompile(`https?://[0-9A-Za-z_.:-]+`)

// EphemeralAgent is a spawned, pass-private opencode server. Base is the URL to
// point a swarm.Opencode client at; Close tears the server down.
type EphemeralAgent struct {
	Base    string
	cmd     *exec.Cmd
	dataDir string    // temp XDG_DATA_HOME whose /opencode subtree the server writes
	log     io.Writer // pass-through for the server's post-startup output
}

// SpawnEphemeralAgent launches a dedicated `agentCmd serve` for one honeybee pass
// and returns it once the server has announced its listening URL. It:
//
//   - creates a fresh temp dir and points the child's XDG_DATA_HOME at it, so the
//     server's data (SQLite session DB, working-dir git snapshots, logs) lands
//     there and is discarded wholesale at Close — no shared, ever-growing store;
//   - seeds that temp data dir with the real install's opencode auth.json (the
//     provider credentials live in the DATA dir, not the config dir), so the
//     isolated server authenticates to the same provider the shared one would.
//     The provider/model DEFINITIONS live under XDG_CONFIG_HOME, which is left
//     untouched so they still resolve;
//   - starts the server in its own process group with --port 0 (OS-picked free
//     port) and --print-logs, parses the port from the startup line, and confirms
//     the HTTP endpoint answers before returning.
//
// On any failure the partially-started process and temp dir are cleaned up before
// returning, so a caller never leaks a server or a directory on error. log, when
// non-nil, receives the server's post-startup output (drained continuously so the
// child never blocks on a full pipe).
func SpawnEphemeralAgent(ctx context.Context, agentCmd string, log io.Writer) (*EphemeralAgent, error) {
	if strings.TrimSpace(agentCmd) == "" {
		agentCmd = "opencode"
	}
	dataDir, err := os.MkdirTemp("", "beehive-opencode-")
	if err != nil {
		return nil, fmt.Errorf("ephemeral agent: create temp data dir: %w", err)
	}
	if err := seedAgentAuth(dataDir); err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("ephemeral agent: %w", err)
	}

	cmd := exec.Command(agentCmd, "serve", "--hostname", "127.0.0.1", "--port", "0", "--print-logs")
	// Isolate the data dir; inherit everything else (PATH, HOME, XDG_CONFIG_HOME
	// with the provider/model config, and the layered BuildEnv the runner exports).
	cmd.Env = append(withoutEnv(os.Environ(), "XDG_DATA_HOME"), "XDG_DATA_HOME="+dataDir)
	// Own process group so Close can signal the server AND any children it forks
	// (LSP/provider helpers) as a unit.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	pr, pw, err := os.Pipe()
	if err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("ephemeral agent: pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("ephemeral agent: start %q: %w", agentCmd, err)
	}
	// The parent must drop its write end so that when the child exits, the read
	// end sees EOF (otherwise the startup scan could block forever).
	pw.Close()

	ea := &EphemeralAgent{cmd: cmd, dataDir: dataDir, log: log}
	base, err := ea.awaitListening(ctx, pr)
	if err != nil {
		pr.Close()
		ea.Close()
		return nil, err
	}
	ea.Base = base
	if err := waitHTTPReady(ctx, base); err != nil {
		pr.Close()
		ea.Close()
		return nil, fmt.Errorf("ephemeral agent: %s never became ready: %w", base, err)
	}
	return ea, nil
}

// awaitListening scans the merged server output for its "listening on <url>" line,
// then hands the pipe to a background drainer so the server never blocks writing
// logs. It fails if the process exits (pipe EOF), the deadline passes, or ctx is
// cancelled first.
func (ea *EphemeralAgent) awaitListening(ctx context.Context, pr *os.File) (string, error) {
	type res struct {
		url string
		err error
	}
	done := make(chan res, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if ea.log != nil {
				fmt.Fprintln(ea.log, line)
			}
			if strings.Contains(line, "listening on") {
				if u := listeningRe.FindString(line); u != "" {
					done <- res{url: strings.TrimRight(u, "/")}
					// Keep draining the pipe for the rest of the pass so the
					// server never stalls on a full pipe buffer.
					if ea.log != nil {
						_, _ = io.Copy(ea.log, pr)
					} else {
						_, _ = io.Copy(io.Discard, pr)
					}
					return
				}
			}
		}
		// EOF or scan error before we ever saw the listening line.
		if err := sc.Err(); err != nil {
			done <- res{err: fmt.Errorf("ephemeral agent: read server output: %w", err)}
			return
		}
		done <- res{err: fmt.Errorf("ephemeral agent: server exited before announcing a listening address")}
	}()

	timer := time.NewTimer(ephemeralStartTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.url, r.err
	case <-timer.C:
		return "", fmt.Errorf("ephemeral agent: server did not announce a listening address within %s", ephemeralStartTimeout)
	case <-ctx.Done():
		return "", fmt.Errorf("ephemeral agent: aborted while starting server: %w", ctx.Err())
	}
}

// Close tears the server down: SIGINT the process group (opencode's graceful
// stop signal), give it a moment, then SIGKILL any survivor, and finally remove
// the temp data dir so the pass leaves no on-disk agent state behind. Safe to
// call on a partially-started agent and idempotent enough for a deferred call.
func (ea *EphemeralAgent) Close() error {
	if ea == nil {
		return nil
	}
	if ea.cmd != nil && ea.cmd.Process != nil {
		pgid := ea.cmd.Process.Pid
		// Negative pid signals the whole process group (Setpgid above made the
		// child a group leader, so its pid == its pgid).
		_ = syscall.Kill(-pgid, syscall.SIGINT)
		exited := make(chan struct{})
		go func() { _, _ = ea.cmd.Process.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(8 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-exited
		}
	}
	if ea.dataDir != "" {
		return os.RemoveAll(ea.dataDir)
	}
	return nil
}

// seedAgentAuth copies the real install's opencode auth.json into the ephemeral
// data dir so the isolated server has the same provider credentials. opencode
// keeps credentials in the DATA dir ($XDG_DATA_HOME/opencode/auth.json), so a
// bare fresh data dir would have no way to authenticate. A missing source
// auth.json is NOT fatal: a credential-free setup (e.g. a purely local model over
// an unauthenticated endpoint) legitimately has none, so we simply skip seeding
// and let the server start without it.
func seedAgentAuth(dataDir string) error {
	src := filepath.Join(realOpencodeDataDir(), "auth.json")
	b, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no credentials to seed; not an error
		}
		return fmt.Errorf("read %s: %w", src, err)
	}
	dstDir := filepath.Join(dataDir, "opencode")
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dstDir, err)
	}
	dst := filepath.Join(dstDir, "auth.json")
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// realOpencodeDataDir resolves the install's opencode data directory the same way
// opencode itself does: $XDG_DATA_HOME/opencode when XDG_DATA_HOME is an absolute
// path, else $HOME/.local/share/opencode.
func realOpencodeDataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "opencode")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode")
}

// withoutEnv returns env with any assignment of key removed, so a caller can set
// exactly one authoritative value for it afterward.
func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// waitHTTPReady polls the server's base URL until an HTTP request completes with
// ANY status (the port is accepting and serving), bounded by a short deadline and
// ctx. It guards the gap between the process printing its listening line and the
// listener actually accepting connections.
func waitHTTPReady(ctx context.Context, base string) error {
	deadline := time.Now().Add(15 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil // any HTTP response means the server is accepting requests
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(150 * time.Millisecond)
	}
}
