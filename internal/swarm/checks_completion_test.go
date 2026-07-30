package swarm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// TestChecksApprovedItem: the completion predicate a bootstrap/reconcile pass must
// meet — every open task's declared check matches an approved framework in
// CHECKS.md — is met for an approved plan, unmet (with the offending task named)
// for a grep check, and unmet when CHECKS.md is missing while a check is declared.
func TestChecksApprovedItem(t *testing.T) {
	newSel := func(t *testing.T, planBody, checksBody string) (*Runner, *selectt.Selection) {
		root := t.TempDir()
		sm := filepath.Join(root, "submodules", "sm")
		if err := os.MkdirAll(sm, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sm, repo.PlanFile), []byte(planBody), 0o644); err != nil {
			t.Fatal(err)
		}
		if checksBody != "" {
			if err := os.WriteFile(filepath.Join(sm, repo.ChecksFile), []byte(checksBody), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		sub := repo.Submodule{Name: "sm", Path: sm}
		return &Runner{}, &selectt.Selection{Kind: selectt.Reconcile, Submodule: sub}
	}
	goStub := "# Checks\n\n## go-test <!-- category=unit -->\nMatch: (^|&&|;|\\s)go\\s+test\\b\n"

	// Approved check -> met.
	r, sel := newSel(t, "# Plan\n\n## a [TODO] <!-- deps= -->\nx\nCheck: go test ./...\n", goStub)
	if it := r.checksApprovedItem(sel); !it.met {
		t.Fatalf("approved check must be met; got %q", it.label)
	}

	// Grep check -> unmet, naming the task.
	r, sel = newSel(t, "# Plan\n\n## a [TODO] <!-- deps= -->\nx\nCheck: grep -q Z repo/x\n", goStub)
	if it := r.checksApprovedItem(sel); it.met {
		t.Fatal("a grep check must be UNMET")
	} else if want := "a"; !contains(it.label, want) {
		t.Fatalf("label must name offending task %q; got %q", want, it.label)
	}

	// Declared check, no CHECKS.md -> unmet.
	r, sel = newSel(t, "# Plan\n\n## a [TODO] <!-- deps= -->\nx\nCheck: go test ./...\n", "")
	if it := r.checksApprovedItem(sel); it.met {
		t.Fatal("a declared check with no CHECKS.md must be UNMET")
	}

	// DONE task with a grep check is grandfathered (not open) -> met.
	r, sel = newSel(t, "# Plan\n\n## a [DONE] <!-- deps= -->\nx\nCheck: grep -q Z repo/x\n", goStub)
	if it := r.checksApprovedItem(sel); !it.met {
		t.Fatalf("a DONE task's check must be grandfathered; got %q", it.label)
	}

	// No checks declared at all -> met (nothing to constrain).
	r, sel = newSel(t, "# Plan\n\n## a [TODO] <!-- deps= -->\nx\n", "")
	if it := r.checksApprovedItem(sel); !it.met {
		t.Fatalf("no declared checks must be met; got %q", it.label)
	}
}
