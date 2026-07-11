package audit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spencerharmon/beehive/internal/repo"
)

// warningHeader is the exact line internal/swarm.recorder.appendWarning emits on
// an aborted session: "\n## \u26a0\ufe0f warning\n\n". It is the only
// deterministic, producer-written abort marker.
const warningHeader = "## \u26a0\ufe0f warning"

// toolAbortMarker is the exact line the surrounding agent/tool harness emits as a
// tool RESULT when a tool call is aborted mid-execution — rendered by
// internal/swarm.renderTranscript inside a fenced block ("```\n" + p.Error +
// "\n```\n\n"), so it lands as its own standalone line. It is NOT produced by any
// code in this repo (it appears nowhere in this repo's source — the harness emits
// it), so it is pinned as an exact-line literal and matched with the SAME
// genuinely-trailing discipline as warningHeader: only the last occurrence, and
// only when nothing of substance follows it, is this session's own stall marker.
const toolAbortMarker = "Tool execution aborted"

// lostRaceRe matches lost-claim / forced-reselect language. It is applied ONLY
// to the text inside a warning block, never the whole transcript: the protocol
// prompt echoed into the user turn contains "lost the race"/"ErrLost"/"STOP",
// so a whole-file scan would flag every session.
var lostRaceRe = regexp.MustCompile(`(?i)lost (the )?(race|claim)|errlost|reselect|stale claim`)

// ParseDir parses every "*.md" transcript in dir into Sessions sorted by epoch
// ascending (ties broken by ID). It also annotates the corpus-level
// reconcile-loop heuristic. dir must be a beehive sessions directory
// (submodules/<sm>/sessions); ParseDir never reaches into a target repo/.
//
// ParseDir is per-file resilient: a single unparsable/odd transcript name must
// not zero an entire audit pass, so a file that fails to parse is skipped and its
// error is accumulated rather than aborting the whole batch. The good sessions
// are always returned; any per-file failures are surfaced (never swallowed) in a
// joined error so the caller can report them. A clean directory returns a nil
// error.
//
// An UNFINALIZED stub (a repo.SessionStub placeholder still streaming to a live
// branch) is a known shape, NOT a parse error: ParseDir classifies it out via
// ParseDirCensus so it is neither returned as a Session nor folded into the error
// pile. Callers that need to tell an empty-because-audited window apart from an
// empty-because-unfinalized one must use ParseDirCensus, which surfaces the stub
// count; ParseDir preserves its original (sessions, joined-malformed-error)
// contract for existing callers.
func ParseDir(dir string) ([]Session, error) {
	c, err := ParseDirCensus(dir)
	if err != nil {
		return nil, err
	}
	return c.Sessions, errors.Join(c.Errors...)
}

// ParseDirCensus parses dir into a corpus Census: finalized (mineable) Sessions,
// unfinalized Stubs, and genuinely malformed Errors. It is the corpus-integrity
// front door: a session file that repo.ParseSessionStub recognises (a known
// unfinalized shape, still streaming to a live branch) is recorded as a Stub —
// its sid and the branch it points to — rather than mis-reported as a "no header
// line" parse error. Only a file that is neither a valid transcript nor a
// recognised stub becomes an Error. Sessions are epoch-sorted with the
// reconcile-loop heuristic applied (exactly as ParseDir returns them); Stubs are
// sid-sorted; both are deterministic. A directory that cannot be read is the only
// hard error; per-file failures are collected in Census.Errors, never swallowed.
//
// This is what lets `beehive audit` tell empty-because-audited (a rested swarm)
// apart from empty-because-unfinalized (a corpus-loss defect): the two are
// byte-identical windows, distinguished only by whether unfinalized stubs exist.
func ParseDirCensus(dir string) (Census, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return Census{}, err
	}
	var c Census
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			c.Errors = append(c.Errors, rerr)
			continue
		}
		// Classify a stub BEFORE the transcript parse. A stub carries no
		// "submodule · kind · branch" header, so parseTranscript would reject it
		// as malformed and bury every unfinalized-but-known file in the error
		// pile — the exact 729-errors-and-an-empty-window defect this guards
		// against. repo.ParseSessionStub is the same recogniser beehived uses to
		// resolve a live stub, so audit and the frontend agree on what a stub is.
		if branch, ok := repo.ParseSessionStub(string(data)); ok {
			c.Stubs = append(c.Stubs, Stub{
				SID:    strings.TrimSuffix(e.Name(), ".md"),
				Branch: branch,
			})
			continue
		}
		s, perr := parseTranscript(e.Name(), data)
		if perr != nil {
			c.Errors = append(c.Errors, perr)
			continue
		}
		c.Sessions = append(c.Sessions, *s)
	}
	sortByEpoch(c.Sessions)
	markReconcileLoops(c.Sessions)
	markSilentLosses(c.Sessions)
	sortStubs(c.Stubs)
	return c, nil
}

