// Package checkpolicy is the sandbox/policy for the DoD-verification `Check:`
// command — the shell string a task declares as its machine definition of done,
// which the runner executes at the DONE gate, at pass-start (ground truth), and on
// `beehive task check`. A Check runs against the LIVE environment (it curls real
// endpoints, talks to the cluster, pulls images), so it is attacker- and
// bug-adjacent surface: this package bounds WHAT a check may invoke (a command
// DENYLIST) and WHERE on the filesystem it may reach (its own submodule checkout
// plus the checkouts of submodules it is LINKED to via SUBMODULE-LINKS, derived at
// runtime — never hardcoded — plus operator-declared read paths).
//
// The command layer is a DENYLIST. There is deliberately NO positive allowlist:
// the universe of commands a honeybee may run at all is owned by the agent
// runtime's (opencode's) own permission configuration, and a check may run
// ANYTHING opencode can run EXCEPT the commands on the denylist. Everything
// available to opencode is available to a check — a check never needs a tool
// "installed" or "allowlisted" that a bee can otherwise run. The denylist exists
// to stop a honeybee ABUSING the
// check as a fake definition of done — asserting a source-text fact instead of a
// real effect (`grep`/`find`/`cat`/`test -f` on the checkout, or a no-op like
// `true`/`echo` that always exits 0) — and, because a Check executes via the
// RUNNER (not opencode's own sandboxed bash tool), to keep the highest-risk
// classes off the live host by default even though opencode itself allows them:
// interpreters used to smuggle code (`python -c …`, `bash -c …`, `… | sh`) and
// destructive tools (`rm`/`dd`/`mkfs`). Override the denylist per install via
// config `check_denied_commands` (a non-empty list REPLACES the default, so an
// operator can both narrow it — trusting opencode's own restrictions — and widen
// it). The gate still fails CLOSED on a check it cannot statically analyze (a
// variable/eval/command-substitution used as the command), because it cannot then
// prove no denied command is smuggled.
//
// Two enforcement layers, independent:
//
//   - Command denylist (ALWAYS enforced, host-independent, deterministic): no
//     command word the check invokes may be on the denylist, and the check must be
//     statically analyzable (else it is refused). Everything else opencode permits
//     is admitted.
//   - Filesystem confinement (via bubblewrap when present): the check runs in a
//     namespace whose only writable path is its OWN submodule checkout, whose only
//     extra readable paths are its linked submodule checkouts + declared read paths
//   - the minimal system dirs tools need, with the network shared (checks must
//     reach endpoints/clusters). `check_sandbox: off` disables this layer (the
//     denylist still applies); `check_sandbox: bwrap` + `check_require_sandbox:
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
	SandboxAuto  = "auto"  // bwrap if available, else degrade to denylist-only (default)
	SandboxBwrap = "bwrap" // require bwrap (see RequireSandbox for the missing-bwrap behavior)
	SandboxOff   = "off"   // no filesystem confinement; the command denylist still applies
)

// Policy is the resolved, per-install check policy (assembled from layered config,
// with Default() supplying the low-risk baseline).
type Policy struct {
	// Denied is the set of command basenames a check may NOT invoke. Empty means "use
	// the built-in default set" (DefaultDeniedCommands); a configured list REPLACES
	// the default (the operator states the full set they want), so an install can
	// both widen and narrow it deliberately. Everything opencode permits and that is
	// not on this list is admitted.
	Denied []string
	// Sandbox selects the filesystem-confinement layer (SandboxAuto default).
	Sandbox string
	// RequireSandbox, when true, makes a requested-but-unavailable bwrap a hard error
	// (fail-closed) instead of a degrade-to-denylist-only. Default false.
	RequireSandbox bool
	// ReadPaths are extra absolute host paths bound READ-ONLY into the sandbox — the
	// site-specific credentials/config a check's tools need (a kubeconfig
	// outside the default ~/.kube, a CA bundle, a cloud config). Operator-declared in
	// config; documented in LOCALS.md. The submodule + linked-submodule checkouts are
	// NOT listed here — they are derived at runtime and passed to Argv separately.
	ReadPaths []string
}

