// Package checks parses and validates CHECKS.md: the per-submodule registry of
// APPROVED definition-of-done check frameworks. It is the whitelist half of the
// DoD contract (docs/dod-verification-spec.md, docs/checks-framework-registry.md).
//
// A task's `Check:` / `Verify-After-Merge:` command is the machine definition of
// done the runner gates on (internal/plan, internal/swarm/verify.go). Left
// unconstrained, an agent can satisfy the gate with a check that only asserts a
// SOURCE-TEXT fact — `grep -q SomeSymbol repo/...` / `test -f repo/...` — which
// passes the moment the code is written and proves NOTHING about the real effect.
// CHECKS.md closes that hole: every non-DONE task's check MUST MATCH one of the
// APPROVED stubs registered here (a real test runner, a compile, a build-pipeline
// status query, an integration/e2e probe), so a bare source-grep — matching no
// framework stub — is refused by the linter, the runner handoff gate, and the
// reconcile/bootstrap completion gate alike.
//
// CHECKS.md is OWNED by honeybees and operator-directed agents: as a target's
// testing frameworks evolve they add or refine a stub here (a hive-layer file at
// submodules/<name>/CHECKS.md, alongside PLAN.md), then point tasks' checks at it.
// This package is pure and deterministic (no LLM, no side effects apart from
// Load's file read), mirroring internal/plan.
//
// CHECKS.md format (line-oriented, stable round-trip):
//
//	# Checks — <submodule>
//	<free-form header prose>
//
//	## <stub-id> <!-- category=<category> -->
//	<one-or-more description lines>
//	Match: <RE2 regexp the whole check command string must match>
//	Example: <a concrete check command that matches>
//
// The first H2 begins the stub list; prose before it is the header. Each stub
// carries a `category=` metadata token (unit, compile, lint, integration, e2e,
// pipeline, deploy, endpoint, artifact — see Categories) and a required `Match:`
// RE2 regexp. `Example:` is optional but recommended. A stub whose `Match:` is
// absent, uncompilable, or matches the empty string (e.g. `.*`, which would
// approve ANY command and defeat the whitelist) is a parse error.
package checks

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ChecksFile is the registry filename, alongside PLAN.md in the beehive layer.
const ChecksFile = "CHECKS.md"

const (
	categoryPrefix = "category="
	matchPrefix    = "Match:"
	examplePrefix  = "Example:"
)

// Categories is the recommended stub-category vocabulary. It is NOT closed —
// Parse accepts any non-empty category so the registry can grow with new kinds of
// framework — but Warnings() flags a category outside this set so an operator/
// reviewer notices a typo or an ad-hoc value. Each names a class of REAL
// verification, never a source-text assertion.
var Categories = []string{
	"unit",        // a unit-test runner (go test, pytest, cargo test, ...)
	"compile",     // the target builds/compiles (go build, tsc, cargo build, ...)
	"lint",        // a static-analysis / vet / formatter gate (go vet, golangci-lint, ...)
	"integration", // an integration-test suite exercising real components together
	"e2e",         // an end-to-end suite against a running system
	"pipeline",    // a CI/CD or GitOps build-pipeline status query (flux, kubectl, gh run, ...)
	"deploy",      // a deployment/rollout status (kubectl rollout status, helm status, ...)
	"endpoint",    // a live endpoint probe (curl + assert body/status)
	"artifact",    // a published-artifact existence/digest check (skopeo, crane, oras, ...)
}

// ErrNoChecksFile is returned by Load when CHECKS.md does not exist. Callers
// distinguish it (errors.Is) from a parse/read failure: a MISSING registry is a
// distinct, actionable condition (the submodule has declared no approved
// frameworks yet) the linter/gate report with a create-the-file remedy.
var ErrNoChecksFile = errors.New("no CHECKS.md")

// Stub is one approved check framework.
type Stub struct {
	ID          string
	Category    string
	Description string         // free prose (may be empty)
	MatchRaw    string         // the raw RE2 source, preserved for round-trip
	Match       *regexp.Regexp // compiled MatchRaw (never nil after Parse)
	Example     string         // optional concrete example command
}

// Checks is a parsed CHECKS.md.
type Checks struct {
	Header []string // prose before the first stub, verbatim
	Stubs  []Stub
}

var stubHeaderRe = regexp.MustCompile(`^##\s+(\S+)\s*(?:<!--\s*(.*?)\s*-->)?\s*$`)

