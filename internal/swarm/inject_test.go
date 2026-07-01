package swarm

import (
	"context"
	"fmt"
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

// section returns the text of the "## <name>" section of protocol, from its
// heading through the byte before the next "## " heading (or EOF). Used to assert
// a whole section survives trimming verbatim.
func section(protocol, name string) string {
	head := "## " + name + "\n"
	i := strings.Index(protocol, head)
	if i < 0 {
		return ""
	}
	rest := protocol[i:]
	if j := strings.Index(rest[len(head):], "\n## "); j >= 0 {
		return rest[:len(head)+j+1]
	}
	return rest
}

// TestScopeProtocolWorkTrims proves a Work session's injected protocol drops the
// managed provenance boilerplate and the OTHER kinds' numbered steps by a
// measurable margin, while retaining the Absolute rules, the shared steps, and
// Work's own phase step.
func TestScopeProtocolWorkTrims(t *testing.T) {
	in := prompts.Honeybee
	out := scopeProtocol(in, selectt.Work)

	if len(out) >= len(in) {
		t.Fatalf("trim did not shrink the protocol: in=%d out=%d", len(in), len(out))
	}
	saved := len(in) - len(out)
	if saved < 500 {
		t.Fatalf("trim saved only %d bytes; expected a measurable cut (>=500)", saved)
	}
	t.Logf("Work protocol trim saved %d bytes (%d -> %d)", saved, len(in), len(out))

	// Managed provenance boilerplate is gone (both the marker and a phrase unique
	// to that paragraph).
	for _, gone := range []string{fileMetaMarker, "beehive instruction update"} {
		if contains(out, gone) {
			t.Errorf("trimmed Work protocol still carries boilerplate %q", gone)
		}
	}
	// Other kinds' phase steps are gone.
	for _, gone := range []string{"ROI reconcile", "Arbitration first", "Review next"} {
		if contains(out, gone) {
			t.Errorf("trimmed Work protocol still carries another kind's step %q", gone)
		}
	}
	// Work's own step and every shared step are retained.
	for _, keep := range []string{
		"Main task last",     // step 4: Work's own phase
		"Confirm the claim",  // step 1: shared
		"Human escalation",   // step 5: shared
		"On any -> DONE",     // step 6: shared
		"NEVER touch ROI.md", // step 8: shared (also an absolute rule)
		"## Absolute rules",
	} {
		if !contains(out, keep) {
			t.Errorf("trimmed Work protocol dropped required content %q", keep)
		}
	}
}

// TestScopeProtocolRetainsEveryAbsoluteRule is the safety net for "no protocol
// rule silently lost": the entire Absolute rules section must survive trimming
// verbatim for every kind, since it lives outside the parts the trimmer edits.
func TestScopeProtocolRetainsEveryAbsoluteRule(t *testing.T) {
	rules := section(prompts.Honeybee, "Absolute rules")
	if rules == "" || !strings.Contains(rules, "NEVER edit `ROI.md`") {
		t.Fatalf("test fixture: could not extract Absolute rules section")
	}
	for _, k := range []selectt.Kind{selectt.Work, selectt.Review, selectt.Arbitrate, selectt.Reconcile, selectt.Bootstrap} {
		out := scopeProtocol(prompts.Honeybee, k)
		if !strings.Contains(out, rules) {
			t.Errorf("kind %s: Absolute rules section not retained verbatim", k)
		}
	}
}

// TestScopeProtocolPerKind proves each kind keeps its OWN phase step and drops the
// others', and that Bootstrap (which maps to no phase) keeps every step while
// still shedding the provenance boilerplate.
func TestScopeProtocolPerKind(t *testing.T) {
	cases := []struct {
		kind selectt.Kind
		keep string
		drop []string
	}{
		{selectt.Review, "Review next", []string{"ROI reconcile", "Arbitration first", "Main task last"}},
		{selectt.Arbitrate, "Arbitration first", []string{"ROI reconcile", "Review next", "Main task last"}},
		{selectt.Reconcile, "ROI reconcile", []string{"Arbitration first", "Review next", "Main task last"}},
	}
	for _, c := range cases {
		out := scopeProtocol(prompts.Honeybee, c.kind)
		if !contains(out, c.keep) {
			t.Errorf("kind %s dropped its own step %q", c.kind, c.keep)
		}
		for _, d := range c.drop {
			if contains(out, d) {
				t.Errorf("kind %s kept another kind's step %q", c.kind, d)
			}
		}
		if contains(out, fileMetaMarker) {
			t.Errorf("kind %s kept provenance boilerplate", c.kind)
		}
	}
	// Bootstrap maps to no phase: keep every step, still strip boilerplate.
	boot := scopeProtocol(prompts.Honeybee, selectt.Bootstrap)
	for _, keep := range []string{"ROI reconcile", "Arbitration first", "Review next", "Main task last"} {
		if !contains(boot, keep) {
			t.Errorf("Bootstrap must retain every step, dropped %q", keep)
		}
	}
	if contains(boot, fileMetaMarker) {
		t.Errorf("Bootstrap must still shed provenance boilerplate")
	}
}

// TestScopeProtocolLazySkills guards the "skills stay lazy" invariant: no skill
// file body is ever inlined into the injected protocol.
func TestScopeProtocolLazySkills(t *testing.T) {
	out := scopeProtocol(prompts.Honeybee, selectt.Work)
	for _, sk := range prompts.Skills() {
		if strings.TrimSpace(sk.Body) != "" && contains(out, sk.Body) {
			t.Errorf("skill %s body was inlined into the injected protocol (must stay lazy)", sk.Name)
		}
	}
}

// TestScopeProtocolUnrecognizedUnchanged proves the trimmer is a no-op on input it
// does not recognize (an operator-customized protocol) and that a step whose
// marker keyword was reworded away is KEPT (content guard), so a rule is never
// removed by accident.
func TestScopeProtocolUnrecognizedUnchanged(t *testing.T) {
	plain := "just some notes\nno protocol structure here\n"
	if got := scopeProtocol(plain, selectt.Work); got != plain {
		t.Errorf("unrecognized input must pass through unchanged;\n got: %q", got)
	}

	craft := "# H\n\n## Absolute rules\n- keep me\n\n## Protocol\n" +
		"0. ROI reconcile do the thing\n" +
		"1. Confirm the claim is yours\n" +
		"2. Settle disputes without the canonical phrase\n" +
		"3. Review next branch\n" +
		"4. Main task last, do the work\n" +
		"5. Human escalation path\n" +
		"\n## Tooling\nblah\n"
	out := scopeProtocol(craft, selectt.Work)
	// Reworded step 2 lost its "Arbitration first" marker -> content guard keeps it.
	if !contains(out, "Settle disputes without the canonical phrase") {
		t.Errorf("content guard failed: a reworded step (no marker) must be retained")
	}
	// Markered other-kind steps are still dropped.
	for _, d := range []string{"ROI reconcile do the thing", "Review next branch"} {
		if contains(out, d) {
			t.Errorf("markered other-kind step %q should have been dropped", d)
		}
	}
	// Shared + own steps and the absolute rule survive.
	for _, keep := range []string{"keep me", "Confirm the claim is yours", "Main task last", "Human escalation path", "## Tooling"} {
		if !contains(out, keep) {
			t.Errorf("crafted trim dropped required content %q", keep)
		}
	}
}

// legacyWorkPreamble reproduces the pre-refactor inline Work preamble. It is the
// golden the byte-stable default must still emit.
func legacyWorkPreamble(smName, branch, taskID string) string {
	return fmt.Sprintf(
		"# Context\nYou are working from the beehive repo root (cwd). Submodule: %[1]s.\n"+
			"Coordination files (the beehive layer): submodules/%[1]s/ROI.md (read-only), "+
			"submodules/%[1]s/PLAN.md, submodules/%[1]s/docs/.\n"+
			"Code worktree (already created and checked out for you): submodules/%[1]s/worktrees/%[2]s/ "+
			"on branch %[2]s. Edit the submodule's CODE there; never write submodules/%[1]s/repo (the shared checkout).\n"+
			"On completion of a Work task: PLAN.md -> NEEDS-REVIEW on main; commit the code on branch %[2]s "+
			"with a `Beehive: %[3]s <doc-path>` stamp and ensure that commit is pushed to the submodule's origin; "+
			"bump the submodule pointer.\n"+
			"REQUIRED change doc path: submodules/%[1]s/docs/%[2]s-%[3]s.md (the beehive layer — NOT inside the code "+
			"worktree). The runner's completion check looks for it exactly there; a doc elsewhere reads as 'not done'.\n"+
			"Act autonomously: do not ask for confirmation; make the edits and commits the protocol requires.\n\n",
		smName, branch, taskID)
}

// TestWorkPreambleByteStableDefault proves trim=false reproduces the historical
// preamble byte-for-byte, and trim=true drops exactly the completion mechanics
// while keeping the worktree handoff.
func TestWorkPreambleByteStableDefault(t *testing.T) {
	full := workPreamble("sm", "bee-T1", "T1", false)
	if want := legacyWorkPreamble("sm", "bee-T1", "T1"); full != want {
		t.Fatalf("default preamble drifted from the byte-stable legacy text\n got:\n%q\nwant:\n%q", full, want)
	}
	trimmed := workPreamble("sm", "bee-T1", "T1", true)
	if len(trimmed) >= len(full) {
		t.Fatalf("trimmed preamble not smaller: full=%d trimmed=%d", len(full), len(trimmed))
	}
	// Mechanics dropped from turn 1...
	for _, gone := range []string{"On completion of a Work task", "REQUIRED change doc path"} {
		if contains(trimmed, gone) {
			t.Errorf("trimmed preamble still front-loads %q", gone)
		}
	}
	// ...worktree handoff kept.
	for _, keep := range []string{"submodules/sm/worktrees/bee-T1/", "never write submodules/sm/repo"} {
		if !contains(trimmed, keep) {
			t.Errorf("trimmed preamble dropped essential handoff %q", keep)
		}
	}
}

// TestWorkContinueHintCarriesDecision proves the decision-point prompt carries the
// concrete resolved doc path, the commit stamp, and the NEEDS-REVIEW flip — the
// mechanics moved out of turn 1.
func TestWorkContinueHintCarriesDecision(t *testing.T) {
	h := workContinueHint("sm", "bee-T1", "T1")
	if !strings.HasPrefix(h, "continue") {
		t.Errorf("continue hint must still begin with \"continue\"; got %q", h)
	}
	for _, want := range []string{
		"submodules/sm/docs/bee-T1-T1.md",
		"Beehive: T1 submodules/sm/docs/bee-T1-T1.md",
		"push",
		"NEEDS-REVIEW",
	} {
		if !contains(h, want) {
			t.Errorf("continue hint missing %q; got %q", want, h)
		}
	}
}

// workRunFixture builds a minimal Work selection over a real (local) submodule
// checkout, driving completion on turn 2 so the "continue" prompt is exercised.
func workRunFixture(t *testing.T) (*repo.Repo, *git.Repo, *selectt.Selection, string, string) {
	t.Helper()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")
	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
	return rp, g, sel, sm, planPath
}

// completeOnTurn2 writes the change doc + flips DONE on the second turn.
func completeOnTurn2(sm, planPath string) func(int) {
	return func(turn int) {
		if turn == 2 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		}
	}
}

