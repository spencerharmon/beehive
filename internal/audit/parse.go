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
)

// warningHeader is the exact line internal/swarm.recorder.appendWarning emits on
// an aborted session: "\n## \u26a0\ufe0f warning\n\n". It is the only
// deterministic, producer-written abort marker.
const warningHeader = "## \u26a0\ufe0f warning"

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
func ParseDir(dir string) ([]Session, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Session
	var errs []error
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		s, err := ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, *s)
	}
	sortByEpoch(out)
	markReconcileLoops(out)
	return out, errors.Join(errs...)
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
	sub, kind, hdrBranch, err := parseHeader(data)
	if err != nil {
		return nil, fmt.Errorf("audit: %s: %w", name, err)
	}
	stem, epoch, err := splitName(name, hdrBranch)
	if err != nil {
		return nil, err
	}
	turns, userTurns, warn, err := scanBody(data)
	if err != nil {
		return nil, fmt.Errorf("audit: %s: %w", name, err)
	}
	s := &Session{
		ID:        stem,
		Epoch:     epoch,
		Submodule: sub,
		Kind:      kind,
		Branch:    hdrBranch,
		TaskID:    strings.TrimPrefix(hdrBranch, "bee-"),
		Bytes:     int64(len(data)),
		Turns:     turns,
		UserTurns: userTurns,
	}
	if warn != "" {
		s.Heuristics.Aborted = true
		s.Heuristics.AbortReason = firstNonEmptyLine(warn)
		s.Heuristics.LostRace = lostRaceRe.MatchString(warn)
		s.Heuristics.CompletionMiss = kind == KindWork
	}
	return s, nil
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

// headerRe captures the three fields of the transcript header line:
// "submodule: <sm> · kind: <kind> · branch: <branch>". The separator is the
// U+00B7 middle dot the recorder writes.
var headerRe = regexp.MustCompile(`^submodule:\s*(\S+)\s*\x{00b7}\s*kind:\s*(\S+)\s*\x{00b7}\s*branch:\s*(\S+)\s*$`)

// parseHeader finds and parses the "submodule: … · kind: … · branch: …" line.
func parseHeader(data []byte) (sub, kind, branch string, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if !strings.HasPrefix(line, "submodule:") {
			continue
		}
		m := headerRe.FindStringSubmatch(line)
		if m == nil {
			return "", "", "", fmt.Errorf("malformed header line %q", line)
		}
		return m[1], m[2], m[3], nil
	}
	if err := sc.Err(); err != nil {
		return "", "", "", err
	}
	return "", "", "", fmt.Errorf("no submodule/kind/branch header line")
}

// scanBody counts assistant and user turns by the PINNED exact-line rule and
// extracts the trailing "## ⚠️ warning" block text (everything after the warning
// header line), if present. A scan failure (e.g. an over-long line) is surfaced,
// never silently truncated.
func scanBody(data []byte) (turns, userTurns int, warning string, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	inWarn := false
	var warn strings.Builder
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case inWarn:
			warn.WriteString(line)
			warn.WriteByte('\n')
			continue
		case line == "## assistant":
			turns++
		case line == "## user":
			userTurns++
		case line == warningHeader:
			inWarn = true
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, "", err
	}
	return turns, userTurns, warn.String(), nil
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
