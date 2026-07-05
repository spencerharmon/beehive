package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
)

// TestResolveFileContextDistinct: the resolver yields a distinct, file-appropriate
// preamble per path class (matched by basename, so a submodule-qualified path
// resolves the same as the bare basename), and an ordinary file falls to the
// generic default. This is the "distinct preambles per path" acceptance.
func TestResolveFileContextDistinct(t *testing.T) {
	cases := map[string]struct {
		path        string
		wantSubs    []string // must all appear
		wantNotSubs []string // must NOT appear
	}{
		"roi":    {"submodules/alpha/ROI.md", []string{"ROI.md", "human-owned", "FORBIDDEN"}, nil},
		"plan":   {"submodules/alpha/PLAN.md", []string{"PLAN.md", "NEEDS-REVIEW", "Beehive-ROI", "state machine"}, nil},
		"rules":  {"submodules/alpha/RULES.md", []string{"RULES.md", "ADDITIVE", "AGENTS.md"}, nil},
		"agents": {"AGENTS.md", []string{"AGENTS.md", "operating guide"}, nil},
		"infra":  {"submodules/alpha/INFRASTRUCTURE.md", []string{"INFRASTRUCTURE.md", "internal/artifacts"}, nil},
		"arti":   {"ARTIFACTS.md", []string{"ARTIFACTS.md", "internal/artifacts"}, nil},
		"code":   {"internal/web/web.go", []string{"ordinary file"}, []string{"FORBIDDEN", "PLAN.md", "ROI.md"}},
		"deep":   {"a/b/c/notes.txt", []string{"ordinary file"}, []string{"FORBIDDEN"}},
	}
	got := map[string]string{}
	for name, c := range cases {
		p := resolveFileContext(c.path)
		if strings.TrimSpace(p) == "" {
			t.Fatalf("%s: empty preamble for %q", name, c.path)
		}
		for _, sub := range c.wantSubs {
			if !strings.Contains(p, sub) {
				t.Errorf("%s (%q): preamble missing %q:\n%s", name, c.path, sub, p)
			}
		}
		for _, sub := range c.wantNotSubs {
			if strings.Contains(p, sub) {
				t.Errorf("%s (%q): preamble should not contain %q:\n%s", name, c.path, sub, p)
			}
		}
		got[name] = p
	}
	// The named classes must be pairwise distinct (code and deep both map to the
	// default, so they are expected equal — compare only the named-rule classes).
	named := []string{"roi", "plan", "rules", "agents", "infra", "arti", "code"}
	for i := 0; i < len(named); i++ {
		for j := i + 1; j < len(named); j++ {
			if got[named[i]] == got[named[j]] {
				t.Errorf("preambles for %s and %s are identical; expected distinct", named[i], named[j])
			}
		}
	}
	// A basename match is independent of directory depth: submodule-qualified and
	// bare ROI.md resolve to the same rule.
	if resolveFileContext("submodules/alpha/ROI.md") != resolveFileContext("ROI.md") {
		t.Error("ROI.md rule should match regardless of directory")
	}
}

