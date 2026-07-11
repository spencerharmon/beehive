package audit

import (
	"bufio"
	"bytes"
	"regexp"
	"sort"
	"strings"
)

// Tool-call failure mining (the "tool check").
//
// # What this measures and why it is here
//
// A large, recurring slice of wasted honeybee agent-time is spent on tool calls
// (overwhelmingly bash) that FAIL and have to be re-issued: probing for a git
// branch that does not exist (doc-only tasks have no `bee-<taskid>` code branch),
// hunting for `beehive` subcommands/flags that were never added, cd-ing into a
// path that is not there, or rebuilding a binary that is already deployed. A
// manual pass over the session corpus (operator, 2026-07-11) put this at roughly
// one-in-eleven bash calls, concentrated in a handful of stable, fixable classes.
// Making the count DETERMINISTIC and part of every audit pass turns that one-off
// spelunk into a tracked, reproducible trend — the same treatment SilentLoss got.
//
// # Producer-anchored, fence-scoped detection (immune to the series' own charter)
//
// internal/swarm.renderTranscript emits every tool call as a top-level line
// "**🔧 <tool>** `<input summary>`" followed, when the call produced output or
// errored, by that text inside a bare ``` fenced block. The wrench-bold prefix is
// producer-only structural markup written at column 0, never indented. The
// session-audit series' explicit charter is to mine PRIOR sessions, so a
// transcript may embed another session's text — but a quoted transcript is
// rendered INSIDE a ``` fence (it is tool OUTPUT of the mining command). So the
// scan counts a tool marker ONLY at fence-depth 0: a quoted marker sits inside a
// fence and is skipped, exactly as the abort/silent-loss heuristics scope
// themselves away from the prompt-polluted body. The one residual imprecision —
// a session that cats a RAW transcript whose own output embeds bare ``` lines can
// desync the depth counter — is the same conservative-flag tradeoff the rest of
// this package accepts: the number is a trend signal, not an assertion, and such
// full-raw-transcript dumps are rare (the series mines via `beehive audit`, whose
// output is TSV and carries no tool markers).
//
// A tool call is counted a FAILURE when the fenced output that immediately
// follows its marker matches one of the classifier's signatures. Empty output (a
// silent success) is never a failure. Categories are stable so a future pass can
// rank fixable classes by frequency without re-deriving them.

// toolMarkerRe matches the START of a producer-emitted tool-call line,
// "**🔧 <tool>** ", capturing the tool name. Anchored at ^ so only a
// column-0 (structural) marker matches; a quoted marker is inside a fence and is
// never reached at depth 0 anyway.
var toolMarkerRe = regexp.MustCompile(`^\*\*\x{1f527} (\w+)\*\* `)

// toolFailClass is one ordered failure signature. First match (in slice order)
// wins, so more-specific classes precede the generic fatal/panic catch-all.
type toolFailClass struct {
	cat string
	re  *regexp.Regexp
}

// toolFailClasses are the deterministic failure signatures, ordered
// most-specific first. They key off producer/OS/git/cobra output that a passing
// call never emits. The categories mirror the operator's 2026-07-11 manual
// triage so the recurring audit reproduces (and tracks) those exact classes.
var toolFailClasses = []toolFailClass{
	// A missing git ref / bad revision — the dominant class: reviewers probing
	// for a `bee-<taskid>` branch that a doc-only task never created.
	{"missing-git-ref", regexp.MustCompile(`couldn't find remote ref|ambiguous argument|unknown revision|does not appear to be a git repository|not a git repository`)},
	// A beehive (or any tool) subcommand/flag that does not exist — agents
	// hunting for a validate/lint/list affordance that was never added.
	{"unknown-subcommand", regexp.MustCompile(`unknown flag|unknown command|unknown shorthand flag`)},
	// A binary or program not on PATH — e.g. rebuilt audit binaries under /tmp.
	{"command-not-found", regexp.MustCompile(`(?m)command not found|: not found(\s|$)|executable file not found`)},
	// A path that is not there — cd/ls/cat into a missing worktree or doc.
	{"path-missing", regexp.MustCompile(`(?i)no such file or directory|cannot access`)},
	{"permission-denied", regexp.MustCompile(`(?i)permission denied`)},
	// Generic git/tool hard failure not caught above.
	{"fatal-or-panic", regexp.MustCompile(`(?m)^fatal:|(?m)^panic:|(?m)^error:`)},
	// A shell-reported non-zero exit that reached the transcript verbatim.
	{"nonzero-exit", regexp.MustCompile(`exit status [1-9]`)},
}

