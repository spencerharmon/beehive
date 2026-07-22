package swarm

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// The host build/test environment (static link + tmp/cache redirected off a
// broken /tmp or cgo linker — the fix LOCALS.md documents) is OWNED by the runner
// via config.Config.BuildEnv, resolved once and reached through two levers with
// two different consumers. This split is deliberate: opencode runs as a SEPARATE
// server process (the honeybee talks to it over HTTP, see opencode.go), so the
// agent's own bash/shell tool runs in opencode's process and does NOT inherit the
// honeybee process env.
//
//  1. exportBuildEnv applies BuildEnv to the HONEYBEE's own process env at agent
//     spawn, so any build/test subprocess the honeybee itself spawns inherits it.
//     It does NOT reach opencode's sibling bash tool.
//  2. buildEnvPreamble states the mandated invocation ONCE in the injected prompt
//     so the AGENT's own commands (the opencode bash tool) stop re-deriving it.
//
// Both read the one BuildEnv map, so the exported env and the stated line can
// never drift. Both are inert (no export, byte-identical preamble) when BuildEnv
// is empty, so a normal host is unaffected. BuildEnv is a generic KEY=VALUE map:
// it carries whatever host environment the target's build/test tooling needs and
// assumes NO specific language — the runner passes it through, it does not know or
// judge the submodule's toolchain (see docs/runner-protocol-vs-correctness.md).

// exportBuildEnv applies the resolved BuildEnv to the honeybee process
// environment so build/test subprocesses the honeybee spawns inherit the host
// settings. ExportEnv is the injectable seam: nil runs the real os.Setenv loop
// (over sorted keys, for deterministic behavior); tests set it to capture the map
// without touching the real process env. No-op when BuildEnv is empty.
func (r *Runner) exportBuildEnv() {
	if len(r.BuildEnv) == 0 {
		return
	}
	if r.ExportEnv != nil {
		r.ExportEnv(r.BuildEnv)
		return
	}
	for _, k := range sortedKeys(r.BuildEnv) {
		_ = os.Setenv(k, r.BuildEnv[k])
	}
}

// buildEnvPreamble renders the told-once build-env block: the exact `KEY=VALUE …`
// prefix (keys SORTED so the line is deterministic and never drifts) the agent
// must put in front of every build/test command it runs. It deliberately does NOT
// claim the vars are already set in the agent's shell — they are not (opencode is
// a sibling process) — it instructs the agent to PREFIX its commands. The block is
// language-neutral: it states the host environment, never a toolchain. Returns ""
// when unconfigured so the default injected preamble is byte-identical.
func buildEnvPreamble(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for i, k := range sortedKeys(env) {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
	}
	prefix := b.String()
	return fmt.Sprintf(
		"# Build/test environment (host-mandated — the runner owns this; do NOT re-derive it)\n"+
			"This host requires specific environment settings for build/test commands. opencode's "+
			"shell does NOT inherit them, so PREFIX every build or test command you run with these "+
			"exact settings:\n"+
			"    %[1]s\n"+
			"e.g. `%[1]s <your build/test command>`. Use this instead of a bare invocation; it is "+
			"the mandated setup for this host (do not spend turns rediscovering it).\n\n",
		prefix)
}

// sortedKeys returns env's keys in lexical order, so both the exported os.Setenv
// loop and the rendered preamble line are deterministic (drift-free) regardless
// of Go's randomized map iteration.
func sortedKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