// DefaultDeniedCommands is the built-in denylist — the commands a check may NOT
// invoke, in two groups (see the package doc for the rationale). Everything else
// opencode permits is admitted, so real test runners a check legitimately needs
// (`go`, `dotnet`, `pytest`, `nix`, `cargo`, `make`, `npm`, …) pass by default
// with no per-tool config entry required (there is no positive allowlist to add to).
//
// Group 1 — ANTI-ABUSE: source-inspection and no-op tools whose presence signals a
// FAKE definition of done (a task "passes" the moment the code is written, proving
// nothing about the real effect). A check must exercise a real framework, not grep
// the checkout. This is the abuse the operator called out ("grep or find instead of
// a real test").
//
// Group 2 — SAFETY BACKSTOP: because a Check runs via the RUNNER (not opencode's
// own sandboxed bash tool), opencode's `* allow` does not gate it — so the
// highest-risk classes are denied by default even though opencode permits them:
// interpreters/shells that can smuggle arbitrary code (`bash -c`, `python -c`,
// `… | sh`) and destructive filesystem/host tools (`rm`, `dd`, `mkfs`, `shutdown`).
// An install that trusts opencode's own restrictions can narrow this via
// check_denied_commands. Shell SYNTAX a check legitimately uses (pipes, `&&`, `if`,
// `for`, subshells) needs no shell BINARY — the check already runs as `sh -c`.
var DefaultDeniedCommands = []string{
	// --- Group 1: anti-abuse (fake definition of done) ---
	// source-text search masquerading as a test
	"grep", "egrep", "fgrep", "rg", "ag", "ack",
	// filename existence search
	"find", "locate",
	// path/existence inspection
	"ls", "stat", "readlink", "realpath",
	// dump/count file contents
	"cat", "head", "tail", "wc",
	// file/string predicates (`test -f …`, `[ -e … ]`)
	"test", "[",
	// trivial always-passing no-ops (a check that can never fail)
	"true", "false", ":", "echo", "printf", "yes",
	// --- Group 2: safety backstop (check runs via the runner, not opencode) ---
	// shells / interpreters usable to smuggle arbitrary code
	"sh", "bash", "dash", "zsh", "ksh", "csh", "tcsh", "fish", "eval", "exec", "source",
	"python", "python2", "python3", "perl", "ruby", "node", "php", "lua", "tclsh", "awk", "gawk",
	// destructive filesystem / host tools
	"rm", "rmdir", "unlink", "dd", "mkfs", "shred", "truncate", "mv", "chmod", "chown", "chgrp",
	"mount", "umount", "shutdown", "reboot", "halt", "poweroff", "init", "kill", "killall", "pkill",
}

// Default returns the baseline policy: the built-in denylist, auto sandbox, no
// hard-require, no extra read paths.
func Default() Policy {
	return Policy{Sandbox: SandboxAuto}
}

// deniedSet resolves the effective denylist (configured list replaces the
// default when non-empty), keyed by command basename.
func (p Policy) deniedSet() map[string]bool {
	list := p.Denied
	if len(list) == 0 {
		list = DefaultDeniedCommands
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

// Validate enforces the command denylist against a check command. It returns nil
// when NO command word the check invokes is on the denylist; otherwise a
// descriptive error naming the first denied token (or the unparseable construct).
// It is intentionally CONSERVATIVE — a construct it cannot statically resolve to a
// concrete command word (a variable used as a command, an eval, a command
// substitution in command position) is REFUSED, because the gate cannot then prove
// a denied command is not being smuggled (fail closed).
func (p Policy) Validate(check string) error {
	words, err := commandWords(check)
	if err != nil {
		return fmt.Errorf("check command cannot be verified against the denied-commands policy (%w); rewrite it as a plain, statically-analyzable command", err)
	}
	denied := p.deniedSet()
	for _, w := range words {
		base := commandBase(w)
		if denied[base] {
			return fmt.Errorf("check invokes %q which is on the check denied-commands policy (a fake-test tool like grep/find/test, or a smuggling/destructive command); rewrite the check to exercise a REAL test framework, or have the operator adjust check_denied_commands (config.yaml, documented in LOCALS.md)", base)
		}
	}
	return nil
}

// commandBase reduces a command word to the basename used for denylist lookup, so
// `/usr/bin/curl` and `curl` match the same entry. Filesystem confinement (bwrap)
// separately prevents reaching a same-named binary outside the bound dirs.
func commandBase(w string) string {
	if i := strings.LastIndexByte(w, '/'); i >= 0 {
		return w[i+1:]
	}
	return w
}

// deniedSorted returns the effective denylist sorted, for diagnostics/printing.
func (p Policy) deniedSorted() []string {
	m := p.deniedSet()
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
