package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// recorder polls one opencode session and renders its full transcript (user
// prompts, assistant text, model reasoning, tool commands + output) to a file
// on disk. It tees live activity to two DISJOINT sinks:
//
//   - concise (always-on): a terse per-turn activity log — tool-call names and
//     stream-health notices — so every scheduled pass is observable live in the
//     journal (journalctl -t honeybee) even with no --debug flag.
//   - debug (--debug only): the verbose full-transcript tee — model reasoning and
//     assistant-text deltas, user-prompt markers, and full tool OUTPUT bodies.
//
// The two sinks emit disjoint line sets, so with --debug (both set to the same
// stderr) the output is a clean SUPERSET of the concise stream, no line doubled.
// A single recorder per honeybee means opencode is polled exactly once; beehived
// reads the file (a gitignored, real-time live transcript) instead of polling
// opencode itself. The file is NOT committed per poll — the runner makes one
// durable commit at session end — so streaming never touches git.
type recorder struct {
	sess    Session
	path    string // session worktree file streamed to the session branch
	header  string
	concise io.Writer // always-on: terse per-turn activity (tool-call names, stream health)
	debug   io.Writer // --debug only: verbose full-transcript tee (reasoning/text deltas, tool output bodies)

	lastMD string // last rendered transcript, to skip rewriting an unchanged file

	// commit, when set, persists the current transcript (commit to the isolated
	// session branch, push to remote when distributed). Throttled to commitIvl and
	// only run when the transcript changed, to bound commit churn while staying
	// near real-time. lastCommit tracks the throttle.
	commit     func(context.Context)
	commitIvl  time.Duration
	lastCommit time.Time

	// live-stream state (incremental, append-only diffing) shared by both sinks
	toolSt  map[string]string // callID -> last status streamed
	partLen map[string]int    // "<kind>:<partID>" -> chars streamed
	started map[string]bool   // markers already emitted (user msg / reasoning lead)

	// pollIvl is the fallback poll cadence (recorder.pollLoop). Zero = the 700ms
	// default; tests set a small value so the fallback path advances quickly.
	pollIvl time.Duration

	// onToolDone, when set, is invoked once per tool call that reaches its
	// "completed" state, on the recorder's own goroutine (stream OR poll path —
	// both funnel through streamActivity). The runner wires it to the mid-turn
	// completionGuard: a committed status flip lands as a completed tool call, so
	// this is the earliest deterministic point the runner can observe delivery and
	// hard-stop the turn before the agent is solicited for a trailing tool call.
	// Kept out of the transcript-render path proper so a nil hook is byte-identical
	// to the pre-guard recorder.
	onToolDone func()
}

// loop drives the recorder for the life of the session. It prefers streaming:
// when the session can consume opencode's event channel, the transcript is
// rendered as tokens arrive (near real time) instead of at the poll cadence. It
// falls back to polling when streaming is unsupported (ErrNoStream, e.g. a test
// mock or an older server) or if a live stream drops mid-session, so recording
// always continues. ctx cancellation (session end) returns from either path.
func (rc *recorder) loop(ctx context.Context) {
	if st, ok := rc.sess.(streamer); ok {
		onUpdate := func(msgs []Message) { _ = rc.render(ctx, msgs) }
		onIdle := func() { rc.flush(ctx) }
		err := st.stream(ctx, onUpdate, onIdle)
		if ctx.Err() != nil {
			return // session ended: stop, no fallback
		}
		// Stream-health notices are concise activity (they explain a stalled
		// transcript), so they go to the always-on sink, not the debug-only tee.
		if errors.Is(err, ErrNoStream) {
			rc.activity("\n[recorder] opencode event stream unavailable; polling transcript\n")
		} else if err != nil {
			rc.activity(fmt.Sprintf("\n[recorder] event stream ended (%v); falling back to polling\n", err))
		}
		// Fall through to polling for the rest of the session.
	}
	rc.pollLoop(ctx)
}

