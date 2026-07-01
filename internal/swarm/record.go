package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// recorder polls one opencode session and renders its full transcript (user
// prompts, assistant text, model reasoning, tool commands + output) to a file
// on disk. When debug is set it also tees live activity to a writer. A single
// recorder per honeybee means opencode is polled exactly once; beehived reads
// the file (a gitignored, real-time live transcript) instead of polling opencode
// itself. The file is NOT committed per poll — the runner makes one durable
// commit at session end — so streaming never touches git.
type recorder struct {
	sess   Session
	path   string // session worktree file streamed to the session branch
	header string
	debug  io.Writer

	lastMD string // last rendered transcript, to skip rewriting an unchanged file

	// commit, when set, persists the current transcript (commit to the isolated
	// session branch, push to remote when distributed). Throttled to commitIvl and
	// only run when the transcript changed, to bound commit churn while staying
	// near real-time. lastCommit tracks the throttle.
	commit     func(context.Context)
	commitIvl  time.Duration
	lastCommit time.Time

	// now, when set, replaces time.Now for the commit throttle so the coalescing
	// behavior is testable with a controlled clock. Nil == wall clock.
	now func() time.Time

	// debug-stream state (incremental, append-only diffing)
	toolSt  map[string]string // callID -> last status streamed
	partLen map[string]int    // "<kind>:<partID>" -> chars streamed
	started map[string]bool   // markers already emitted (user msg / reasoning lead)
}

func (rc *recorder) loop(ctx context.Context) {
	t := time.NewTicker(700 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = rc.snapshot(ctx)
		}
	}
}

// snapshot fetches the session, writes the rendered transcript to the worktree
// file, commits it to the isolated session branch (throttled), and (when debug)
// streams new activity. The recorder loop treats errors as transient; final
// session publication checks them so it never publishes a stale or missing file.
func (rc *recorder) snapshot(ctx context.Context) error {
	msgs, err := rc.sess.Messages(ctx)
	if err != nil {
		return err
	}
	md := renderTranscript(rc.header, msgs)
	if md == rc.lastMD {
		if rc.debug != nil {
			rc.streamDebug(msgs)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(rc.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(rc.path, []byte(md), 0o644); err != nil {
		return err
	}
	rc.lastMD = md
	if rc.debug != nil {
		rc.streamDebug(msgs)
	}
	// Stream to the session branch so beehived sees it near real time, throttled.
	if rc.commit != nil {
		now := rc.clock()
		if rc.lastCommit.IsZero() || now.Sub(rc.lastCommit) >= rc.commitIvl {
			rc.lastCommit = now
			rc.commit(ctx)
		}
	}
	return nil
}

// clock returns the recorder's time source (a test may override rc.now).
func (rc *recorder) clock() time.Time {
	if rc.now != nil {
		return rc.now()
	}
	return time.Now()
}

// appendWarning records an abort notice at the end of the session file so it is
// visible in the UI and committed. Called only after the recorder goroutine has
// stopped (no concurrent writer to rc.path).
func (rc *recorder) appendWarning(msg string) error {
	if err := os.MkdirAll(filepath.Dir(rc.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(rc.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n## \u26a0\ufe0f warning\n\n%s\n", msg)
	return err
}

// renderTranscript produces the full markdown transcript for the session file
// and the web UI. Everything is recorded: reasoning, tool input, tool output.
func renderTranscript(header string, msgs []Message) string {
	var b strings.Builder
	b.WriteString(header)
	for _, m := range msgs {
		fmt.Fprintf(&b, "\n## %s\n\n", m.Role)
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				if strings.TrimSpace(p.Text) != "" {
					b.WriteString(p.Text + "\n\n")
				}
			case "reasoning":
				if strings.TrimSpace(p.Text) != "" {
					q := strings.ReplaceAll(strings.TrimRight(p.Text, "\n"), "\n", "\n> ")
					b.WriteString("> \U0001f4ad " + q + "\n\n")
				}
			case "tool":
				fmt.Fprintf(&b, "**\U0001f527 %s** `%s`\n\n", p.Tool, inputSummary(p.Tool, p.Input))
				switch {
				case p.Status == "error" && strings.TrimSpace(p.Error) != "":
					b.WriteString("```\n" + p.Error + "\n```\n\n")
				case strings.TrimSpace(p.Output) != "":
					b.WriteString("```\n" + p.Output + "\n```\n\n")
				}
			}
		}
	}
	return b.String()
}

// inputSummary renders a tool's arguments compactly: the bash command, the file
// path read/written, the search pattern, etc., falling back to compact JSON.
func inputSummary(tool string, in map[string]any) string {
	get := func(k string) string {
		if v, ok := in[k]; ok {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}
	switch tool {
	case "bash":
		return get("command")
	case "read", "write", "edit", "patch":
		return get("filePath")
	case "list":
		return get("path")
	case "glob", "grep":
		if d := get("path"); d != "" {
			return get("pattern") + " in " + d
		}
		return get("pattern")
	case "webfetch":
		return get("url")
	default:
		if len(in) == 0 {
			return ""
		}
		j, _ := json.Marshal(in)
		s := string(j)
		if len(s) > 200 {
			s = s[:200] + "\u2026"
		}
		return s
	}
}

// streamDebug emits only newly-appeared content since the last poll to the debug
// writer: user prompt markers, assistant text + reasoning deltas, and tool calls
// with their command and (truncated) output.
func (rc *recorder) streamDebug(msgs []Message) {
	for _, m := range msgs {
		if m.Role == "user" {
			if !rc.started["u:"+m.ID] {
				rc.started["u:"+m.ID] = true
				fmt.Fprintf(rc.debug, "\n> %s\n", firstLine(messageText(m)))
			}
			continue
		}
		for _, p := range m.Parts {
			switch p.Type {
			case "reasoning":
				key := "r:" + p.ID
				if !rc.started[key] {
					rc.started[key] = true
					fmt.Fprint(rc.debug, "\n\U0001f4ad ")
				}
				rc.streamDelta(key, p.Text)
			case "text":
				rc.streamDelta("t:"+p.ID, p.Text)
			case "tool":
				if rc.toolSt[p.CallID] == p.Status {
					continue
				}
				rc.toolSt[p.CallID] = p.Status
				switch p.Status {
				case "pending", "running":
					fmt.Fprintf(rc.debug, "\n  \u00b7 %s %s\n", p.Tool, inputSummary(p.Tool, p.Input))
				case "completed":
					if out := strings.TrimRight(p.Output, "\n"); strings.TrimSpace(out) != "" {
						if len(out) > 2000 {
							out = out[:2000] + "\n    \u2026(truncated; full output in session file)"
						}
						fmt.Fprintln(rc.debug, indent(out))
					}
					fmt.Fprintf(rc.debug, "  \u2713 %s\n", p.Tool)
				case "error":
					fmt.Fprintf(rc.debug, "  \u2717 %s: %s\n", p.Tool, firstLine(p.Error))
				}
			}
		}
	}
}

func (rc *recorder) streamDelta(key, full string) {
	n := rc.partLen[key]
	if n > len(full) {
		n = len(full)
	}
	if len(full) > n {
		fmt.Fprint(rc.debug, full[n:])
		rc.partLen[key] = len(full)
	}
}

func messageText(m Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "\u2026"
	}
	return s
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