// Parse reads a CHECKS.md document into a Checks. It fails on a duplicate stub id,
// a stub missing a category, a stub missing or with an uncompilable `Match:`, or a
// `Match:` that matches the empty string (which would approve every command).
func Parse(s string) (*Checks, error) {
	c := &Checks{}
	lines := strings.Split(s, "\n")
	// Trim one trailing empty element from a final newline so round-trips are stable.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	// Split into the header prose and per-stub line blocks.
	firstStub := -1
	for i, ln := range lines {
		if stubHeaderRe.MatchString(ln) {
			firstStub = i
			break
		}
	}
	if firstStub == -1 {
		c.Header = append(c.Header, lines...)
		return c, nil
	}
	c.Header = append(c.Header, lines[:firstStub]...)

	seen := map[string]bool{}
	i := firstStub
	for i < len(lines) {
		m := stubHeaderRe.FindStringSubmatch(lines[i])
		if m == nil {
			// A stray non-header line between stubs (should not happen after the
			// header split) — attach to the previous stub's description tolerantly.
			return nil, fmt.Errorf("checks: unexpected line %d outside a stub: %q", i+1, lines[i])
		}
		id := m[1]
		meta := m[2]
		if seen[id] {
			return nil, fmt.Errorf("checks: duplicate stub id %q", id)
		}
		seen[id] = true
		// Collect this stub's body: lines until the next stub header or EOF.
		j := i + 1
		for j < len(lines) && !stubHeaderRe.MatchString(lines[j]) {
			j++
		}
		st, err := parseStub(id, meta, lines[i+1:j])
		if err != nil {
			return nil, err
		}
		c.Stubs = append(c.Stubs, st)
		i = j
	}
	return c, nil
}

func parseStub(id, meta string, body []string) (Stub, error) {
	st := Stub{ID: id}
	// category= from the metadata comment.
	for _, tok := range strings.Fields(meta) {
		if v, ok := strings.CutPrefix(tok, categoryPrefix); ok {
			st.Category = v
		}
	}
	if strings.TrimSpace(st.Category) == "" {
		return Stub{}, fmt.Errorf("checks: stub %q missing a category=<kind> token in its `## %s <!-- category=... -->` header", id, id)
	}
	var descLines []string
	for _, ln := range body {
		trimmed := strings.TrimSpace(ln)
		if v, ok := strings.CutPrefix(trimmed, matchPrefix); ok {
			st.MatchRaw = strings.TrimSpace(v)
			continue
		}
		if v, ok := strings.CutPrefix(trimmed, examplePrefix); ok {
			st.Example = strings.TrimSpace(v)
			continue
		}
		descLines = append(descLines, ln)
	}
	st.Description = strings.TrimSpace(strings.Join(descLines, "\n"))
	if st.MatchRaw == "" {
		return Stub{}, fmt.Errorf("checks: stub %q has no `Match:` regexp — a stub must declare the RE2 pattern an approved check command matches", id)
	}
	re, err := regexp.Compile(st.MatchRaw)
	if err != nil {
		return Stub{}, fmt.Errorf("checks: stub %q has an invalid `Match:` regexp %q: %w", id, st.MatchRaw, err)
	}
	if re.MatchString("") {
		return Stub{}, fmt.Errorf("checks: stub %q `Match:` %q matches the empty string — it would approve EVERY command (including a bare grep); tighten it to require a concrete framework invocation", id, st.MatchRaw)
	}
	st.Match = re
	return st, nil
}

// String re-renders the registry (Parse -> String -> Parse is stable).
func (c *Checks) String() string {
	var b strings.Builder
	for _, h := range c.Header {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	for _, st := range c.Stubs {
		fmt.Fprintf(&b, "## %s <!-- category=%s -->\n", st.ID, st.Category)
		if st.Description != "" {
			b.WriteString(st.Description)
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s %s\n", matchPrefix, st.MatchRaw)
		if st.Example != "" {
			fmt.Fprintf(&b, "%s %s\n", examplePrefix, st.Example)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// Match returns the first approved stub whose Match regexp matches cmd (trimmed),
// or nil,false when none does.
func (c *Checks) Match(cmd string) (*Stub, bool) {
	cmd = strings.TrimSpace(cmd)
	for i := range c.Stubs {
		if c.Stubs[i].Match != nil && c.Stubs[i].Match.MatchString(cmd) {
			return &c.Stubs[i], true
		}
	}
	return nil, false
}

// StubIDs returns the registered stub ids in order (for error messages).
func (c *Checks) StubIDs() []string {
	out := make([]string, 0, len(c.Stubs))
	for _, st := range c.Stubs {
		out = append(out, st.ID)
	}
	return out
}

// Unapproved reports why cmd is not an approved check, or "" when it matches an
// approved stub (or is empty — no command to constrain). A nil/empty registry
// makes every non-empty command unapproved.
func (c *Checks) Unapproved(cmd string) string {
	if strings.TrimSpace(cmd) == "" {
		return ""
	}
	if c != nil {
		if _, ok := c.Match(cmd); ok {
			return ""
		}
	}
	ids := "(none defined)"
	if c != nil && len(c.Stubs) > 0 {
		ids = strings.Join(c.StubIDs(), ", ")
	}
	return fmt.Sprintf("matches no approved check framework in %s (approved stubs: %s) — a check must invoke a real test framework, not grep/test-f the source; add or point at a stub", ChecksFile, ids)
}

// Warnings returns non-fatal registry-quality issues (a stub whose category is
// outside the recommended Categories vocabulary). The linter surfaces these.
func (c *Checks) Warnings() []string {
	known := map[string]bool{}
	for _, cat := range Categories {
		known[cat] = true
	}
	var out []string
	for _, st := range c.Stubs {
		if !known[st.Category] {
			out = append(out, fmt.Sprintf("stub %q has category %q outside the recommended set {%s}", st.ID, st.Category, strings.Join(Categories, ", ")))
		}
	}
	return out
}
