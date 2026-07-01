package swarm

import (
	"fmt"
	"sort"
	"strings"
)

// turnContext bounds what a honeybee re-sends across the turns of ONE session.
//
// Naively, every "continue" turn re-includes material the model has already
// seen: the same files re-read in full and the entire prior transcript
// re-injected, so cost grows with turn count even when the pass is making small,
// local edits. turnContext cuts both streams (ROI Priority 1 — cut tokens per
// honeybee; levers "Working set / retrieval" and "History"):
//
//   - Working set: it PINS the content of every file the agent has already been
//     shown. On the next turn an UNCHANGED file is not re-sent in full (only a
//     one-line pinned no-op); a CHANGED file is sent as a line diff; a NEW file
//     is sent in full (and pinned). The per-turn file payload is bounded to
//     new + changed content, never "everything seen so far".
//   - History: it folds each processed message into a bounded ROLLING SUMMARY
//     (the decision headline + tool ACTIONS, dropping verbatim tool OUTPUT and
//     file bodies) instead of re-injecting the full transcript.
//
// The result is a per-turn feed of (new/changed content + summary). It is a
// pure, deterministic compressor: the change doc and the recorded session
// transcript remain the authoritative full history, so bounding here never loses
// the record. Correctness of the pass outranks the byte cut, so the runner ships
// it behind an inert-by-default knob (see Runner.CompactContext).
type turnContext struct {
	seen       map[string]string // path -> content last shown to the agent (pinned)
	summary    []string          // one digest line per processed message (oldest first)
	processed  map[string]bool   // message IDs already folded into summary (roll cursor)
	maxSummary int               // byte cap on the rendered summary; <=0 uses the default
}

// defaultSummaryCap bounds the rolling summary when the caller passes 0. Large
// enough to retain the recent decisions/actions a turn needs to continue, small
// enough that the summary never approaches the size of the verbatim transcript.
const defaultSummaryCap = 2000

func newTurnContext(maxSummary int) *turnContext {
	if maxSummary <= 0 {
		maxSummary = defaultSummaryCap
	}
	return &turnContext{
		seen:       map[string]string{},
		processed:  map[string]bool{},
		maxSummary: maxSummary,
	}
}

// observe folds any not-yet-processed messages into the rolling summary. It is a
// cursor over message IDs, so calling it every turn rolls the summary forward
// without re-digesting old turns (and re-observing the same messages is a no-op).
// Only the digest (headline + tool actions) is kept; verbatim scrollback (tool
// output, file bodies) is dropped here and never re-injected.
func (tc *turnContext) observe(msgs []Message) {
	for _, m := range msgs {
		if m.ID == "" || tc.processed[m.ID] {
			continue
		}
		tc.processed[m.ID] = true
		if line := digestMessage(m); line != "" {
			tc.summary = append(tc.summary, line)
		}
	}
}

