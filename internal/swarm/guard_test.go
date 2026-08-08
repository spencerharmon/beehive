package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

const guardTestRegistry = `# Guards — flux
## bluegreen-active-color-frozen
Protects: infrastructure/phantom-library-helm/helmrelease-*.yaml
Command: guards/active-color-frozen.sh
`

// fakeGit dispatches injected git outputs by the joined argv (after the leading
// "git"). Anything unmatched returns an exitErr so a missing stub is visible.
type fakeGit map[string]verifyOutcome

func (f fakeGit) run(_ context.Context, _ string, name string, args ...string) (verifyOutcome, error) {
	if name != "git" {
		return verifyOutcome{}, nil
	}
	key := strings.Join(args, " ")
	if o, ok := f[key]; ok {
		return o, nil
	}
	return verifyOutcome{exitErr: true, out: "unmatched: " + key}, nil
}

func baseGitFixture(changed string) fakeGit {
	return fakeGit{
		"merge-base HEAD main":                    {out: "BASE\n"},
		"show BASE:GUARDS.md":                     {out: guardTestRegistry},
		"diff --name-only BASE HEAD":              {out: changed},
		"diff BASE HEAD":                          {out: "--- patch ---\n"},
		"ls-tree -r BASE -- guards GUARDS.md":     {out: "100755 blob abc\tguards/active-color-frozen.sh\n100644 blob def\tGUARDS.md\n"},
		"show BASE:guards/active-color-frozen.sh": {out: "#!/bin/sh\nexit 0\n"},
		"show BASE:GUARDS.md ":                    {out: guardTestRegistry},
	}
}

func newGuardRunner(git fakeGit, guardExit bool, capture *[]string) *Runner {
	return &Runner{
		RunVerify: git.run,
		RunGuard: func(_ context.Context, _ string, env []string, _ string, _ ...string) (verifyOutcome, error) {
			if capture != nil {
				*capture = env
			}
			return verifyOutcome{exitErr: guardExit, out: "guard says no"}, nil
		},
	}
}

func guardSel(kind selectt.Kind) *selectt.Selection {
	return &selectt.Selection{Kind: kind, Submodule: repo.Submodule{Name: "flux", Path: "submodules/flux"}}
}

func TestGuardGate_RefusesProtectedActiveEdit(t *testing.T) {
	git := baseGitFixture("infrastructure/phantom-library-helm/helmrelease-green.yaml\n")
	r := newGuardRunner(git, true, nil) // guard exits non-zero => refuse
	hint, err := r.guardGate(context.Background(), guardSel(selectt.Work), t.TempDir(), t.TempDir(), "main")
	if err != nil {
		t.Fatalf("infra err: %v", err)
	}
	if hint == "" {
		t.Fatal("expected a refusal prompt when the guard exits non-zero")
	}
	if !strings.Contains(hint, "bluegreen-active-color-frozen") || !strings.Contains(hint, "helmrelease-green.yaml") {
		t.Fatalf("prompt should name the guard and the offending file, got: %s", hint)
	}
}

func TestGuardGate_AllowsWhenGuardPasses(t *testing.T) {
	git := baseGitFixture("infrastructure/phantom-library-helm/helmrelease-blue.yaml\n")
	r := newGuardRunner(git, false, nil) // guard exits 0 => allow
	hint, err := r.guardGate(context.Background(), guardSel(selectt.Work), t.TempDir(), t.TempDir(), "main")
	if err != nil || hint != "" {
		t.Fatalf("expected allow, got hint=%q err=%v", hint, err)
	}
}

func TestGuardGate_SkipsWhenNoProtectedPathTouched(t *testing.T) {
	git := baseGitFixture("README.md\n")
	captured := []string{}
	r := newGuardRunner(git, true, &captured) // guard WOULD refuse, but must never run
	hint, err := r.guardGate(context.Background(), guardSel(selectt.Work), t.TempDir(), t.TempDir(), "main")
	if err != nil || hint != "" {
		t.Fatalf("untriggered guard must allow, got hint=%q err=%v", hint, err)
	}
	if len(captured) != 0 {
		t.Fatal("guard command must not execute when no protected path is in the diff")
	}
}

func TestGuardGate_NoRegistryAtBaselineIsZeroOverhead(t *testing.T) {
	git := baseGitFixture("infrastructure/phantom-library-helm/helmrelease-green.yaml\n")
	git["show BASE:GUARDS.md"] = verifyOutcome{exitErr: true} // absent at baseline
	r := newGuardRunner(git, true, nil)
	hint, err := r.guardGate(context.Background(), guardSel(selectt.Work), t.TempDir(), t.TempDir(), "main")
	if err != nil || hint != "" {
		t.Fatalf("absent baseline GUARDS.md must allow, got hint=%q err=%v", hint, err)
	}
}

func TestGuardGate_MalformedBaselineRegistryFailsForward(t *testing.T) {
	git := baseGitFixture("infrastructure/phantom-library-helm/helmrelease-green.yaml\n")
	git["show BASE:GUARDS.md"] = verifyOutcome{out: "## broken\nCommand: true\n"} // no Protects, trivial cmd
	r := newGuardRunner(git, false, nil)
	hint, err := r.guardGate(context.Background(), guardSel(selectt.Work), t.TempDir(), t.TempDir(), "main")
	if err != nil {
		t.Fatalf("a malformed registry is an author defect, not infra: %v", err)
	}
	if !strings.Contains(hint, "does not parse") {
		t.Fatalf("expected a parse fix-forward prompt, got: %s", hint)
	}
}

func TestGuardGate_PassesABIEnv(t *testing.T) {
	git := baseGitFixture("infrastructure/phantom-library-helm/helmrelease-green.yaml\n")
	captured := []string{}
	r := newGuardRunner(git, false, &captured)
	if _, err := r.guardGate(context.Background(), guardSel(selectt.Work), t.TempDir(), t.TempDir(), "main"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(captured, "\n")
	for _, want := range []string{
		"BEEHIVE_HONEYBEE=1",
		"BEEHIVE_GUARD_SUBMODULE=flux",
		"BEEHIVE_GUARD_ID=bluegreen-active-color-frozen",
		"BEEHIVE_GUARD_MATCHED_FILES=infrastructure/phantom-library-helm/helmrelease-green.yaml",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("guard ABI missing %q; env=\n%s", want, joined)
		}
	}
}