// classifyToolFail returns the failure category of a tool call's fenced output,
// or "" when the output shows no failure signature (a success or an empty
// result). First matching class in toolFailClasses order wins.
func classifyToolFail(output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	for _, c := range toolFailClasses {
		if c.re.MatchString(output) {
			return c.cat
		}
	}
	return ""
}

// scanToolCalls counts this session's OWN tool calls and the subset that failed,
// bucketed by category. It walks the transcript tracking bare-``` fence depth and
// only recognises a tool marker at depth 0 (see the package doc above), pairing
// each marker with the next fenced block as that call's output.
func scanToolCalls(data []byte) (calls, fails int, cats map[string]int) {
	cats = map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	inFence := false
	pending := false   // a depth-0 tool marker awaiting its output fence
	collecting := false // currently accumulating the pending marker's output fence
	var out strings.Builder

	flush := func() {
		if pending {
			if cat := classifyToolFail(out.String()); cat != "" {
				fails++
				cats[cat]++
			}
		}
		pending = false
		collecting = false
		out.Reset()
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "```" {
			if inFence {
				inFence = false
				if collecting {
					// The pending call's output fence just closed: evaluate it.
					flush()
				}
			} else {
				inFence = true
				if pending && !collecting {
					// This fence is the pending marker's output block.
					collecting = true
				}
			}
			continue
		}
		if inFence {
			if collecting {
				out.WriteString(line)
				out.WriteByte('\n')
			}
			continue
		}
		// Depth 0 (structural) line.
		if toolMarkerRe.MatchString(line) {
			flush() // finalize any previous marker that had no output block
			calls++
			pending = true
			continue
		}
		if line == "## assistant" || line == "## user" {
			flush()
		}
	}
	flush()
	return calls, fails, cats
}

// ToolFailTask is the per-task tool-call rollup, for ranking the worst offenders.
type ToolFailTask struct {
	TaskID string
	Calls  int
	Fails  int
}

// ToolFailSummary is the full-corpus tool-call-failure rollup a pass reports.
// Sessions counts sessions that made at least one tool call; ByCategory and
// PerTask let a pass rank the recurring, fixable failure classes.
type ToolFailSummary struct {
	Sessions   int
	Calls      int
	Fails      int
	ByCategory map[string]int
	PerTask    []ToolFailTask
}

// AggregateToolFails rolls tool-call stats up over the WHOLE corpus of finalized
// sessions (not just the N-2 window), so each pass looks through every mineable
// session for tool-call waste. PerTask is sorted by Fails desc, then Calls desc,
// then TaskID, for a stable worst-first ranking.
func AggregateToolFails(sessions []Session) ToolFailSummary {
	s := ToolFailSummary{ByCategory: map[string]int{}}
	byTask := map[string]*ToolFailTask{}
	for _, sess := range sessions {
		if sess.ToolCalls > 0 {
			s.Sessions++
		}
		s.Calls += sess.ToolCalls
		s.Fails += sess.ToolFails
		for cat, n := range sess.ToolFailCats {
			s.ByCategory[cat] += n
		}
		t := byTask[sess.TaskID]
		if t == nil {
			t = &ToolFailTask{TaskID: sess.TaskID}
			byTask[sess.TaskID] = t
		}
		t.Calls += sess.ToolCalls
		t.Fails += sess.ToolFails
	}
	for _, t := range byTask {
		s.PerTask = append(s.PerTask, *t)
	}
	sort.Slice(s.PerTask, func(i, j int) bool {
		a, b := s.PerTask[i], s.PerTask[j]
		if a.Fails != b.Fails {
			return a.Fails > b.Fails
		}
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		return a.TaskID < b.TaskID
	})
	return s
}

// SortedCategories returns the summary's categories, count desc then name, for
// deterministic TSV output.
func (s ToolFailSummary) SortedCategories() []ToolFailTask {
	out := make([]ToolFailTask, 0, len(s.ByCategory))
	for cat, n := range s.ByCategory {
		out = append(out, ToolFailTask{TaskID: cat, Fails: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Fails != out[j].Fails {
			return out[i].Fails > out[j].Fails
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out
}