// sortStubs orders stubs by SID ascending, in place, for deterministic output.
func sortStubs(s []Stub) {
	sort.Slice(s, func(i, j int) bool { return s[i].SID < s[j].SID })
}

// BranchResolver resolves a stub's stream branch to the ref it currently
// exists at (e.g. "refs/heads/<branch>" or "refs/remotes/<remote>/<branch>"),
// or "" when the branch resolves nowhere. It mirrors, in shape and resolution
// order, internal/swarm/sweep.go's private resolveRef closure (refs/heads
// first, then refs/remotes/<remote>) against the PRIMARY coordination repo —
// session branches live in the same repo as submodules/<sm>/sessions/, never a
// target's repo/ checkout — so cmd/beehive/cmd_audit.go's caller-supplied,
// *git.Repo-backed implementation must walk the identical two candidates in
// the identical order (never drift on what counts as a gone branch between
// the finalize sweep and the audit census). Keeping this a plain function type
// rather than a git.Repo dependency is what keeps internal/audit
// dependency-light and lets tests supply a func literal with no git repo at
// all.
type BranchResolver func(branch string) (ref string)

// ClassifyStubs sets GoneBranch on every stub in c.Stubs (in place): a stub is
// GoneBranch when resolve reports its stream branch resolves nowhere (""). It
// is reporting only — it never removes a stub from the list or touches
// Sessions/Errors; see Stub.GoneBranch and Census.CorpusBroken, which is the
// only thing the classification changes. A nil resolve is a safe no-op (every
// stub keeps its zero-value GoneBranch == false), so a caller with no
// coordination-repo access — or a test constructing a Census by hand — can
// skip classification and CorpusBroken behaves exactly as it did before this
// field existed.
func ClassifyStubs(c *Census, resolve BranchResolver) {
	if resolve == nil {
		return
	}
	for i := range c.Stubs {
		c.Stubs[i].GoneBranch = resolve(c.Stubs[i].Branch) == ""
	}
}