// TestRunTrimOffByteStable proves the DEFAULT runner injects the protocol and the
// continue prompt byte-for-byte as before (opt-in trimming is truly opt-in).
func TestRunTrimOffByteStable(t *testing.T) {
	rp, g, sel, sm, planPath := workRunFixture(t)
	var sys, first, cont string
	cl := &mockClient{captureSystem: &sys, sess: &mockSession{
		capture: &first, captureCont: &cont, onTurn: completeOnTurn2(sm, planPath),
	}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	if _, err := r.Run(context.Background(), sel, prompts.Honeybee, "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if sys != prompts.Honeybee {
		t.Errorf("default must inject the protocol byte-for-byte (got %d bytes, want %d)", len(sys), len(prompts.Honeybee))
	}
	if cont != "continue" {
		t.Errorf("default continue prompt must be %q, got %q", "continue", cont)
	}
	if !contains(first, "REQUIRED change doc path") {
		t.Errorf("default preamble must keep the completion mechanics")
	}
}

// TestRunTrimOnScopes proves the opt-in runner scopes the injected system prompt
// and swaps the continue prompt for the concrete decision-point hint — end to end
// through Run, not just the pure helpers.
func TestRunTrimOnScopes(t *testing.T) {
	rp, g, sel, sm, planPath := workRunFixture(t)
	var sys, first, cont string
	cl := &mockClient{captureSystem: &sys, sess: &mockSession{
		capture: &first, captureCont: &cont, onTurn: completeOnTurn2(sm, planPath),
	}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, TrimInject: true}
	if _, err := r.Run(context.Background(), sel, prompts.Honeybee, "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if sys == prompts.Honeybee {
		t.Errorf("trim on must scope the system prompt")
	}
	if len(sys) >= len(prompts.Honeybee) {
		t.Errorf("scoped system prompt should be smaller: got %d, full %d", len(sys), len(prompts.Honeybee))
	}
	for _, gone := range []string{"Arbitration first", "Review next"} {
		if contains(sys, gone) {
			t.Errorf("scoped Work system still carries %q", gone)
		}
	}
	if !contains(sys, "Main task last") || !contains(sys, "## Absolute rules") {
		t.Errorf("scoped Work system dropped required content")
	}
	// Turn 1 preamble lost the mechanics; the continue turn carries them concretely.
	if contains(first, "REQUIRED change doc path") {
		t.Errorf("trimmed preamble should not front-load the doc path")
	}
	if !contains(cont, "submodules/sm/docs/bee-T1-T1.md") || !contains(cont, "NEEDS-REVIEW") {
		t.Errorf("continue prompt missing decision-point mechanics; got %q", cont)
	}
}
