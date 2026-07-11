package audit

import (
	"fmt"
	"strings"
	"testing"
)

// mark renders a producer tool-call marker line exactly as
// internal/swarm.renderTranscript does: "**🔧 <tool>** `<input>`".
func mark(tool, input string) string {
	return fmt.Sprintf("**\U0001f527 %s** `%s`", tool, input)
}

func TestScanToolCalls(t *testing.T) {
	fence := "```"
	lines := []string{
		"submodule: beehive \u00b7 kind: work \u00b7 branch: bee-x \u00b7 model: m",
		"",
		"## assistant",
		"",
		mark("bash", "git log bee-x"),
		"",
		fence,
		"fatal: ambiguous argument 'bee-x': unknown revision or path not in the working tree",
		fence,
		"",
		mark("bash", "ls foo"),
		"",
		fence,
		"ls: cannot access 'foo': No such file or directory",
		fence,
		"",
		mark("bash", "beehive plan check"),
		"",
		fence,
		"beehive: unknown flag: --submodule",
		fence,
		"",
		mark("bash", "echo hi"),
		"",
		fence,
		"hi",
		fence,
		"",
		// A tool call whose OUTPUT quotes a PRIOR session's tool marker (the
		// session-audit series' charter). The quoted marker is INSIDE this call's
		// output fence, so it must NOT be counted as a separate call — only this
		// grep counts, and its benign output is not a failure.
		mark("bash", "grep marker notes.md"),
		"",
		fence,
		mark("bash", "git show bee-quoted"),
		"a benign quoted line",
		fence,
		"",
	}
	data := []byte(strings.Join(lines, "\n") + "\n")

	calls, fails, cats := scanToolCalls(data)
	if calls != 5 {
		t.Errorf("calls=%d want 5 (quoted marker must not count)", calls)
	}
	if fails != 3 {
		t.Errorf("fails=%d want 3", fails)
	}
	want := map[string]int{"missing-git-ref": 1, "path-missing": 1, "unknown-subcommand": 1}
	for cat, n := range want {
		if cats[cat] != n {
			t.Errorf("cats[%q]=%d want %d (full=%v)", cat, cats[cat], n, cats)
		}
	}
	if len(cats) != len(want) {
		t.Errorf("unexpected extra categories: %v", cats)
	}
}

func TestClassifyToolFail(t *testing.T) {
	cases := []struct {
		out, cat string
	}{
		{"", ""},
		{"clean output, all good\n", ""},
		{"fatal: couldn't find remote ref bee-old", "missing-git-ref"},
		{"beehive: unknown command \"lint\"", "unknown-subcommand"},
		{"bash: frobnicate: command not found", "command-not-found"},
		{"cat: /nope: No such file or directory", "path-missing"},
		{"open /root/x: permission denied", "permission-denied"},
		{"fatal: not a valid object name", "fatal-or-panic"},
		{"go: exit status 2", "nonzero-exit"},
	}
	for _, c := range cases {
		if got := classifyToolFail(c.out); got != c.cat {
			t.Errorf("classifyToolFail(%q)=%q want %q", c.out, got, c.cat)
		}
	}
}

func TestAggregateToolFails(t *testing.T) {
	sessions := []Session{
		{TaskID: "a", ToolCalls: 10, ToolFails: 3, ToolFailCats: map[string]int{"missing-git-ref": 2, "path-missing": 1}},
		{TaskID: "a", ToolCalls: 5, ToolFails: 1, ToolFailCats: map[string]int{"missing-git-ref": 1}},
		{TaskID: "b", ToolCalls: 4, ToolFails: 0},
		{TaskID: "c", ToolCalls: 0, ToolFails: 0},
	}
	s := AggregateToolFails(sessions)
	if s.Sessions != 3 { // c made no tool calls
		t.Errorf("sessions=%d want 3", s.Sessions)
	}
	if s.Calls != 19 || s.Fails != 4 {
		t.Errorf("calls=%d fails=%d want 19/4", s.Calls, s.Fails)
	}
	if s.ByCategory["missing-git-ref"] != 3 || s.ByCategory["path-missing"] != 1 {
		t.Errorf("bycat=%v", s.ByCategory)
	}
	if len(s.PerTask) == 0 || s.PerTask[0].TaskID != "a" || s.PerTask[0].Fails != 4 {
		t.Errorf("worst-first PerTask wrong: %+v", s.PerTask)
	}
}