// ParseFile reads and parses a single transcript file. Bytes is the file size.
func ParseFile(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := parseTranscript(filepath.Base(path), data)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// parseTranscript is the pure core: it derives every field from the file name
// and bytes. Splitting it out keeps parsing testable without touching disk.
func parseTranscript(name string, data []byte) (*Session, error) {
	// The header "branch:" line is authoritative, so parse it FIRST and split the
	// file name against it. The runner emits "<branch>-<epoch>[-<pid>].md"; only
	// the header tells us where the branch ends and the epoch begins, so a greedy
	// name-only split would mis-attribute the per-process suffix.
	hdr, err := parseHeader(data)
	if err != nil {
		return nil, fmt.Errorf("audit: %s: %w", name, err)
	}
	stem, epoch, err := splitName(name, hdr.Branch)
	if err != nil {
		return nil, err
	}
	turns, userTurns, warn, harnessAbortStall, err := scanBody(data)
	if err != nil {
		return nil, fmt.Errorf("audit: %s: %w", name, err)
	}
	toolCalls, toolFails, toolCats := scanToolCalls(data)
	s := &Session{
		ID:        stem,
		Epoch:     epoch,
		Submodule: hdr.Submodule,
		Kind:      hdr.Kind,
		Branch:    hdr.Branch,
		TaskID:    strings.TrimPrefix(hdr.Branch, "bee-"),
		Model:     hdr.Model,
		Runner:    hdr.Runner,
		Bytes:     int64(len(data)),
		Turns:     turns,
		UserTurns: userTurns,
		ToolCalls:    toolCalls,
		ToolFails:    toolFails,
		ToolFailCats: toolCats,
	}
	if warn != "" {
		s.Heuristics.Aborted = true
		s.Heuristics.AbortReason = firstNonEmptyLine(warn)
		s.Heuristics.LostRace = lostRaceRe.MatchString(warn)
		s.Heuristics.CompletionMiss = hdr.Kind == KindWork
	}
	// Harness-abort-stall path (ADDITIVE; see scanBody): a transcript that ends on
	// a genuinely-trailing raw "Tool execution aborted" tool result — never given
	// a recovery turn and never sealed by a "## ⚠️ warning" block — reached NO
	// verdict. Fold it into the same Aborted/CompletionMiss signals the warning
	// path feeds, so downstream TSV/window output needs no new plumbing. This is
	// independent of the warning path: on genuine producer output the two are
	// mutually exclusive (the runner writes its warning LAST, which scanBody's
	// disqualifier set already accounts for), but if both somehow fired the result
	// is still a coherent Aborted session. CompletionMiss here extends to every
	// task-bearing kind (work/review/arbitrate) since the confirmed instance is an
	// arbitration; bootstrap/reconcile own no task handoff and are excluded.
	if harnessAbortStall {
		s.Heuristics.HarnessAbortStall = true
		s.Heuristics.Aborted = true
		if s.Heuristics.AbortReason == "" {
			s.Heuristics.AbortReason = toolAbortMarker
		}
		if isTaskBearingKind(hdr.Kind) {
			s.Heuristics.CompletionMiss = true
		}
	}
	return s, nil
}

// isTaskBearingKind reports whether a session kind owes a task-status handoff
// whose premature end is a completion miss: work (→ NEEDS-REVIEW), review
// (→ NEEDS-ARBITRATION / DONE), and arbitrate (→ DONE) each must land a status
// transition, so a stall short of it misses completion. bootstrap and reconcile
// carry no such handoff (they never mark a task DONE) and are excluded — matching
// their exclusion from the delivered-only trend gauge.
func isTaskBearingKind(kind string) bool {
	switch kind {
	case KindWork, KindReview, KindArbitration:
		return true
	default:
		return false
	}
}

// splitName decomposes a transcript file name "<branch>-<epoch>[-<suffix>].md"
// into the stem (the full ID, suffix included) and the epoch, using the
// header-authoritative branch as the prefix.
//
// The branch is NOT re-derived from the name. The runner now emits
// "<branch>-<epoch>-<pid>.md" (internal/swarm.SessionID appends a per-process
// discriminator for fan-out), so greedily taking the LAST "-<digits>" as the
// epoch would fold the real epoch into the branch and then disagree with the
// header — the exact crash this engine hit on the live corpus. Instead we require
// the name to begin with "<branch>-", take the FIRST numeric segment after that
// prefix as the epoch, and keep any remaining "-<suffix>" as an opaque session
// discriminator: it stays in the ID but is never folded into the branch or
// taskid. The strict header-vs-name agreement is preserved — a name that does not
// start with the header branch, or whose first post-prefix segment is not a
// number, is rejected (a genuinely malformed/mis-headed transcript).
func splitName(name, branch string) (stem string, epoch int64, err error) {
	stem = strings.TrimSuffix(name, ".md")
	if stem == name {
		return "", 0, fmt.Errorf("audit: %s: not a .md file", name)
	}
	prefix := branch + "-"
	if !strings.HasPrefix(stem, prefix) {
		return "", 0, fmt.Errorf("audit: %s: name does not match header branch %q", name, branch)
	}
	rest := stem[len(prefix):] // "<epoch>" or "<epoch>-<suffix>"
	epochStr := rest
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		epochStr = rest[:i] // first segment; the trailing "-<suffix>" stays opaque
	}
	if epochStr == "" {
		return "", 0, fmt.Errorf("audit: %s: no -<epoch> segment after branch %q", name, branch)
	}
	epoch, err = strconv.ParseInt(epochStr, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("audit: %s: bad epoch %q", name, epochStr)
	}
	return stem, epoch, nil
}

