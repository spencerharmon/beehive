package swarm

import (
	"fmt"
	"sort"
	"strings"
)

// Per-turn context bounds. They keep the injected block small and, crucially,
// BOUNDED regardless of session length: the summary is a rolling window and each
// file body/diff is clipped. Generous enough to preserve the decisions/outputs a
// turn needs (correctness of the pass outranks the byte cut) while dropping
// verbatim scrollback and unchanged file bodies.
const (
	ctxSummaryCap  = 4096 // max bytes of rolling summary retained (oldest dropped past this)
	ctxFileCap     = 4096 // max bytes emitted for one new file body or one file diff
	ctxSummaryLine = 240  // max bytes kept from one folded transcript message
)

// turnContext assembles a bounded per-turn prompt add-on so a honeybee is not fed
// (everything it has seen so far) on every continue turn. It:
//
//   - pins the content of files the agent has already SEEN (opencode `read`
//     outputs) and, on later turns, emits a DIFF when a file changed and NOTHING
//     when it is unchanged — so the same file is never re-sent in full; and
//   - folds prior turns' transcript into a bounded ROLLING SUMMARY (decisions,
//     tool actions, outputs) instead of re-injecting the verbatim scrollback.
//
// Per-turn injected context becomes (new/changed content + summary), not
// (everything seen so far) — the ROI P1 "Working set / retrieval" + "History"
// token levers. The Runner gates it behind ContextDiff, OFF by default, so the
// default path is the bare nextPrompt output, byte-for-byte.
//
// It performs no I/O: everything comes from the opencode transcript ([]Message)
// the recorder already polls, so it is deterministic and unit-testable without a
// server, disk, or worktree. It BOUNDS what is re-injected — it never drops the
// record: the committed session transcript and change doc remain authoritative.
type turnContext struct {
	pinned  map[string]string // file path -> full content last emitted (diff base)
	summary []string          // folded per-message summary entries (rolling window)
	folded  int               // count of transcript messages already folded in
}

// newTurnContext returns a ready assembler.
func newTurnContext() *turnContext {
	return &turnContext{pinned: map[string]string{}}
}

// assemble returns the next turn's prompt: base ("continue" / the completion
// hint) plus, when there is anything to add, a bounded context block =
// rolling-summary + changed-file diffs. base is ALWAYS included first and
// verbatim, so a turn never loses the instruction it needs to make forward
// progress or to satisfy the completion check. msgs is the FULL transcript so
// far; assemble folds only the not-yet-folded tail into the summary and derives
// current file state idempotently (an unchanged file re-observed across turns is
// omitted). With no prior turns and no seen files it returns base unchanged.
func (tc *turnContext) assemble(base string, msgs []Message) string {
	if tc.pinned == nil {
		tc.pinned = map[string]string{}
	}
	tc.fold(msgs)
	files := tc.fileSection(latestReadFiles(msgs))
	sum := tc.summaryBlock()
	if sum == "" && files == "" {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n--- context (bounded: prior turns summarized; unchanged files omitted, changed shown as diffs) ---\n")
	b.WriteString(sum)
	b.WriteString(files)
	return b.String()
}

// fold appends a terse summary entry for every transcript message not yet folded,
// then trims the rolling window to the byte cap (oldest first). Because msgs is
// the full history each turn, folded advances so a message is summarized once.
func (tc *turnContext) fold(msgs []Message) {
	for i := tc.folded; i < len(msgs); i++ {
		if e := summarizeMessage(msgs[i]); e != "" {
			tc.summary = append(tc.summary, e)
		}
	}
	if len(msgs) > tc.folded {
		tc.folded = len(msgs)
	}
	tc.trimSummary()
}

// trimSummary drops the oldest entries until the summary is within ctxSummaryCap,
// keeping the most recent turns (the ones a continue is most likely to need).
func (tc *turnContext) trimSummary() {
	total := 0
	for _, e := range tc.summary {
		total += len(e) + 1
	}
	for total > ctxSummaryCap && len(tc.summary) > 1 {
		total -= len(tc.summary[0]) + 1
		tc.summary = tc.summary[1:]
	}
}

// summaryBlock renders the rolling summary, or "" when empty.
func (tc *turnContext) summaryBlock() string {
	if len(tc.summary) == 0 {
		return ""
	}
	return "prior turns (summary):\n" + strings.Join(tc.summary, "\n") + "\n"
}

// fileSection emits, for each file the agent has seen this session (cur = latest
// read content per path), only what changed since it was last emitted: nothing
// for an unchanged file, the body once for a newly-seen file, and a line diff for
// a changed one. It updates the pin to the current full content so the next diff
// is measured against what the agent last saw. Deterministic path order.
func (tc *turnContext) fileSection(cur map[string]string) string {
	if len(cur) == 0 {
		return ""
	}
	paths := make([]string, 0, len(cur))
	for p := range cur {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		now := cur[p]
		prev, seen := tc.pinned[p]
		switch {
		case seen && prev == now:
			// Unchanged since the agent last saw it: do NOT re-send it. This is the
			// core cut — an unchanged file costs zero bytes on the next turn.
			continue
		case !seen:
			fmt.Fprintf(&b, "\nfile %s (seen — full body shown once, then diffed):\n%s\n", p, clip(now, ctxFileCap))
		default:
			fmt.Fprintf(&b, "\nfile %s (changed — diff):\n%s\n", p, clip(lineDiff(prev, now), ctxFileCap))
		}
		tc.pinned[p] = now
	}
	return b.String()
}

// latestReadFiles maps each file path the agent READ to the most recent content
// it saw (opencode stamps a completed `read` tool part with the file body in
// Output). "read" is the target of "the same files re-read in full every turn";
// writes/edits still surface in the summary as actions, and a subsequent read
// re-observes the new content, so a single representation per path is kept.
func latestReadFiles(msgs []Message) map[string]string {
	out := map[string]string{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type != "tool" || p.Tool != "read" || p.Status != "completed" {
				continue
			}
			path := strArg(p.Input, "filePath")
			if path == "" || p.Output == "" {
				continue
			}
			out[path] = p.Output
		}
	}
	return out
}

