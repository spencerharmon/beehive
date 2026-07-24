// Package checkpolicy is the sandbox/policy for the DoD-verification `Check:`
// command — the shell string a task declares as its machine definition of done,
// which the runner executes at the DONE gate, at pass-start (ground truth), and on
// `beehive task check`. A Check runs against the LIVE environment (it curls real
// endpoints, talks to the cluster, pulls images), so it is attacker- and
// bug-adjacent surface: this package bounds WHAT a check may invoke (a low-risk
// command allowlist) and WHERE on the filesystem it may reach (its own submodule
// checkout plus the checkouts of submodules it is LINKED to via SUBMODULE-LINKS,
// derived at runtime — never hardcoded — plus operator-declared read paths).
//
// Two enforcement layers, independent:
//
//   - Command allowlist (ALWAYS enforced, host-independent, deterministic): every
//     command word the check invokes must be in the allowlist. This blocks the
//     highest-risk classes outright — arbitrary interpreters used to smuggle code
//     (`python -c …`), `curl … | sh`, `rm`/`dd`, and anything not on the low-risk
//     read/network-tool list. Extend it per install via config `check_allowed_commands`.
//   - Filesystem confinement (via bubblewrap when present): the check runs in a
//     namespace whose only writable path is its OWN submodule checkout, whose only
//     extra readable paths are its linked submodule checkouts + declared read paths
//   - the minimal system dirs tools need, with the network shared (checks must
//     reach endpoints/clusters). `check_sandbox: off` disables this layer (the
//     allowlist still applies); `check_sandbox: bwrap` + `check_require_sandbox:
//     true` makes a missing bwrap a hard failure instead of a degrade.
//
// The package is pure and side-effect-free apart from Argv's `exec.LookPath`
// probe; the swarm/CLI layers own resolving the submodule + linked paths and
// running the returned argv through the existing verify seam.
package checkpolicy

import (
	"fmt"
	"sort"
	"strings"
)

// Sandbox modes for the filesystem-confinement layer.
const (
	SandboxAuto  = "auto"  // bwrap if available, else degrade to allowlist-only (default)
	SandboxBwrap = "bwrap" // require bwrap (see RequireSandbox for the missing-bwrap behavior)
	SandboxOff   = "off"   // no filesystem confinement; the command allowlist still applies
)

// Policy is the resolved, per-install check policy (assembled from layered config,
// with Default() supplying the low-risk baseline).
type Policy struct {
	// Allowed is the set of command basenames a check may invoke. Empty means "use
	// the built-in default set" (DefaultAllowedCommands); a configured list REPLACES
	// the default (the operator states the full set they want), so an install can
	// both widen and narrow it deliberately.
	Allowed []string
	// Sandbox selects the filesystem-confinement layer (SandboxAuto default).
	Sandbox string
	// RequireSandbox, when true, makes a requested-but-unavailable bwrap a hard error
	// (fail-closed) instead of a degrade-to-allowlist-only. Default false.
	RequireSandbox bool
	// ReadPaths are extra absolute host paths bound READ-ONLY into the sandbox — the
	// site-specific credentials/config a check's allowlisted tools need (a kubeconfig
	// outside the default ~/.kube, a CA bundle, a cloud config). Operator-declared in
	// config; documented in LOCALS.md. The submodule + linked-submodule checkouts are
	// NOT listed here — they are derived at runtime and passed to Argv separately.
	ReadPaths []string
}

// DefaultAllowedCommands is the built-in low-risk allowlist: read-only inspection,
// text processing, hashing, DNS, and the network/cluster clients a real DoD check
// needs (curl/kubectl/helm). It deliberately EXCLUDES general-purpose shells and
// interpreters as a COMMAND word (sh/bash/python/perl/ruby/node) so a check cannot
// re-enter a shell to smuggle code (`… | sh`, `bash -c …`) and destructive tools
// (rm/dd/mkfs/mv). Shell SYNTAX a check legitimately uses (pipes, `&&`, `if`, `for`,
// `[ … ]`/`test`, subshells) needs no shell BINARY — the check itself already runs
// as `sh -c <check>`. A check that genuinely needs one of the excluded commands is a
// smell the operator opts into explicitly via config.
var DefaultAllowedCommands = []string{
	// control / trivial builtins available as real binaries
	"test", "[", "true", "false", "echo", "printf", "env", "timeout", "sleep", "expr", "seq",
	// filesystem read / inspection
	"cat", "ls", "stat", "readlink", "realpath", "basename", "dirname", "find", "head", "tail", "wc",
	// text processing
	"grep", "egrep", "fgrep", "cut", "tr", "sort", "uniq", "awk", "sed", "jq", "yq", "column", "tee", "xargs",
	// hashing / encoding
	"sha256sum", "sha512sum", "md5sum", "base64", "cmp", "diff",
	// version control (read a submodule/linked checkout)
	"git",
	// network / DNS / cluster clients (the point of a live DoD check)
	"curl", "wget", "dig", "nslookup", "host", "nc", "ping", "getent", "ss",
	"kubectl", "helm", "flux", "skopeo", "crane", "oras", "docker", "podman",
	"date",
}

// Default returns the baseline policy: the built-in allowlist, auto sandbox, no
// hard-require, no extra read paths.
func Default() Policy {
	return Policy{Sandbox: SandboxAuto}
}

// allowedSet resolves the effective allowlist (configured list replaces the
// default when non-empty), keyed by command basename.
func (p Policy) allowedSet() map[string]bool {
	list := p.Allowed
	if len(list) == 0 {
		list = DefaultAllowedCommands
	}
	m := make(map[string]bool, len(list))
	for _, c := range list {
		c = strings.TrimSpace(c)
		if c != "" {
			m[c] = true
		}
	}
	return m
}

// Validate enforces the command allowlist against a check command. It returns nil
// when every command word the check invokes is allowlisted; otherwise a descriptive
// error naming the first offending token (or the unparseable construct). It is
// intentionally CONSERVATIVE — a construct it cannot statically resolve to a
// concrete command word (a variable used as a command, an eval) is REFUSED, because
// this is a security gate and an un-vetted command must never pass by default.
func (p Policy) Validate(check string) error {
	words, err := commandWords(check)
	if err != nil {
		return fmt.Errorf("check command cannot be verified against the allowlist (%w); rewrite it with plain allowlisted commands", err)
	}
	allowed := p.allowedSet()
	for _, w := range words {
		base := commandBase(w)
		if !allowed[base] {
			return fmt.Errorf("check invokes %q which is not in the allowed-commands policy; use an allowlisted low-risk tool or have the operator add it to check_allowed_commands (config.yaml, documented in LOCALS.md)", base)
		}
	}
	return nil
}

// commandBase reduces a command word to the basename used for allowlist lookup, so
// `/usr/bin/curl` and `curl` match the same entry. Filesystem confinement (bwrap)
// separately prevents reaching a same-named binary outside the bound dirs.
func commandBase(w string) string {
	if i := strings.LastIndexByte(w, '/'); i >= 0 {
		return w[i+1:]
	}
	return w
}

// AllowsCommandOverride reports whether the policy replaced the default allowlist.
func (p Policy) allowedSorted() []string {
	m := p.allowedSet()
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