// digestMessage renders one message as a single terse summary line: the role,
// the first line of its text (the decision/output to preserve), and the tool
// ACTIONS it took (name + compact input) — deliberately WITHOUT the tool output
// or any file body, which is the verbatim scrollback the summary exists to drop.
func digestMessage(m Message) string {
	var parts []string
	if t := strings.TrimSpace(firstLine(messageText(m))); t != "" {
		parts = append(parts, t)
	}
	var tools []string
	for _, p := range m.Parts {
		if p.Type != "tool" || p.Tool == "" {
			continue
		}
		if in := strings.TrimSpace(inputSummary(p.Tool, p.Input)); in != "" {
			tools = append(tools, p.Tool+" "+firstLine(in))
		} else {
			tools = append(tools, p.Tool)
		}
	}
	if len(tools) > 0 {
		parts = append(parts, "tools: "+strings.Join(tools, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return m.Role + ": " + strings.Join(parts, "; ")
}

// renderSummary joins the digest lines newest-last, keeping the most RECENT lines
// within the byte cap (older lines are elided first, marked with a leading
// notice). Returns "" when there is nothing to summarize.
func (tc *turnContext) renderSummary() string {
	if len(tc.summary) == 0 {
		return ""
	}
	var kept []string
	total := 0
	elided := false
	for i := len(tc.summary) - 1; i >= 0; i-- {
		line := tc.summary[i]
		if len(kept) > 0 && total+len(line)+1 > tc.maxSummary {
			elided = true
			break
		}
		kept = append(kept, line)
		total += len(line) + 1
	}
	// kept is newest-first; reverse to chronological order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	if elided {
		kept = append([]string{"(earlier turns elided)"}, kept...)
	}
	return strings.Join(kept, "\n")
}

// assemble builds the bounded per-turn feed for a "continue" turn from the CURRENT
// working set (path -> content the agent now holds). It opens with the literal
// "continue" instruction (so the turn boundary is byte-identical to the bare
// default), then the rolling summary, then a working-set section carrying ONLY
// new/changed files: a new file in full, a changed file as a line diff, an
// unchanged file as a one-line pinned no-op (never re-embedded). assemble mutates
// the pin set: afterwards seen holds the content just shown for every path.
func (tc *turnContext) assemble(files map[string]string) string {
	var b strings.Builder
	b.WriteString("continue")
	if s := tc.renderSummary(); s != "" {
		b.WriteString("\n\n## prior turns (rolling summary)\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var body strings.Builder
	for _, path := range paths {
		cur := files[path]
		prev, ok := tc.seen[path]
		switch {
		case !ok:
			fmt.Fprintf(&body, "### %s (new, %d bytes)\n```\n%s\n```\n", path, len(cur), cur)
		case prev == cur:
			fmt.Fprintf(&body, "### %s (unchanged, %d bytes — pinned, not re-sent)\n", path, len(cur))
		default:
			fmt.Fprintf(&body, "### %s (changed)\n```diff\n%s```\n", path, lineDiff(prev, cur))
		}
		tc.seen[path] = cur
	}
	if body.Len() > 0 {
		b.WriteString("\n## working-set files (diffs only; unchanged files are pinned)\n")
		b.WriteString(body.String())
	}
	return b.String()
}

// lineDiff renders a compact line diff of old -> new: shared leading and trailing
// lines are collapsed to a count marker (not re-sent) and only the changed middle
// is emitted (removed lines prefixed "-", added lines "+"). Deterministic and
// dependency-free; not a minimal edit script, but it guarantees the unchanged
// head/tail bytes are not re-sent, which is the token win.
func lineDiff(oldS, newS string) string {
	o := strings.Split(oldS, "\n")
	n := strings.Split(newS, "\n")
	p := 0
	for p < len(o) && p < len(n) && o[p] == n[p] {
		p++
	}
	s := 0
	for s < len(o)-p && s < len(n)-p && o[len(o)-1-s] == n[len(n)-1-s] {
		s++
	}
	var b strings.Builder
	if p > 0 {
		fmt.Fprintf(&b, "@@ %d unchanged leading line(s) @@\n", p)
	}
	for i := p; i < len(o)-s; i++ {
		b.WriteString("-" + o[i] + "\n")
	}
	for i := p; i < len(n)-s; i++ {
		b.WriteString("+" + n[i] + "\n")
	}
	if s > 0 {
		fmt.Fprintf(&b, "@@ %d unchanged trailing line(s) @@\n", s)
	}
	return b.String()
}

// filesFromMessages extracts the working set the agent has materialized in this
// session: for each file it read/wrote/edited, the LATEST content it was shown
// (a read's output, or a write/edit body). Later turns override earlier ones so
// the map holds the newest content per path — exactly what assemble pins and
// diffs against.
func filesFromMessages(msgs []Message) map[string]string {
	files := map[string]string{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type != "tool" {
				continue
			}
			path := stringArg(p.Input, "filePath")
			if path == "" {
				continue
			}
			switch p.Tool {
			case "read":
				if p.Status == "completed" && strings.TrimSpace(p.Output) != "" {
					files[path] = p.Output
				}
			case "write", "edit", "patch":
				if body := stringArg(p.Input, "content"); body != "" {
					files[path] = body
				}
			}
		}
	}
	return files
}

func stringArg(in map[string]any, key string) string {
	if in == nil {
		return ""
	}
	if v, ok := in[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