// summarizeMessage renders one transcript message as a terse summary line: the
// role, the first line of any assistant/user text (a decision or instruction),
// and each tool action (name + compact arg). Reasoning and full tool output are
// dropped — they are the verbatim scrollback we are replacing. "" when the
// message carries nothing worth keeping.
func summarizeMessage(m Message) string {
	var acts []string
	for _, p := range m.Parts {
		switch p.Type {
		case "text":
			if s := firstLine(p.Text); s != "" {
				acts = append(acts, s)
			}
		case "tool":
			acts = append(acts, toolAction(p))
		}
	}
	if len(acts) == 0 {
		return ""
	}
	role := m.Role
	if role == "" {
		role = "?"
	}
	return clip(role+": "+strings.Join(acts, "; "), ctxSummaryLine)
}

// toolAction is a compact "tool arg" for the summary, reusing inputSummary's
// per-tool argument extraction (bash command, file path, pattern, ...).
func toolAction(p Part) string {
	arg := inputSummary(p.Tool, p.Input)
	if arg == "" {
		return p.Tool
	}
	return p.Tool + " " + arg
}

// strArg reads a string field from a tool's decoded input arguments.
func strArg(in map[string]any, key string) string {
	if in == nil {
		return ""
	}
	if v, ok := in[key]; ok {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

// clip bounds s to n bytes, appending a marker when truncated so the reader knows
// content was elided (the full text remains in the committed transcript).
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(clipped; full text in the session transcript)"
}

// lineDiff is a minimal line-oriented diff of old->new: removed ("- ") and added
// ("+ ") lines in order via an LCS, so unchanged lines are dropped and only the
// change is sent. Pure Go, no deps. Empty when the inputs are equal.
func lineDiff(old, next string) string {
	a := strings.Split(old, "\n")
	b := strings.Split(next, "\n")
	m, n := len(a), len(b)
	// lcs[i][j] = length of the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			fmt.Fprintf(&out, "- %s\n", a[i])
			i++
		default:
			fmt.Fprintf(&out, "+ %s\n", b[j])
			j++
		}
	}
	for ; i < m; i++ {
		fmt.Fprintf(&out, "- %s\n", a[i])
	}
	for ; j < n; j++ {
		fmt.Fprintf(&out, "+ %s\n", b[j])
	}
	return strings.TrimRight(out.String(), "\n")
}
