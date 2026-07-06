package swarm

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubSession returns a scripted, growing message list so the recorder renders a
// changing transcript across snapshots.
type stubSession struct{ msgs func() []Message }

func (s *stubSession) Prompt(ctx context.Context, text string) (string, error) { return "", nil }
func (s *stubSession) Messages(ctx context.Context) ([]Message, error)         { return s.msgs(), nil }
func (s *stubSession) Close() error                                            { return nil }

// TestRecorderStreamsLiveFile proves the recorder writes the rendered transcript
// to its live file on every change (real-time streaming beehived reads off disk)
// and skips rewriting when the transcript is unchanged. There is no per-poll git
// commit anymore — durability is a single end-of-session commit by the runner.
func TestRecorderStreamsLiveFile(t *testing.T) {
	dir := t.TempDir()
	text := "one"
	sess := &stubSession{msgs: func() []Message {
		return []Message{{ID: "m1", Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}}
	}}
	rc := &recorder{
		sess:    sess,
		path:    filepath.Join(dir, "s.md"),
		header:  "# s\n",
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	ctx := context.Background()

	rc.snapshot(ctx) // first content -> writes the live file
	b, err := os.ReadFile(rc.path)
	if err != nil {
		t.Fatalf("session file not written: %v", err)
	}
	if !contains(string(b), "one") {
		t.Fatalf("live file missing first content: %q", b)
	}
	mod1 := statModTime(t, rc.path)

	rc.snapshot(ctx) // identical transcript -> no rewrite (mtime unchanged)
	if got := statModTime(t, rc.path); got != mod1 {
		t.Fatalf("unchanged transcript rewrote the file (mtime changed)")
	}

	text = "one two" // transcript grows -> live file updates
	rc.snapshot(ctx)
	b, _ = os.ReadFile(rc.path)
	if !contains(string(b), "one two") {
		t.Fatalf("live file not updated on change: %q", b)
	}
}

func statModTime(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.ModTime().UnixNano()
}

// TestRecorderCoalescesCommits is the producer half of remote-host-session-view:
// while a (remote) honeybee streams its transcript, the recorder must NOT commit
// every changed poll — it coalesces them to commitIvl so a distributed hive does
// not spam origin with a commit+push per 700ms tick. Many transcript changes
// within one interval collapse to a single commit; a change after the interval
// commits again. Deterministic: the interval boundary is crossed by rewinding
// lastCommit, never by sleeping.
func TestRecorderCoalescesCommits(t *testing.T) {
	dir := t.TempDir()
	text := "a"
	sess := &stubSession{msgs: func() []Message {
		return []Message{{ID: "m1", Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}}
	}}
	commits := 0
	rc := &recorder{
		sess:      sess,
		path:      filepath.Join(dir, "s.md"),
		header:    "# s\n",
		toolSt:    map[string]string{},
		partLen:   map[string]int{},
		started:   map[string]bool{},
		commit:    func(context.Context) { commits++ },
		commitIvl: time.Hour, // one wide interval for the whole test
	}
	ctx := context.Background()

	// First change: lastCommit is zero -> commit fires.
	rc.snapshot(ctx)
	if commits != 1 {
		t.Fatalf("first change: commits=%d, want 1", commits)
	}
	// Several more changes inside the interval: each writes the live file but the
	// commit is throttled -> still exactly one commit (coalesced).
	for i := 0; i < 5; i++ {
		text += "-x"
		rc.snapshot(ctx)
	}
	if commits != 1 {
		t.Fatalf("changes within one interval must coalesce: commits=%d, want 1", commits)
	}
	// Cross the interval boundary -> the next change commits again.
	rc.lastCommit = time.Now().Add(-2 * time.Hour)
	text += "-y"
	rc.snapshot(ctx)
	if commits != 2 {
		t.Fatalf("change after the interval must commit: commits=%d, want 2", commits)
	}
	// An unchanged transcript never commits, even past the interval (no churn on
	// a quiet turn).
	rc.lastCommit = time.Now().Add(-2 * time.Hour)
	rc.snapshot(ctx)
	if commits != 2 {
		t.Fatalf("unchanged transcript must not commit: commits=%d, want 2", commits)
	}
}

// toolTurn is a two-poll message sequence for one turn: a bash tool call that goes
// running then completes with output, alongside a user prompt, a reasoning delta,
// and assistant text — the shape the recorder streams. running() is the first
// poll, completed() the second.
func toolTurn() (running, completed []Message) {
	base := func(status, output string) []Message {
		return []Message{
			{ID: "u1", Role: "user", Parts: []Part{{Type: "text", Text: "continue"}}},
			{ID: "a1", Role: "assistant", Parts: []Part{
				{ID: "r1", Type: "reasoning", Text: "planning the build"},
				{ID: "t1", Type: "text", Text: "Running the build."},
				{ID: "c1", Type: "tool", Tool: "bash", CallID: "c1", Status: status,
					Input: map[string]any{"command": "go build ./..."}, Output: output},
			}},
		}
	}
	return base("running", ""), base("completed", "VERBOSE_OUTPUT_BODY_LINE")
}

// TestRecorderConciseStreamsWithoutDebug is the binding acceptance for
// journal-activity-stream: a recorder with ONLY the always-on concise sink (no
// --debug tee) still streams per-turn tool-call activity, so a scheduled
// `honeybee` pass is observable live in the journal. It FAILS before the sink
// split, where a no-debug recorder emitted nothing at all. The concise stream must
// also stay concise: no full tool-OUTPUT body, no reasoning/text deltas, no
// user-prompt marker — those belong only to the --debug verbose tee.
func TestRecorderConciseStreamsWithoutDebug(t *testing.T) {
	var concise strings.Builder
	rc := &recorder{
		concise: &concise, // debug intentionally nil: the scheduled (no --debug) pass
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	running, completed := toolTurn()
	rc.streamActivity(running)
	rc.streamActivity(completed)

	got := concise.String()
	if !contains(got, "· bash go build ./...") {
		t.Fatalf("concise stream missing the tool-call start line (the whole point); got:\n%s", got)
	}
	if !contains(got, "✓ bash") {
		t.Fatalf("concise stream missing the tool-call completion line; got:\n%s", got)
	}
	// Concise stays concise — none of the verbose-only content leaks in.
	if contains(got, "VERBOSE_OUTPUT_BODY_LINE") {
		t.Fatalf("concise stream leaked the verbose tool OUTPUT body; got:\n%s", got)
	}
	if contains(got, "planning the build") || contains(got, "Running the build.") {
		t.Fatalf("concise stream leaked verbose reasoning/text deltas; got:\n%s", got)
	}
	if contains(got, "> continue") {
		t.Fatalf("concise stream leaked the verbose user-prompt marker; got:\n%s", got)
	}
}

// TestRecorderDebugSupersetsConcise proves the --debug tee is a clean SUPERSET of
// the concise stream with no line doubled: the verbose extras (user marker,
// reasoning/text deltas, tool OUTPUT body) land on the debug sink, the tool-call
// NAME lines land on the concise sink, the two are disjoint, and their union is
// the full verbose transcript. (In production both sinks are the same os.Stderr,
// so the union is exactly what the journal shows under --debug.)
func TestRecorderDebugSupersetsConcise(t *testing.T) {
	var concise, debug strings.Builder
	rc := &recorder{
		concise: &concise,
		debug:   &debug,
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	running, completed := toolTurn()
	rc.streamActivity(running)
	rc.streamActivity(completed)

	con, dbg := concise.String(), debug.String()
	// Concise: tool-call names only.
	if !contains(con, "· bash go build ./...") || !contains(con, "✓ bash") {
		t.Fatalf("concise missing tool-call name lines; got:\n%s", con)
	}
	if contains(con, "VERBOSE_OUTPUT_BODY_LINE") || contains(con, "planning the build") {
		t.Fatalf("concise leaked verbose content; got:\n%s", con)
	}
	// Debug: the verbose extras.
	for _, want := range []string{"VERBOSE_OUTPUT_BODY_LINE", "planning the build", "Running the build.", "> continue"} {
		if !contains(dbg, want) {
			t.Fatalf("debug tee missing verbose content %q; got:\n%s", want, dbg)
		}
	}
	// Disjoint: the tool-call name lines are NOT duplicated onto the debug sink, so
	// under --debug (concise==debug==stderr) each line appears exactly once.
	if contains(dbg, "· bash go build ./...") || contains(dbg, "✓ bash") {
		t.Fatalf("tool-call name line duplicated onto the debug sink; got:\n%s", dbg)
	}
	// Union = the full verbose transcript (the superset property).
	union := con + dbg
	for _, want := range []string{"· bash go build ./...", "✓ bash", "VERBOSE_OUTPUT_BODY_LINE", "planning the build"} {
		if !contains(union, want) {
			t.Fatalf("superset union missing %q", want)
		}
	}
}

// TestRecorderNoSinksNoStream proves a recorder with neither sink set (a plain
// unit test) streams nothing and touches no writer, so the durable transcript
// path is unaffected by the activity split.
func TestRecorderNoSinksNoStream(t *testing.T) {
	rc := &recorder{
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	running, completed := toolTurn()
	rc.streamActivity(running) // must not panic on nil sinks
	rc.streamActivity(completed)
}

// TestRecorderTranscriptByteIdenticalAcrossSinks proves the durable session
// transcript on disk is byte-identical no matter which live-activity sink is
// attached — the concise/debug split only tees to stderr and never perturbs the
// session file the runner commits to the session branch.
func TestRecorderTranscriptByteIdenticalAcrossSinks(t *testing.T) {
	_, completed := toolTurn()
	ctx := context.Background()
	newRec := func(concise, debug io.Writer) *recorder {
		return &recorder{
			path: filepath.Join(t.TempDir(), "s.md"), header: "# s\n",
			concise: concise, debug: debug,
			toolSt: map[string]string{}, partLen: map[string]int{}, started: map[string]bool{},
		}
	}
	render := func(rc *recorder) string {
		if err := rc.render(ctx, completed); err != nil {
			t.Fatalf("render: %v", err)
		}
		b, err := os.ReadFile(rc.path)
		if err != nil {
			t.Fatalf("read transcript: %v", err)
		}
		return string(b)
	}
	none := render(newRec(nil, nil))
	var cbuf, dbuf strings.Builder
	withConcise := render(newRec(&cbuf, nil))
	withDebug := render(newRec(nil, &dbuf))
	if none != withConcise || none != withDebug {
		t.Fatalf("transcript differs across sinks:\n none=%q\n concise=%q\n debug=%q", none, withConcise, withDebug)
	}
	// Sanity: the sinks really did receive activity, so we compared the with-stream
	// case, not two no-op renders.
	if cbuf.Len() == 0 || dbuf.Len() == 0 {
		t.Fatalf("expected activity on the attached sinks (concise=%d debug=%d)", cbuf.Len(), dbuf.Len())
	}
}
