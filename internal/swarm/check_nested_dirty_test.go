package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
)

func TestCheckRejectsDirtyNestedTrackedContent(t *testing.T) {
	for _, level := range []string{"nested", "nested/child"} {
		for _, edit := range []string{"unstaged", "staged", "staged-pin"} {
			t.Run(level+"/"+edit, func(t *testing.T) {
				f := newNestedCheckFixture(t, false, false)
				ctx := context.Background()
				nestedCheckGit(t, f.wt, "fetch", "origin", "bee-R1")
				nestedCheckGit(t, f.wt, "merge", "--no-edit", "origin/bee-R1")
				if err := git.New(f.wt).PrepareNestedSubmodules(ctx); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(f.wt, level)
				if edit == "staged-pin" {
					parent := filepath.Dir(dir)
					old := nestedCheckGit(t, dir, "rev-parse", "HEAD^")
					nestedCheckGit(t, parent, "update-index", "--cacheinfo", "160000,"+old+","+filepath.Base(dir))
				} else {
					nestedCheckWrite(t, filepath.Join(dir, "value.txt"), "agent edit\n")
					if edit == "staged" {
						nestedCheckGit(t, dir, "add", "value.txt")
					}
				}
				// Ambient ignore settings must never conceal tracked dependency edits.
				nestedCheckGit(t, f.wt, "config", "submodule.nested.ignore", "all")
				nestedCheckGit(t, filepath.Join(f.wt, "nested"), "config", "submodule.child.ignore", "all")
				before := nestedCheckGit(t, dir, "status", "--porcelain", "--ignore-submodules=none")
				pins := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive")
				_, err := f.r.runCheckIn(ctx, f.sel, f.task(t).Check(), f.wt)
				if err == nil {
					t.Error("dirty tracked dependency must fail preparation")
				}
				if _, err := os.Stat(f.marker); !os.IsNotExist(err) {
					t.Error("check launched with modified nested source")
				}
				if got := nestedCheckGit(t, dir, "status", "--porcelain", "--ignore-submodules=none"); got != before {
					t.Errorf("preparation changed edits: %q -> %q", before, got)
				}
				if got := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive"); got != pins {
					t.Errorf("rejected preparation moved pins: %s -> %s", pins, got)
				}
			})
		}
	}
}

func TestReviewMergeAcceptsCommittedNestedPinDrift(t *testing.T) {
	for _, gate := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "handoff"}[gate], func(t *testing.T) {
			f := newNestedCheckFixture(t, false, false)
			nestedCheckGit(t, f.wt, "fetch", "origin", "bee-R1")
			nestedCheckGit(t, f.wt, "merge", "--no-edit", "origin/bee-R1")
			if gate {
				f.registerChecks(t)
				if hint, err := f.r.verifyGate(context.Background(), f.sel, f.wt, f.root, "bee-R1"); err != nil || hint != "" {
					t.Fatalf("committed pin drift is not authored dirt: hint=%q err=%v", hint, err)
				}
			}
			hint, _, err := f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root)
			if err != nil || hint != "" {
				t.Fatalf("committed merge must prepare: hint=%q err=%v", hint, err)
			}
			f.assertPins(t, f.wt)
		})
	}
}

func TestNestedHandoffSharedFallbackIsReadOnly(t *testing.T) {
	f := newNestedCheckFixture(t, false, false)
	code := f.sel.Submodule.RepoDir()
	nestedCheckGit(t, code, "submodule", "update", "--init", "--recursive")
	nestedCheckGit(t, code, "fetch", "origin", "bee-R1")
	nestedCheckGit(t, code, "merge", "--ff-only", "origin/bee-R1")
	before := nestedCheckGit(t, code, "submodule", "status", "--recursive")
	if hint, err := f.r.verifyGate(context.Background(), f.sel, "", f.root, "bee-R1"); err != nil || !strings.Contains(hint, "UNCOMMITTED") {
		t.Fatalf("shared fallback must only report drift: hint=%q err=%v", hint, err)
	}
	if after := nestedCheckGit(t, code, "submodule", "status", "--recursive"); after != before {
		t.Fatalf("shared fallback mutated nested pins: %s -> %s", before, after)
	}
}

func TestNestedHandoffPreservesAuthoredChanges(t *testing.T) {
	for _, edit := range []string{"parent", "staged-pin", "nested"} {
		t.Run(edit, func(t *testing.T) {
			f := newNestedCheckFixture(t, false, false)
			switch edit {
			case "parent":
				nestedCheckWrite(t, filepath.Join(f.wt, "expected.txt"), "authored\n")
			case "staged-pin":
				nestedCheckGit(t, f.wt, "update-index", "--cacheinfo", "160000,"+f.pin+",nested")
			case "nested":
				nestedCheckWrite(t, filepath.Join(f.wt, "nested", "child", "value.txt"), "authored\n")
			}
			before := nestedCheckGit(t, f.wt, "status", "--porcelain", "--ignore-submodules=none")
			prepErr := git.New(f.wt).PrepareCleanNestedCheckout(context.Background())
			if hint, err := f.r.gateCleanCheckout(context.Background(), f.wt); prepErr == nil && err == nil && !strings.Contains(hint, "UNCOMMITTED") {
				t.Fatalf("authored changes must be refused: hint=%q err=%v", hint, err)
			}
			if after := nestedCheckGit(t, f.wt, "status", "--porcelain", "--ignore-submodules=none"); after != before {
				t.Fatalf("handoff changed authored state: %q -> %q", before, after)
			}
		})
	}
}
