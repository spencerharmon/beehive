package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// defaultSummaryMax bounds the rolling prior-turn summary (bytes) when the runner
// does not set one. Big enough to keep several turns of decisions/actions, small
// enough that it never approaches re-injecting the full transcript.
const defaultSummaryMax = 4096

// turnContext bounds the per-turn context the runner feeds the agent to
// (new/changed file content + a rolling summary of prior turns) instead of
// re-sending everything seen so far. It is the mechanism behind the two Priority-1
// token levers for a single honeybee:
//
//   - Working set / retrieval: it PINS the state of every file the agent has
//     already seen and, when one changes, feeds a DIFF (lineDiff) rather than the
//     full file; an unchanged file is omitted entirely.
//   - History: it rolls prior turns into a bounded SUMMARY (digestTurn) — the
//     decisions and actions, with the verbatim tool OUTPUT dropped — instead of
//     re-injecting the full transcript.
//
// It is a pure state machine: the runner feeds it the current file contents and
// the session transcript each turn and it returns the bounded prompt. It is inert
// unless the runner opts in (Runner.ContextDiff); the default loop still sends a
// bare "continue" byte-for-byte, so the durable record (session transcript +
// change doc) is unchanged — this is context COMPRESSION, not dropping history.
type turnContext struct {
	seen       map[string]string // path -> content last fed to the agent (the pin)
	digests    []string          // per-turn summaries, oldest first (rolling window)
	digested   map[string]bool   // assistant message IDs already summarised
	summaryMax int               // cap on the rolling summary size (bytes)
}

// newTurnContext returns an empty assembler. summaryMax <= 0 uses the default.
func newTurnContext(summaryMax int) *turnContext {
	if summaryMax <= 0 {
		summaryMax = defaultSummaryMax
	}
	return &turnContext{
		seen:       map[string]string{},
		digested:   map[string]bool{},
		summaryMax: summaryMax,
	}
}

// assemble builds the bounded per-turn prompt: the rolling summary of prior turns
// + ONLY the files that are new or changed since the agent last saw them + the
// turn directive. That is (new/changed content + summary), NOT everything seen so
// far. When there is nothing to bound yet (no summary, no file delta) it returns
// the bare directive so an early turn behaves exactly like a plain continue.
func (tc *turnContext) assemble(directive string, files map[string]string, msgs []Message) string {
	tc.rollTranscript(msgs)
	fileBlock := tc.fileSection(files)
	summary := tc.summary()
	if summary == "" && fileBlock == "" {
		return directive
	}
	var b strings.Builder
	b.WriteString("# Turn context (bounded: a summary of prior turns + only what changed since your last turn)\n")
	b.WriteString("# Unchanged files you have already read are omitted — re-read one only if you need more than the diff below.\n")
	if summary != "" {
		b.WriteString("\n## Prior turns (rolling summary)\n")
		b.WriteString(summary)
		b.WriteString("\n")
	}
	if fileBlock != "" {
		b.WriteString("\n## Files new or changed since your last turn\n")
		b.WriteString(fileBlock)
	}
	b.WriteString("\n")
	b.WriteString(directive)
	return b.String()
}

// fileSection returns the changed-files block for this turn and updates the pin.
// A file the agent has not seen is emitted in FULL; a file whose content changed
// since it was last fed is emitted as a line DIFF; an unchanged file is OMITTED
// (its content is already in the conversation). Paths are sorted for determinism.
func (tc *turnContext) fileSection(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		cur := files[p]
		prev, ok := tc.seen[p]
		switch {
		case !ok:
			fmt.Fprintf(&b, "### %s (new)\n```\n%s\n```\n", p, cur)
		case prev != cur:
			fmt.Fprintf(&b, "### %s (changed — diff)\n```diff\n%s```\n", p, lineDiff(prev, cur))
		default:
			continue // unchanged: do not re-send it
		}
		tc.seen[p] = cur
	}
	return b.String()
}

// rollTranscript folds any assistant turns not yet summarised into the rolling
// summary, then evicts the oldest digests once it exceeds the byte cap — so the
// history the agent carries is a bounded summary of decisions/actions rather than
// the full verbatim transcript. At least the most recent digest is always kept.
func (tc *turnContext) rollTranscript(msgs []Message) {
	for _, m := range msgs {
		if m.Role != "assistant" || m.ID == "" || tc.digested[m.ID] {
			continue
		}
		tc.digested[m.ID] = true
		if d := digestTurn(m); d != "" {
			tc.digests = append(tc.digests, d)
		}
	}
	for len(tc.digests) > 1 && tc.summaryBytes() > tc.summaryMax {
		tc.digests = tc.digests[1:]
	}
}

func (tc *turnContext) summaryBytes() int {
	n := 0
	for _, d := range tc.digests {
		n += len(d) + 1 // + the joining newline
	}
	return n
}

func (tc *turnContext) summary() string { return strings.Join(tc.digests, "\n") }

// digestTurn compresses one assistant turn to its decisions and actions: the
// assistant text (truncated) plus a one-line-per-tool list of what it DID (tool +
// target), deliberately dropping the verbatim tool OUTPUT — the file dumps and
// command output that dominate transcript size. This is what "roll the transcript
// into a running summary" keeps versus drops.
func digestTurn(m Message) string {
	var parts []string
	for _, p := range m.Parts {
		switch p.Type {
		case "text":
			if t := strings.TrimSpace(p.Text); t != "" {
				parts = append(parts, truncate(t, 400))
			}
		case "tool":
			parts = append(parts, "· "+p.Tool+" "+firstLine(inputSummary(p.Tool, p.Input)))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// filesFromTranscript returns the current on-disk contents of the files the agent
// has READ or WRITTEN so far (its working set), keyed by the path string the agent
// used (so the pin is stable across turns). Relative paths are resolved against
// cwd (the session working directory). A file that can no longer be read is
// skipped: context assembly is best-effort and must never fail or stall a turn.
func filesFromTranscript(msgs []Message, cwd string) map[string]string {
	out := map[string]string{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type != "tool" {
				continue
			}
			switch p.Tool {
			case "read", "write", "edit", "patch":
			default:
				continue
			}
			raw, _ := p.Input["filePath"].(string)
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			path := raw
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			out[raw] = string(b)
		}
	}
	return out
}

// reinjectEverything is the naive per-turn payload this task ELIMINATES: the full
// content of every file in the working set re-sent each turn, plus the full
// verbatim transcript, then the directive. It exists only so a test can prove the
// bounded assemble() output is strictly smaller. It is not used on any runtime
// path.
func reinjectEverything(directive string, files map[string]string, msgs []Message) string {
	var b strings.Builder
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(&b, "### %s\n```\n%s\n```\n", p, files[p])
	}
	b.WriteString(renderTranscript("", msgs))
	b.WriteString(directive)
	return b.String()
}