// header is the parsed transcript metadata line: "submodule: <sm> · kind:
// <kind> · branch: <branch>[ · <key>: <value> ...]" (recognised trailing keys:
// "model", "runner").
type header struct {
	Submodule, Kind, Branch string
	Model                   string // "model:" field (commit 248e967); "" if absent
	Runner                  string // "runner:" field (build SHA / "dev"); "" if absent (legacy)
}

// headerKeys are the REQUIRED leading "key: value" segments, in this exact
// order — unchanged since the header's introduction. Any segment beyond them is
// a trailing extra field (see parseHeaderLine).
var headerKeys = []string{"submodule", "kind", "branch"}

// headerFieldRe validates and captures a single "key: value" header segment
// (already split on the U+00B7 middle-dot separator and trimmed of leading
// space): a bare identifier key, a colon, and a whitespace-free value — the
// same strictness the original three-field regex enforced per field.
var headerFieldRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)\s*:\s*(\S+)$`)

// parseHeader finds and parses the "submodule: … · kind: … · branch: …" line
// (optionally followed by more "· key: value" fields).
func parseHeader(data []byte) (header, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if !strings.HasPrefix(line, "submodule:") {
			continue
		}
		return parseHeaderLine(line)
	}
	if err := sc.Err(); err != nil {
		return header{}, err
	}
	return header{}, fmt.Errorf("no submodule/kind/branch header line")
}

// parseHeaderLine parses the header as an ORDERED "·"-separated key:value list.
// The first three segments MUST be submodule/kind/branch, in that order — a
// missing field or a reordering is still rejected as malformed, exactly as
// before. Any segment beyond the third is a trailing extra field: it is
// accepted (never rejected) so the producer can grow the header — e.g. commit
// 248e967's "· model: <model>" — without ever rebreaking this consumer again;
// a recognised extra key (currently only "model") is captured, an unrecognised
// one is silently ignored.
func parseHeaderLine(line string) (header, error) {
	fields := strings.Split(line, "\u00b7")
	if len(fields) < len(headerKeys) {
		return header{}, fmt.Errorf("malformed header line %q", line)
	}
	var h header
	vals := make([]string, len(headerKeys))
	for i, raw := range fields {
		m := headerFieldRe.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			return header{}, fmt.Errorf("malformed header line %q", line)
		}
		key, val := m[1], m[2]
		if i < len(headerKeys) {
			if key != headerKeys[i] {
				return header{}, fmt.Errorf("malformed header line %q", line)
			}
			vals[i] = val
			continue
		}
		if key == "model" {
			h.Model = val
		}
		if key == "runner" {
			h.Runner = val
		}
		// Any other trailing key is a future field this parser does not yet know
		// about: ignore it, do not reject the line.
	}
	h.Submodule, h.Kind, h.Branch = vals[0], vals[1], vals[2]
	return h, nil
}

// scanBody counts assistant and user turns by the PINNED exact-line rule,
// UNCONDITIONALLY end to end, and derives two independent trailing-abort
// signals from the file's tail:
//
//   - warning: the "## ⚠️ warning" block text (everything after the header
//     line) — but ONLY if the file's LAST exact warningHeader line is genuinely
//     trailing: no "## assistant"/"## user" turn-marker line occurs after it.
//   - harnessAbortStall: true iff the file's LAST exact toolAbortMarker line is
//     genuinely trailing with respect to turn markers AND warningHeader — the
//     transcript ends on a raw "Tool execution aborted" tool result, never given
//     a recovery turn and never sealed by a runner warning block.
//
// A transcript's own work routinely greps/dumps a PRIOR session's transcript as
// evidence (the session-audit series' explicit, permanent charter), which can
// embed another file's exact "## ⚠️ warning" OR "Tool execution aborted" line
// mid-body. Such an occurrence is quoted content, not this file's own abort
// marker: it must never gate the turn count, seed the warning text, or set the
// stall flag. Only the LAST occurrence of each is even a candidate, and only
// when nothing of substance follows it does it count — matching the producer
// contract (internal/swarm.recorder.appendWarning always writes the warning
// block LAST). Including warningHeader in the tool-abort disqualifier set keeps
// the two paths mutually exclusive on genuine output: a tool-abort immediately
// sealed by the runner's warning block is owned by the warning path, not counted
// as an unrecovered stall. A scan failure (e.g. an over-long line) is surfaced,
// never silently truncated.
func scanBody(data []byte) (turns, userTurns int, warning string, harnessAbortStall bool, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var lines []string
	lastWarn := -1  // index into lines of the LAST exact warningHeader line seen
	lastAbort := -1 // index into lines of the LAST exact toolAbortMarker line seen
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch line {
		case "## assistant":
			turns++
		case "## user":
			userTurns++
		case warningHeader:
			lastWarn = len(lines)
		case toolAbortMarker:
			lastAbort = len(lines)
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		return 0, 0, "", false, err
	}
	// Warning-block path (UNCHANGED behaviour): the last warningHeader, genuinely
	// trailing (disqualified only by a following turn marker), seeds the text.
	if lastWarn >= 0 && genuinelyTrailing(lines, lastWarn, "## assistant", "## user") {
		var warn strings.Builder
		for _, l := range lines[lastWarn+1:] {
			warn.WriteString(l)
			warn.WriteByte('\n')
		}
		warning = warn.String()
	}
	// Harness-abort-stall path (ADDITIVE): the last toolAbortMarker, genuinely
	// trailing with respect to turn markers AND a runner warning block (so a
	// warning-sealed abort stays owned by the path above).
	if lastAbort >= 0 && genuinelyTrailing(lines, lastAbort, "## assistant", "## user", warningHeader) {
		harnessAbortStall = true
	}
	return turns, userTurns, warning, harnessAbortStall, nil
}

// genuinelyTrailing reports whether the candidate marker at index idx is the
// file's last line of substance: no line after it exactly equals any of
// stoppers. It is the shared "last occurrence, nothing real following it"
// discipline both trailing-abort signals key off, so a merely-quoted marker
// mid-body (with real content after it) never counts.
func genuinelyTrailing(lines []string, idx int, stoppers ...string) bool {
	for _, l := range lines[idx+1:] {
		for _, s := range stoppers {
			if l == s {
				return false
			}
		}
	}
	return true
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// sortByEpoch orders sessions by epoch ascending, ties broken by ID, in place.
func sortByEpoch(s []Session) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Epoch != s[j].Epoch {
			return s[i].Epoch < s[j].Epoch
		}
		return s[i].ID < s[j].ID
	})
}

// markReconcileLoops sets Heuristics.ReconcileLoop on every reconcile session
// that sits adjacent (in epoch order) to another reconcile session — i.e. two or
// more reconcile sessions ran back-to-back with no other-kind session between
// them. This deliberately uses adjacency rather than fabricating a "task became
// DONE at time T" timeline (the files carry no such timestamp), keeping the flag
// deterministic. Input must already be epoch-sorted.
func markReconcileLoops(s []Session) {
	for i := 0; i < len(s); i++ {
		if s[i].Kind != KindReconcile {
			continue
		}
		prevAdj := i > 0 && s[i-1].Kind == KindReconcile
		nextAdj := i < len(s)-1 && s[i+1].Kind == KindReconcile
		if prevAdj || nextAdj {
			s[i].Heuristics.ReconcileLoop = true
		}
	}
}
