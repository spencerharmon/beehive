package web

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
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

// TestRulesFileContextKeysOffConstant locks the submodule-rules-md wiring of the
// chat-diff editor (agent/edit) context: the resolver keys the RULES.md rule off
// the shared repo.RulesFile constant (not a stray literal), a submodule-qualified
// RULES.md resolves the same as the bare basename, and the preamble states the
// additive AGENTS-then-RULES order.
func TestRulesFileContextKeysOffConstant(t *testing.T) {
	smPath := filepath.ToSlash(filepath.Join("submodules", "alpha", repo.RulesFile))
	got := resolveFileContext(smPath)
	for _, sub := range []string{repo.RulesFile, "ADDITIVE", repo.AgentsFile} {
		if !strings.Contains(got, sub) {
			t.Errorf("RULES.md context missing %q:\n%s", sub, got)
		}
	}
	// AGENTS.md applied first, then the additive RULES.md — the ordering the
	// explorer and honeybee overlay also honor.
	if !strings.Contains(got, "applied first") || !strings.Contains(got, "then RULES.md") {
		t.Errorf("RULES.md context does not state AGENTS-then-RULES order:\n%s", got)
	}
	// Basename match: submodule-qualified resolves identically to the bare name,
	// and both differ from the AGENTS.md overlay rule (RULES is additive, not a
	// rename of AGENTS).
	if resolveFileContext(smPath) != resolveFileContext(repo.RulesFile) {
		t.Error("RULES.md rule should match regardless of directory")
	}
	if resolveFileContext(repo.RulesFile) == resolveFileContext(repo.AgentsFile) {
		t.Error("RULES.md and AGENTS.md must resolve to distinct preambles")
	}
}

// TestFileContextTokensAndBootstrapSeed: resolveFileContext resolves a path to
// that file's distinct rules, and the bootstrap setup agent's system prompt
// embeds LOCALS.md's rules (the surviving per-file-context injection path after
// edit-session-consolidation — the coordination editor itself uses the generic
// FilePrompt, as it did before this change).
func TestFileContextTokensAndBootstrapSeed(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"submodules/alpha/ROI.md", "FORBIDDEN"},
		{"submodules/alpha/PLAN.md", "NEEDS-REVIEW"},
		{"submodules/alpha/notes.md", "ordinary file"},
	} {
		sp := resolveFileContext(tc.path)
		if !strings.Contains(sp, tc.want) {
			t.Errorf("file context for %q missing rules token %q:\n%s", tc.path, tc.want, sp)
		}
	}
	// The bootstrap agent's system prompt embeds LOCALS.md's editing rules on the
	// coordination editor's direct-edit contract.
	sys := bootstrapSystemPrompt("guide", bootstrapState{})
	if !strings.Contains(sys, "SITE-SPECIFIC") {
		t.Fatalf("bootstrap system prompt missing LOCALS.md rules:\n%s", sys)
	}
	if !strings.Contains(sys, repo.LocalsFile) {
		t.Fatalf("bootstrap system prompt missing the LOCALS.md target")
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

// TestRootInstructionFileContextNotConflated locks root-instruction-file-links'
// chat-diff-file-context seeding: each repo-ROOT instruction file resolves to its
// OWN distinct purpose/ownership preamble, the three managed files (AGENTS/
// HONEYBEE/BOOTSTRAP) note they are beehive-managed while the site-authored
// LOCALS.md does not, and — critically — the generic root AGENTS.md is NOT
// conflated with a per-submodule submodules/<sm>/AGENTS.md overlay.
func TestRootInstructionFileContextNotConflated(t *testing.T) {
	// The generic root AGENTS.md must resolve to a DIFFERENT context than a
	// per-submodule AGENTS.md overlay — the exact conflation the task forbids.
	rootAgents := resolveFileContext(repo.AgentsFile)
	subAgents := resolveFileContext(filepath.ToSlash(filepath.Join("submodules", "alpha", repo.AgentsFile)))
	if rootAgents == subAgents {
		t.Fatal("root AGENTS.md must resolve to a DIFFERENT context than a per-submodule AGENTS.md overlay")
	}
	if !strings.Contains(rootAgents, "GENERIC") {
		t.Errorf("root AGENTS.md context should mark it the generic guide:\n%s", rootAgents)
	}

	// Each managed root file carries its own distinct preamble stating its purpose
	// and that it is beehive-managed (the flag instruction-update-drift honors).
	managed := []struct {
		path string
		subs []string
	}{
		{repo.AgentsFile, []string{"AGENTS.md", "operating guide"}},
		{repo.HoneybeeFile, []string{"HONEYBEE.md", "runtime protocol"}},
		{repo.BootstrapFile, []string{"BOOTSTRAP.md", "install"}},
	}
	seen := map[string]bool{}
	for _, c := range managed {
		got := resolveFileContext(c.path)
		for _, sub := range append(c.subs, "beehive-MANAGED") {
			if !strings.Contains(got, sub) {
				t.Errorf("%s context missing %q:\n%s", c.path, sub, got)
			}
		}
		if seen[got] {
			t.Errorf("%s context is not distinct from another root file", c.path)
		}
		seen[got] = true
	}

	// LOCALS.md is site-authored: its context states site-specific / never
	// auto-generated, is distinct from the managed files, and must NOT claim to be
	// beehive-managed.
	locals := resolveFileContext(repo.LocalsFile)
	if seen[locals] {
		t.Error("LOCALS.md context is not distinct from a managed root file")
	}
	for _, sub := range []string{"LOCALS.md", "SITE-SPECIFIC", "auto-generated"} {
		if !strings.Contains(locals, sub) {
			t.Errorf("LOCALS.md context missing %q:\n%s", sub, locals)
		}
	}
	if strings.Contains(locals, "beehive-MANAGED") {
		t.Errorf("LOCALS.md is site-authored; its context must not mark it beehive-MANAGED:\n%s", locals)
	}
}
