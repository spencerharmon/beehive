package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// mainTask reads R1 from the PUBLISHED hive main, not the pass's working copy.
func (f *nestedCheckFixture) mainTask(t *testing.T) *plan.Task {
	t.Helper()
	p, err := plan.Parse(nestedCheckGit(t, f.root, "show", "main:submodules/sm/PLAN.md") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	task := p.Find("R1")
	if task == nil {
		t.Fatal("R1 disappeared from published PLAN.md")
	}
	return task
}

// registerChecks approves the fixture's real nested-source comparison check.
func (f *nestedCheckFixture) registerChecks(t *testing.T) {
	t.Helper()
	nestedCheckWrite(t, filepath.Join(f.root, "submodules", "sm", "CHECKS.md"),
		"# Checks\n\n## nested-compare <!-- category=integration -->\nCompares prepared nested source.\nMatch: cmp expected\\.txt nested/\n")
	nestedCheckGit(t, f.root, "add", "submodules/sm/CHECKS.md")
	nestedCheckGit(t, f.root, "commit", "-qm", "register nested check")
}

func (f *nestedCheckFixture) seedStatus(t *testing.T, status plan.Status) {
	t.Helper()
	planPath := f.sel.Submodule.PlanPath()
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "[DONE]", "["+string(status)+"]", 1)
	nestedCheckWrite(t, planPath, text)
	nestedCheckGit(t, f.root, "add", "submodules/sm/PLAN.md")
	nestedCheckGit(t, f.root, "commit", "-qm", "seed "+string(status))
}

// approveOnTurn is a reviewer/arbiter agent that commits its DONE approval.
func (f *nestedCheckFixture) approveOnTurn(t *testing.T, working plan.Status) *mockClient {
	planPath := f.sel.Submodule.PlanPath()
	all := &[]string{}
	t.Cleanup(func() {
		if t.Failed() && len(*all) > 1 {
			t.Logf("last prompt:\n%s", (*all)[len(*all)-1])
		}
	})
	return &mockClient{sess: &mockSession{all: all, onTurn: func(int) {
		b, err := os.ReadFile(planPath)
		if err != nil {
			t.Error(err)
			return
		}
		os.WriteFile(planPath, []byte(strings.Replace(string(b), "["+string(working)+"]", "[DONE]", 1)), 0o644)
		_ = f.r.Git.CommitPaths(context.Background(), "approve R1", "submodules/sm/PLAN.md")
	}}}
}

// TestRunnerUnavailableNestedPinFailsClosedAtPublication drives the WHOLE runner
// (post-turn and top-of-turn heartbeat completion) with an approval whose merged
// tree pins a nested gitlink absent from its remote. The hive branch publishes
// successfully, but must carry the reverted working status: no check launch, no
// source push, no durable DONE, attempts untouched. Restoring the dependency
// lets the very next pass approve and merge.
func TestRunnerUnavailableNestedPinFailsClosedAtPublication(t *testing.T) {
	for _, kind := range []selectt.Kind{selectt.Review, selectt.Arbitrate} {
		for _, path := range []string{"post-turn", "heartbeat"} {
			t.Run(fmt.Sprintf("%s/%s", kind, path), func(t *testing.T) {
				ctx := context.Background()
				f := newNestedCheckFixture(t, false, true)
				working := plan.NeedsReview
				if kind == selectt.Arbitrate {
					working = plan.StatusArb
				}
				f.sel.Kind = kind
				f.registerChecks(t)
				f.sel.Task.Status = working
				if path == "post-turn" {
					f.seedStatus(t, working)
					f.r.Client = f.approveOnTurn(t, working)
				} else {
					// DONE already committed: the first heartbeat resolves it.
					f.r.Client = &mockClient{sess: &mockSession{}}
				}

				res, err := f.r.Run(ctx, f.sel, "sys", "first")
				if err == nil {
					t.Fatalf("unavailable nested pin must surface an infrastructure error, got %+v", res)
				}
				if res.Completed {
					t.Fatalf("pass must not complete: %+v", res)
				}
				if _, statErr := os.Stat(f.marker); !os.IsNotExist(statErr) {
					t.Errorf("check launched before nested preparation succeeded: %v", statErr)
				}
				if originMainContains(t, f.origin, f.root, "feature.txt") {
					t.Error("unverifiable merge was pushed to the tracked branch")
				}
				if task := f.mainTask(t); task.Status != working || task.Attempts != 0 {
					t.Fatalf("published hive main must keep %s with attempts=0, got status=%s attempts=%d", working, task.Status, task.Attempts)
				}
				if log := nestedCheckGit(t, f.root, "log", "--format=%s", "main"); !strings.Contains(log, "plan: revert R1 DONE -> "+string(working)) {
					t.Errorf("hive publication did not carry the durable revert:\n%s", log)
				}

				// The dependency returns: publish the pinned nested commit.
				nestedCheckGit(t, f.missing, "push", "-q", f.nested, "HEAD:refs/heads/restored")
				f.sel.Task.Status = working
				f.sel.Task.Session, f.sel.Task.Heartbeat = "", f.sel.Task.Heartbeat.AddDate(-1, 0, 0)
				f.r.Client = f.approveOnTurn(t, working)
				res, err = f.r.Run(ctx, f.sel, "sys", "first")
				if err != nil || !res.Completed {
					t.Fatalf("retry after dependency returned must complete: res=%+v err=%v", res, err)
				}
				if !originMainContains(t, f.origin, f.root, "feature.txt") {
					t.Error("retry did not push the verified merge")
				}
				if task := f.mainTask(t); task.Status != plan.Done || task.Attempts != 0 {
					t.Errorf("retry must publish DONE without attempts, got status=%s attempts=%d", task.Status, task.Attempts)
				}
				if _, statErr := os.Stat(f.marker); statErr != nil {
					t.Errorf("retry must run the real check: %v", statErr)
				}
			})
		}
	}
}
