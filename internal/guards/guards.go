// Package guards parses and validates GUARDS.md: a per-submodule registry of
// diff-scoped MUTATION GUARDS. Where internal/checks gates a task's definition of
// DONE, a guard gates the CHANGE a task is allowed to make — it answers, as an
// agent-authored command, "given this proposed diff and who is making it, is it
// allowed to land right now?".
//
// Motivation (observed 2026-08): a honeybee edited the ACTIVE blue/green color's
// deploy manifest — upgrading prod in lockstep with dev — because it resolved
// "which color is active" from a stale prose comment. The lesson generalizes: some
// diffs must be REFUSED based on LIVE release state the runner cannot itself know,
// and what counts as "protected right now" differs per release strategy (blue/green,
// canary, rolling, waves). So the runner must NOT enumerate strategies. It encodes
// exactly one sentence — "a proposed mutation, judged from a trusted baseline against
// live release-state by strategy-owned policy code" — and nothing strategy-specific.
// Each strategy is a GUARDS.md stub + a guards/ script that fills three slots:
// the protected path-glob, the live authority to read, and the forbidden condition.
//
// The runner is a deterministic executor + whitelist enforcer, exactly as with
// checks: at a gated handoff it computes the committed bee-branch-vs-merge-base
// diff, fires every guard whose `Protects:` glob intersects a changed file, and
// enforces the guard's exit code (0 = allow, non-zero = refuse + fix-forward). The
// guard COMMAND and GUARDS.md are materialized from the MERGE-BASE (the reviewed
// baseline the pass forked from), never the bee branch, so a bee's edit to guard
// code is inert for its own pass — you never grade your own exam with a key you
// edited mid-exam.
//
// GUARDS.md lives IN the submodule repo (versioned with the code it protects), at
// the repo root, so the guard, the protected files, the diff, and the tamper anchor
// are all one git history.
//
// Format (line-oriented, stable round-trip; mirrors internal/checks):
//
//	# Guards — <submodule>
//	<free-form header prose>
//
//	## <stub-id> <!-- ... -->
//	<one-or-more description lines>
//	Protects: <glob>[, <glob> ...]
//	Command: <shell command, run from the baseline, exit!=0 = refuse>
//
// Globs match repo-relative paths: `*`/`?` stay within a path segment, `**` spans
// segments (so `guards/**` matches every file under guards/). A stub missing a
// Protects glob or a Command, or whose Command is a trivially-passing no-op
// (`true`/`:`/`echo`/`exit 0` — it would approve every diff and defeat the guard),
// is a parse error — the direct analogue of checks refusing an empty-matching regex.
package guards

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// GuardsFile is the registry filename, at the submodule repo root.
const GuardsFile = "GUARDS.md"

const (
	protectsPrefix = "Protects:"
	commandPrefix  = "Command:"
)

// ErrNoGuardsFile is returned by Load when GUARDS.md does not exist. A MISSING
// registry is a distinct, benign condition (the submodule declares no guards) —
// callers treat it as "no guards, zero overhead", NOT an error. Mirrors
// checks.ErrNoChecksFile.
var ErrNoGuardsFile = errors.New("no GUARDS.md")

// trivialCommandBases are command basenames that always succeed regardless of the
// diff — a guard built on one would approve every change. Refused at parse time.
var trivialCommandBases = map[string]bool{
	"true": true, ":": true, "echo": true, "printf": true, "cat": true, "test": true,
}

var trivialExitRe = regexp.MustCompile(`^exit\s+0$`)

// Stub is one mutation guard.
type Stub struct {
	ID          string
	Description string   // free prose (may be empty)
	ProtectsRaw []string // raw globs, preserved for round-trip
	protects    []*regexp.Regexp
	Command     string // the guard command, run from the baseline; exit!=0 = refuse
}

// Guards is a parsed GUARDS.md.
type Guards struct {
	Header []string // prose before the first stub, verbatim
	Stubs  []Stub
}

var stubHeaderRe = regexp.MustCompile(`^##\s+(\S+)\s*(?:<!--\s*(.*?)\s*-->)?\s*$`)

// Parse reads a GUARDS.md document into a Guards. It fails on a duplicate stub id, a
// stub missing a Protects glob or with an uncompilable glob, a stub missing a
// Command, or a Command that is a trivially-passing no-op.
func Parse(s string) (*Guards, error) {
	g := &Guards{}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	firstStub := -1
	for i, ln := range lines {
		if stubHeaderRe.MatchString(ln) {
			firstStub = i
			break
		}
	}
	if firstStub == -1 {
		g.Header = append(g.Header, lines...)
		return g, nil
	}
	g.Header = append(g.Header, lines[:firstStub]...)

	seen := map[string]bool{}
	i := firstStub
	for i < len(lines) {
		m := stubHeaderRe.FindStringSubmatch(lines[i])
		if m == nil {
			return nil, fmt.Errorf("guards: unexpected line %d outside a stub: %q", i+1, lines[i])
		}
		id := m[1]
		if seen[id] {
			return nil, fmt.Errorf("guards: duplicate stub id %q", id)
		}
		seen[id] = true
		j := i + 1
		for j < len(lines) && !stubHeaderRe.MatchString(lines[j]) {
			j++
		}
		st, err := parseStub(id, lines[i+1:j])
		if err != nil {
			return nil, err
		}
		g.Stubs = append(g.Stubs, st)
		i = j
	}
	return g, nil
}

