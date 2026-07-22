package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spencerharmon/beehive/prompts"
)

// TestTrimProtocolWorkDropsBoilerplateKeepsRules proves the lean Work protocol is
// materially smaller than the full HONEYBEE.md, drops the managed boilerplate an
// agent never acts on and the other-kind protocol steps, yet retains every
// absolute rule and its own Work step.
func TestTrimProtocolWorkDropsBoilerplateKeepsRules(t *testing.T) {
	full := prompts.Honeybee
	lean := trimProtocol(full, selectt.Work)

	if len(lean) >= len(full) {
		t.Fatalf("lean Work protocol (%d bytes) is not smaller than the full protocol (%d bytes)", len(lean), len(full))
	}
	// Absolute rules + shared sections retained (incl. the ROI ban, the commit
	// stamp rule, the claim model, tooling, and the turn loop).
	for _, must := range []string{
		"You are a honeybee", // the framing paragraph stays
		"## Absolute rules",
		"NEVER edit `ROI.md`",
		"Beehive: <task-id> <doc-path>",
		"## Claim model",
		"## Tooling",
		"## Turn loop",
		"## Work task", // its own role section
	} {
		if !strings.Contains(lean, must) {
			t.Errorf("lean Work protocol dropped required content %q", must)
		}
	}
	// Managed boilerplate + other-kind prose removed.
	for _, gone := range []string{
		"## Topology",
		"## You were started",
		"authoritative for protocol", // intro governance paragraph
		"## Reconcile task",          // reconcile role section
		"## Arbitration task",        // arbitration role section
		"## Review task",             // review role section
	} {
		if strings.Contains(lean, gone) {
			t.Errorf("lean Work protocol still carries boilerplate/other-kind text %q", gone)
		}
	}
}

// TestTrimProtocolKeepsOwnKindStepDropsOthers proves each kind keeps exactly its
// own protocol step and never leaks another kind's prose, while the shared steps
// and absolute rules survive for every kind — so a review/arbitration pass still
// receives its kind-specific prose and no cross-cutting rule is lost.
func TestTrimProtocolKeepsOwnKindStepDropsOthers(t *testing.T) {
	full := prompts.Honeybee
	cases := []struct {
		kind  selectt.Kind
		keep  string
		drops []string
	}{
		{selectt.Work, "## Work task", []string{"## Review task", "## Arbitration task", "## Reconcile task"}},
		{selectt.Review, "## Review task", []string{"## Work task", "## Arbitration task", "## Reconcile task"}},
		{selectt.Arbitrate, "## Arbitration task", []string{"## Work task", "## Review task", "## Reconcile task"}},
		{selectt.Reconcile, "## Reconcile task", []string{"## Work task", "## Review task", "## Arbitration task"}},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			lean := trimProtocol(full, c.kind)
			if !strings.Contains(lean, c.keep) {
				t.Errorf("%s: dropped its own protocol step %q", c.kind, c.keep)
			}
			for _, d := range c.drops {
				if strings.Contains(lean, d) {
					t.Errorf("%s: leaked another kind's step %q", c.kind, d)
				}
			}
			// Shared rules survive regardless of kind.
			for _, shared := range []string{"NEVER edit `ROI.md`", "Human escalation", "## Absolute rules"} {
				if !strings.Contains(lean, shared) {
					t.Errorf("%s: lost a shared rule %q", c.kind, shared)
				}
			}
		})
	}
}

// TestTrimProtocolPassesThroughUnrecognized proves the SAFETY fallback: a protocol
// that lacks the absolute-rules anchor is returned verbatim (never trimmed blind),
// and a custom section under a recognized protocol is KEPT (only positively
// identified boilerplate is dropped) so an operator's added rule is not lost.
func TestTrimProtocolPassesThroughUnrecognized(t *testing.T) {
	custom := "# Custom protocol\n\n## House rules\n- do the thing\n"
	if got := trimProtocol(custom, selectt.Work); got != custom {
		t.Fatalf("unrecognized protocol must pass through unchanged;\n got: %q\nwant: %q", got, custom)
	}

	withAnchor := "# P\n\n## Absolute rules\n- NEVER edit `ROI.md`\n\n## Topology (read once)\ndrop me\n\n## Site override\n- keep this custom rule\n"
	lean := trimProtocol(withAnchor, selectt.Work)
	if strings.Contains(lean, "## Topology") || strings.Contains(lean, "drop me") {
		t.Errorf("recognized boilerplate section was not dropped:\n%s", lean)
	}
	if !strings.Contains(lean, "## Site override") || !strings.Contains(lean, "keep this custom rule") {
		t.Errorf("an operator-added section must be kept, got:\n%s", lean)
	}
}

// TestTrimProtocolDoesNotBulkInjectSkills locks in that skills stay LAZY: the
// per-pass protocol never inlines a skill file's body (an agent reads the relevant
// skill on demand instead).
func TestTrimProtocolDoesNotBulkInjectSkills(t *testing.T) {
	lean := trimProtocol(prompts.Honeybee, selectt.Work)
	for _, sk := range prompts.Skills() {
		body := strings.TrimSpace(sk.Body)
		if body != "" && strings.Contains(lean, body) {
			t.Fatalf("skill %q body was bulk-injected into the lean protocol", sk.Name)
		}
	}
}