// pollLoop renders the transcript by polling the message list on a fixed cadence.
// Used when streaming is unavailable; also the historical default path.
func (rc *recorder) pollLoop(ctx context.Context) {
	iv := rc.pollIvl
	if iv <= 0 {
		iv = 700 * time.Millisecond
	}
	t := time.NewTicker(iv)
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

// flush forces an immediate commit of the current transcript, bypassing the
// throttle. Called when a streamed turn goes idle so a completed turn lands on
// the session branch promptly instead of waiting out the commit interval.
func (rc *recorder) flush(ctx context.Context) {
	if rc.commit != nil {
		rc.lastCommit = time.Now()
		rc.commit(ctx)
	}
}

// snapshot fetches the session by polling and renders it. The final authoritative
// flush in the runner's finish() calls this after the recorder goroutine stops,
// so the poll message list is the source of truth at session end even when the
// live transcript was streamed.
func (rc *recorder) snapshot(ctx context.Context) error {
	msgs, err := rc.sess.Messages(ctx)
	if err != nil {
		return err
	}
	return rc.render(ctx, msgs)
}

// render writes the rendered transcript for msgs to the worktree file, commits it
// to the isolated session branch (throttled), and streams new live activity (the
// always-on concise sink plus, under --debug, the verbose tee). Shared by the poll
// path (snapshot) and the streaming path (loop's onUpdate). The recorder loop
// treats errors as transient; final session publication checks them so it never
// publishes a stale or missing file.
func (rc *recorder) render(ctx context.Context, msgs []Message) error {
	md := renderTranscript(rc.header, msgs)
	if md == rc.lastMD {
		rc.streamActivity(msgs)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(rc.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(rc.path, []byte(md), 0o644); err != nil {
		return err
	}
	rc.lastMD = md
	rc.streamActivity(msgs)
	// Stream to the session branch so beehived sees it near real time, throttled.
	if rc.commit != nil {
		now := time.Now()
		if rc.lastCommit.IsZero() || now.Sub(rc.lastCommit) >= rc.commitIvl {
			rc.lastCommit = now
			rc.commit(ctx)
		}
	}
	return nil
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

// activity writes a concise, always-on activity line to the journal. It prefers
// the concise sink (set on every real pass); with only the debug tee configured
// (a verbose-only test) it falls back to that so concise lines still appear as
// part of the superset. A nil-nil recorder (a plain unit test with no sinks) is a
// no-op.
func (rc *recorder) activity(s string) {
	w := rc.concise
	if w == nil {
		w = rc.debug
	}
	if w != nil {
		fmt.Fprint(w, s)
	}
}

// streamActivity emits only newly-appeared content since the last poll, split
// across two disjoint sinks so --debug is a clean superset of the always-on
// journal stream (see the recorder doc comment):
//
//   - concise (always, via rc.activity): tool-call NAME lines — the pending/done/
//     error markers with the tool and its input summary — so every scheduled pass
//     shows what the agent is doing even without --debug.
//   - debug (--debug only, direct to rc.debug): the verbose extras — user-prompt
//     markers, assistant-text + model-reasoning deltas, and full tool OUTPUT
//     bodies. These never duplicate a concise line.
//
// With neither sink configured it is a no-op, so a plain unit test's transcript
// stays byte-identical — UNLESS the completion guard is wired (onToolDone set), in
// which case it still walks the parts to fire that hook on a tool completion
// (writing nothing, since rc.activity/rc.debug guard their own nil sinks). This
// keeps the mid-turn hard stop working on a host that runs without --debug and
// without an explicitly-set concise sink.
func (rc *recorder) streamActivity(msgs []Message) {
	if rc.concise == nil && rc.debug == nil && rc.onToolDone == nil {
		return
	}
	for _, m := range msgs {
		if m.Role == "user" {
			if rc.debug != nil && !rc.started["u:"+m.ID] {
				rc.started["u:"+m.ID] = true
				fmt.Fprintf(rc.debug, "\n> %s\n", firstLine(messageText(m)))
			}
			continue
		}
		for _, p := range m.Parts {
			switch p.Type {
			case "reasoning":
				if rc.debug == nil {
					continue
				}
				key := "r:" + p.ID
				if !rc.started[key] {
					rc.started[key] = true
					fmt.Fprint(rc.debug, "\n\U0001f4ad ")
				}
				rc.streamDelta(key, p.Text)
			case "text":
				if rc.debug == nil {
					continue
				}
				rc.streamDelta("t:"+p.ID, p.Text)
			case "tool":
				if rc.toolSt[p.CallID] == p.Status {
					continue
				}
				rc.toolSt[p.CallID] = p.Status
				switch p.Status {
				case "pending", "running":
					// Concise tool-call name + input summary (always-on).
					rc.activity(fmt.Sprintf("\n  \u00b7 %s %s\n", p.Tool, inputSummary(p.Tool, p.Input)))
				case "completed":
					// Full output body: verbose tee only. The ✓ marker is concise.
					if rc.debug != nil {
						if out := strings.TrimRight(p.Output, "\n"); strings.TrimSpace(out) != "" {
							if len(out) > 2000 {
								out = out[:2000] + "\n    \u2026(truncated; full output in session file)"
							}
							fmt.Fprintln(rc.debug, indent(out))
						}
					}
					rc.activity(fmt.Sprintf("  \u2713 %s\n", p.Tool))
					// A tool just completed: the agent may have committed the terminal
					// status flip + change doc. Signal the completion guard (if wired) so
					// it can poll the committed predicate and hard-stop the turn the
					// instant delivery is observed — before the next tool call. Fired only
					// on the completed transition (toolSt-deduped above), never per token.
					if rc.onToolDone != nil {
						rc.onToolDone()
					}
				case "error":
					rc.activity(fmt.Sprintf("  \u2717 %s: %s\n", p.Tool, firstLine(p.Error)))
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