func parseStub(id string, body []string) (Stub, error) {
	st := Stub{ID: id}
	var descLines []string
	for _, ln := range body {
		trimmed := strings.TrimSpace(ln)
		if v, ok := strings.CutPrefix(trimmed, protectsPrefix); ok {
			for _, part := range strings.Split(v, ",") {
				p := strings.TrimSpace(part)
				if p != "" {
					st.ProtectsRaw = append(st.ProtectsRaw, p)
				}
			}
			continue
		}
		if v, ok := strings.CutPrefix(trimmed, commandPrefix); ok {
			st.Command = strings.TrimSpace(v)
			continue
		}
		descLines = append(descLines, ln)
	}
	st.Description = strings.TrimSpace(strings.Join(descLines, "\n"))
	if len(st.ProtectsRaw) == 0 {
		return Stub{}, fmt.Errorf("guards: stub %q has no `Protects:` glob — a guard must declare at least one repo-relative path glob it protects", id)
	}
	for _, raw := range st.ProtectsRaw {
		re, err := globToRegexp(raw)
		if err != nil {
			return Stub{}, fmt.Errorf("guards: stub %q has an invalid `Protects:` glob %q: %w", id, raw, err)
		}
		st.protects = append(st.protects, re)
	}
	if st.Command == "" {
		return Stub{}, fmt.Errorf("guards: stub %q has no `Command:` — a guard must declare the command that judges the diff (exit non-zero = refuse)", id)
	}
	if err := rejectTrivialCommand(st.Command); err != nil {
		return Stub{}, fmt.Errorf("guards: stub %q %w", id, err)
	}
	return st, nil
}

// rejectTrivialCommand refuses a guard command that cannot possibly reject a diff
// (a no-op that always exits 0), the analogue of checks refusing an empty-matching
// regex. It is intentionally conservative: it cannot prove an arbitrary command
// ever rejects (the four-way authoring test does that), but it forecloses the
// obvious always-pass shortcuts.
func rejectTrivialCommand(cmd string) error {
	trimmed := strings.TrimSpace(cmd)
	if trivialExitRe.MatchString(trimmed) {
		return fmt.Errorf("`Command:` %q always succeeds — a guard that can never refuse is a no-op; point it at a real policy script", cmd)
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return fmt.Errorf("`Command:` is empty")
	}
	if trivialCommandBases[filepath.Base(fields[0])] {
		return fmt.Errorf("`Command:` %q invokes the no-op/always-pass command %q — a guard must run a real policy script that can exit non-zero to refuse a diff", cmd, fields[0])
	}
	return nil
}

// globToRegexp compiles a path glob to an anchored RE2. `*` and `?` stay within a
// single path segment; `**` spans segments. Other regexp metacharacters are
// escaped so a literal path (e.g. `GUARDS.md`) matches only itself.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i += 2
				continue
			}
			b.WriteString("[^/]*")
			i++
		case '?':
			b.WriteString("[^/]")
			i++
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// Matches reports whether the stub protects the given repo-relative path.
func (st *Stub) Matches(path string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(path)), "./")
	for _, re := range st.protects {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// Triggered returns the stubs whose Protects globs intersect the changed-file set
// (repo-relative paths), in registry order. A guard fires only when the diff
// actually touches something it protects — so a pass touching no protected path
// runs no guard at all.
func (g *Guards) Triggered(changedFiles []string) []Stub {
	if g == nil {
		return nil
	}
	var out []Stub
	for i := range g.Stubs {
		st := &g.Stubs[i]
		for _, f := range changedFiles {
			if st.Matches(f) {
				out = append(out, *st)
				break
			}
		}
	}
	return out
}

// MatchedPaths returns the changed files this stub protects (for a precise
// fix-forward message).
func (st *Stub) MatchedPaths(changedFiles []string) []string {
	var out []string
	for _, f := range changedFiles {
		if st.Matches(f) {
			out = append(out, strings.TrimSpace(f))
		}
	}
	return out
}

// String re-renders the registry (Parse -> String -> Parse is stable).
func (g *Guards) String() string {
	var b strings.Builder
	for _, h := range g.Header {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	for _, st := range g.Stubs {
		fmt.Fprintf(&b, "## %s\n", st.ID)
		if st.Description != "" {
			b.WriteString(st.Description)
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s %s\n", protectsPrefix, strings.Join(st.ProtectsRaw, ", "))
		fmt.Fprintf(&b, "%s %s\n\n", commandPrefix, st.Command)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