// newWorkRun seeds a minimal Work repo (submodule with a committed source repo and
// a claimable TODO task) and returns the pieces a runner-level test drives.
func newWorkRun(t *testing.T) (g *git.Repo, sm, planPath string, sel *selectt.Selection, rp *repo.Repo) {
	t.Helper()
	root := t.TempDir()
	g = gitInit(t, root)
	repo.Init(root)
	sm = filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath = filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")
	rp, _ = repo.Open(root)
	subs, _ := rp.Submodules()
	sel = &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
	return
}

// TestLeanInjectTrimsSystemAndFiresInlineHint is the end-to-end lean proof: the
// runner injects the trimmed protocol as the system prompt, keeps the completion
// rule OUT of the up-front preamble, and fires it as an at-decision-point hint on
// the "continue" turn where the change doc is still missing.
func TestLeanInjectTrimsSystemAndFiresInlineHint(t *testing.T) {
	g, sm, planPath, sel, rp := newWorkRun(t)

	var gotSystem string
	var sent []string
	// Complete only on turn 2, so turn 1 ends incomplete and the runner must emit
	// the completion hint as the turn-2 prompt.
	cl := &mockClient{gotSystem: &gotSystem, sess: &mockSession{all: &sent, onTurn: func(turn int) {
		if turn == 2 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			commitReviewDoc(g)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= commits=none -->\ngo\n"), 0o644)
		}
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, LeanInject: true}
	res, err := r.Run(context.Background(), sel, prompts.Honeybee, "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}

	// 1) The injected system prompt is the LEAN protocol: smaller, boilerplate and
	// other-kind prose gone, absolute rules intact.
	if len(gotSystem) >= len(prompts.Honeybee) {
		t.Fatalf("lean mode did not shrink the injected system (%d vs full %d)", len(gotSystem), len(prompts.Honeybee))
	}
	if strings.Contains(gotSystem, "## Topology") || strings.Contains(gotSystem, "## Review task") {
		t.Errorf("lean system still carries boilerplate/other-kind prose:\n%s", gotSystem)
	}
	if !strings.Contains(gotSystem, "NEVER edit `ROI.md`") {
		t.Errorf("lean system dropped an absolute rule:\n%s", gotSystem)
	}

	if len(sent) < 2 {
		t.Fatalf("expected at least 2 turns, got %d prompts: %q", len(sent), sent)
	}
	// 2) The completion rule is NOT front-loaded in the up-front brief…
	if strings.Contains(sent[0], "On completion of a Work task:") {
		t.Errorf("lean preamble still front-loads the completion rule:\n%s", sent[0])
	}
	// …though the REQUIRED doc path stays in the up-front brief.
	if !strings.Contains(sent[0], "submodules/sm/docs/bee-T1-T1.md") {
		t.Errorf("lean preamble dropped the required doc path:\n%s", sent[0])
	}
	// 3) It is fired AT THE DECISION POINT: the turn-2 "continue" carries the
	// completion hint because the change doc was still absent after turn 1.
	if !strings.Contains(sent[1], "NEEDS-REVIEW") || !strings.Contains(sent[1], "submodules/sm/docs/bee-T1-T1.md") {
		t.Errorf("at-decision-point completion hint missing from the continue prompt: %q", sent[1])
	}
}

// TestDefaultInjectByteStable proves the default path is unchanged where it must
// be: the injected system is the full protocol verbatim and the completion rule
// stays in the up-front preamble. The follow-up prompt is now the continuation
// status report (nextPrompt), not the bare "continue".
func TestDefaultInjectByteStable(t *testing.T) {
	g, sm, planPath, sel, rp := newWorkRun(t)

	var gotSystem string
	var sent []string
	cl := &mockClient{gotSystem: &gotSystem, sess: &mockSession{all: &sent, onTurn: func(turn int) {
		if turn == 2 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			commitReviewDoc(g)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= commits=none -->\ngo\n"), 0o644)
		}
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour} // LeanInject: false
	res, err := r.Run(context.Background(), sel, prompts.Honeybee, "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	// The injected system is byte-identical to the full protocol.
	if gotSystem != prompts.Honeybee {
		t.Fatalf("default mode altered the injected system prompt (len %d vs %d)", len(gotSystem), len(prompts.Honeybee))
	}
	if len(sent) < 2 {
		t.Fatalf("expected at least 2 turns, got %d prompts: %q", len(sent), sent)
	}
	// The completion rule stays front-loaded in the preamble.
	if !strings.Contains(sent[0], "On completion of a Work task:") {
		t.Errorf("default preamble dropped the completion rule:\n%s", sent[0])
	}
	// The follow-up prompt is the per-kind continuation status report, not the
	// bare "continue": it leads with "continue." and enumerates the work
	// predicates each marked met/unmet.
	if !strings.HasPrefix(sent[1], "continue. Completion status for this work pass") ||
		!strings.Contains(sent[1], "[ ] terminal STATUS set") ||
		!strings.Contains(sent[1], "[ ] change doc present at submodules/sm/docs/bee-T1-T1.md") {
		t.Errorf("default follow-up prompt is not the continuation status report:\n%s", sent[1])
	}
}
