package checkpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// bwrapProbe is the bubblewrap lookup seam (overridable in tests). It returns the
// resolved bwrap path and whether it is available.
var bwrapProbe = func() (string, bool) {
	p, err := exec.LookPath("bwrap")
	if err != nil {
		return "", false
	}
	return p, true
}

// systemReadOnlyDirs are the host directories the allowlisted tools resolve against
// (binaries, shared libs, CA certs / resolv.conf / passwd under /etc). They are
// bound READ-ONLY so a check can run curl/kubectl/git but cannot mutate the host.
// Missing entries are skipped (bind-try semantics) so this works across usrmerge
// and split layouts.
var systemReadOnlyDirs = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc"}

// Plan describes how a check will be executed: the argv to run, whether filesystem
// confinement is active, and a human note when the requested sandbox degraded.
type Plan struct {
	Name      string   // argv[0] to exec (bwrap, or the shell when unsandboxed)
	Args      []string // remaining argv
	Sandboxed bool     // true when bubblewrap confinement is in effect
	Note      string   // non-empty when the requested sandbox degraded (surface it)
}

// Argv resolves how to run `check` under this policy. rwPaths are the writable
// paths (the task's own submodule checkout AND its git-common-dir, so `git` checks
// resolve their object store); roPaths are additional readable paths (its linked
// submodule checkouts + Policy.ReadPaths), all absolute. cwd is the working
// directory the check runs in (normally the checkout). It returns a Plan, or an
// error only when RequireSandbox is set and bubblewrap is unavailable
// (fail-closed). The command allowlist is enforced separately via Validate — Argv
// assumes the check has already passed it.
func (p Policy) Argv(check, cwd string, rwPaths []string, roPaths []string) (Plan, error) {
	unsandboxed := Plan{Name: "sh", Args: []string{"-c", check}, Sandboxed: false}

	if p.Sandbox == SandboxOff {
		return unsandboxed, nil
	}
	bwrap, ok := bwrapProbe()
	if !ok {
		if p.Sandbox == SandboxBwrap && p.RequireSandbox {
			return Plan{}, errSandboxUnavailable
		}
		unsandboxed.Note = "bubblewrap not found; check ran WITHOUT filesystem confinement (command allowlist still enforced). Install bwrap or set check_sandbox: off to silence."
		return unsandboxed, nil
	}

	args := []string{
		"--die-with-parent",
		"--unshare-user", "--unshare-ipc", "--unshare-pid", "--unshare-uts", "--unshare-cgroup-try",
		// A live DoD check must reach real endpoints/clusters — keep the network.
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}
	for _, d := range systemReadOnlyDirs {
		if pathExists(d) {
			args = append(args, "--ro-bind", d, d)
		}
	}
	// The task's own submodule checkout + git-common-dir: the writable paths.
	rw := dedupePaths(rwPaths, "")
	for _, w := range rw {
		if pathExists(w) {
			args = append(args, "--bind", w, w)
		}
	}
	// Linked submodule checkouts + operator-declared read paths, read-only, deduped
	// against the writable set.
	for _, ro := range dedupePaths(roPaths, rw...) {
		if pathExists(ro) {
			args = append(args, "--ro-bind", ro, ro)
		}
	}
	if cwd != "" {
		args = append(args, "--chdir", cwd)
	}
	args = append(args, "sh", "-c", check)
	return Plan{Name: bwrap, Args: args, Sandboxed: true}, nil
}

// errSandboxUnavailable is returned by Argv when bwrap is required but missing.
var errSandboxUnavailable = sandboxUnavailableError{}

type sandboxUnavailableError struct{}

func (sandboxUnavailableError) Error() string {
	return "check_sandbox requires bubblewrap but `bwrap` was not found on PATH, and check_require_sandbox is set: refusing to run the DoD check unconfined"
}

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Lstat(p)
	return err == nil
}

// dedupePaths cleans, absolute-filters, drops any excluded path, and de-duplicates
// the path list for stable, minimal bind args.
func dedupePaths(paths []string, exclude ...string) []string {
	seen := map[string]bool{}
	for _, e := range exclude {
		if e != "" {
			seen[filepath.Clean(e)] = true
		}
	}
	var out []string
	for _, p := range paths {
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		c := filepath.Clean(p)
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