// TestChatSystemPromptSeedsFileRules: the system prompt built for a path carries
// that file's rules, AND the same prompt is what is seeded into the opencode
// session (captured end-to-end via the fake client). This is the "seeded prompt
// contains the target's rules" acceptance.
func TestChatSystemPromptSeedsFileRules(t *testing.T) {
	// White-box: the prompt for a path embeds the resolved preamble.
	for _, tc := range []struct{ path, want string }{
		{"submodules/alpha/ROI.md", "FORBIDDEN"},
		{"submodules/alpha/PLAN.md", "NEEDS-REVIEW"},
		{"submodules/alpha/notes.md", "ordinary file"},
	} {
		sp := chatSystemPrompt(tc.path)
		if !strings.Contains(sp, tc.path) {
			t.Errorf("system prompt for %q missing the path", tc.path)
		}
		if !strings.Contains(sp, tc.want) {
			t.Errorf("system prompt for %q missing rules token %q:\n%s", tc.path, tc.want, sp)
		}
	}

	// End-to-end: opening a PLAN.md session and running a turn seeds the opencode
	// session with the PLAN rules (the exact string the resolver produces).
	fc := &fakeChatClient{reply: "ok"}
	s, _ := chatFixtureClient(t, fc)
	ctx := context.Background()
	sess, err := s.chat.open(ctx, "submodules/alpha/PLAN.md")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sess.sys != chatSystemPrompt("submodules/alpha/PLAN.md") {
		t.Fatalf("session system prompt not seeded from the resolver")
	}
	if err := sess.chat(ctx, "hello"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(fc.system, "NEEDS-REVIEW") || !strings.Contains(fc.system, "Beehive-ROI") {
		t.Fatalf("opencode session was not seeded with PLAN rules; got system:\n%s", fc.system)
	}
	// A different target seeds different rules through the same generic surface.
	fc2 := &fakeChatClient{reply: "ok"}
	s2, _ := chatFixtureClient(t, fc2)
	roiSess, err := s2.chat.open(ctx, "submodules/alpha/ROI.md")
	if err != nil {
		t.Fatalf("open roi: %v", err)
	}
	if err := roiSess.chat(ctx, "hello"); err != nil {
		t.Fatalf("chat roi: %v", err)
	}
	if !strings.Contains(fc2.system, "FORBIDDEN") {
		t.Fatalf("ROI session not seeded with ROI rules; got system:\n%s", fc2.system)
	}
	if strings.Contains(fc2.system, "NEEDS-REVIEW") {
		t.Fatalf("ROI session leaked PLAN rules; got system:\n%s", fc2.system)
	}
}

// TestChatEditPlanRoundTrips: editing PLAN.md through the chat-diff surface still
// yields a file that round-trips through plan.Parse — the propose -> normalize ->
// approve path preserves the strict line format. This is the "editing PLAN.md
// still round-trips plan.Parse" acceptance.
func TestChatEditPlanRoundTrips(t *testing.T) {
	newPlan := "<!-- Beehive-ROI: abc123 -->\n# Plan\n\n" +
		"## demo [TODO] <!-- attempts=0 deps= -->\nA demo task body."
	fc := &fakeChatClient{reply: proposeReply("Added a demo task.", newPlan)}
	s, _ := chatFixtureClient(t, fc)
	ctx := context.Background()
	path := "submodules/alpha/PLAN.md"

	sess, err := s.chat.open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.chat(ctx, "add a demo task"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, ok := sess.pending(); !ok {
		t.Fatalf("expected a pending PLAN.md proposal (err=%q)", sess.errText())
	}
	if err := sess.approve(ctx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(sess.wtPath, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read committed PLAN.md: %v", err)
	}
	// The surface must not mangle the bytes: exactly the proposal + one trailing LF.
	if string(raw) != newPlan+"\n" {
		t.Fatalf("committed PLAN.md not byte-faithful:\n%q", string(raw))
	}
	// And it must parse cleanly through the real plan parser.
	p, err := plan.Parse(string(raw))
	if err != nil {
		t.Fatalf("plan.Parse failed on the edited PLAN.md: %v", err)
	}
	if p.ROI != "abc123" {
		t.Errorf("ROI stamp lost: %q", p.ROI)
	}
	if len(p.Tasks) != 1 || p.Tasks[0].ID != "demo" || p.Tasks[0].Status != plan.StatusTODO {
		t.Fatalf("parsed tasks wrong: %+v", p.Tasks)
	}
	// Idempotent round-trip: re-serialize and re-parse to the same shape.
	if p2, err := plan.Parse(p.String()); err != nil || len(p2.Tasks) != 1 || p2.Tasks[0].ID != "demo" {
		t.Fatalf("plan did not round-trip through String()->Parse: err=%v tasks=%+v", err, p2)
	}
}

// TestPerFileEditLinksRouteToChatDiff: the per-file "edit with AI" links (the ROI
// human editor and the dashboard ROI/infrastructure links) route into the generic
// chat-diff handler (/edit?path=), not the legacy single-file editor (/edit?file=).
func TestPerFileEditLinksRouteToChatDiff(t *testing.T) {
	s, _ := setup(t)

	roi := get(t, s, "/roi/alpha").Body.String()
	if !strings.Contains(roi, `/edit?path=submodules/alpha/ROI.md`) {
		t.Errorf("roi editor should link into the chat-diff handler (?path=):\n%s", roi)
	}
	if strings.Contains(roi, `/edit?file=`) {
		t.Errorf("roi editor still routes a per-file link to the legacy editor (?file=):\n%s", roi)
	}

	dash := get(t, s, "/").Body.String()
	for _, want := range []string{
		`/edit?path=submodules/alpha/ROI.md`,
		`/edit?path=INFRASTRUCTURE.md`,
	} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard missing chat-diff link %q:\n%s", want, dash)
		}
	}
	if strings.Contains(dash, `/edit?file=`) {
		t.Errorf("dashboard still routes a per-file link to the legacy editor (?file=):\n%s", dash)
	}
}
