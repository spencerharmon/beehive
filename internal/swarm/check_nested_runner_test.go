package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// publishedPlan reads PLAN.md from the PUBLISHED hive main (a bare primary the
// runner's real publisher pushes to), never the pass's private task branch.
func (f *nestedCheckFixture) publishedPlan(t *testing.T, hiveOrigin string) *plan.Plan {
	t.Helper()
	p, err := plan.Parse(nestedCheckGit(t, hiveOrigin, "show", "main:submodules/sm/PLAN.md") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Find("R1") == nil {
		t.Fatal("R1 disappeared from published PLAN.md")
	}
	return p
}

// dependentReady reports whether the published plan offers the dependent R2
// (deps=R1) as a selectable candidate.
func dependentReady(p *plan.Plan) bool {
	for _, c := range p.Candidates(time.Now(), time.Hour) {
		if c.ID == "R2" {
			return true
		}
	}
	return false
}

// publishViaBareHive adds a dependent R2 (deps=R1), moves the pass onto a
// private hive task branch, and wires a REAL publisher that pushes that
// branch's HEAD to a temporary bare primary hive main. Returns the bare path.
func (f *nestedCheckFixture) publishViaBareHive(t *testing.T) string {
	t.Helper()
	planPath := f.sel.Submodule.PlanPath()
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	nestedCheckWrite(t, planPath, string(b)+"\n## R2 [TODO] <!-- attempts=0 deps=R1 commits=none -->\ndependent\ncheck=none\n")
	nestedCheckGit(t, f.root, "add", "submodules/sm/PLAN.md")
	nestedCheckGit(t, f.root, "commit", "-qm", "add dependent R2")
	bare := filepath.Join(t.TempDir(), "hive.git")
	nestedCheckGit(t, f.root, "init", "-q", "--bare", "-b", "main", bare)
	nestedCheckGit(t, f.root, "push", "-q", bare, "HEAD:refs/heads/main")
	nestedCheckGit(t, f.root, "checkout", "-q", "-b", "bee-hive-task")
	f.r.Publish = func(ctx context.Context) error {
		_, err := f.r.Git.Run(ctx, "push", "-q", bare, "HEAD:refs/heads/main")
		return err
	}
	return bare
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
				hive := f.publishViaBareHive(t)
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
				pub := f.publishedPlan(t, hive)
				if task := pub.Find("R1"); task.Status != working || task.Attempts != 0 {
					t.Fatalf("published hive main must keep %s with attempts=0, got status=%s attempts=%d", working, task.Status, task.Attempts)
				}
				if dependentReady(pub) {
					t.Error("dependent R2 must stay blocked while R1's nested pin is unavailable")
				}
				if log := nestedCheckGit(t, hive, "log", "--format=%s", "main"); !strings.Contains(log, "plan: revert R1 DONE -> "+string(working)) {
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
				pub = f.publishedPlan(t, hive)
				if task := pub.Find("R1"); task.Status != plan.Done || task.Attempts != 0 {
					t.Errorf("retry must publish DONE without attempts, got status=%s attempts=%d", task.Status, task.Attempts)
				}
				if !dependentReady(pub) {
					t.Error("dependent R2 must become ready once R1 is verified DONE")
				}
				if _, statErr := os.Stat(f.marker); statErr != nil {
					t.Errorf("retry must run the real check: %v", statErr)
				}
			})
		}
	}
}

// TestRunnerNestedPinReversionFailureSuppressesPublication: when the
// fail-closed reversion itself cannot be written or committed, the committed
// HEAD may still carry the unverified DONE, so the runner must NOT publish the
// task branch at all — published main keeps the pre-pass working status and the
// dependent stays blocked; no check, no source push, attempts untouched.
func TestRunnerNestedPinReversionFailureSuppressesPublication(t *testing.T) {
	for _, kind := range []selectt.Kind{selectt.Review, selectt.Arbitrate} {
		for _, path := range []string{"post-turn", "heartbeat"} {
			for _, mode := range []string{"write", "commit"} {
				t.Run(fmt.Sprintf("%s/%s/%s", kind, path, mode), func(t *testing.T) {
					ctx := context.Background()
					f := newNestedCheckFixture(t, false, true)
					working := plan.NeedsReview
					if kind == selectt.Arbitrate {
						working = plan.StatusArb
					}
					f.sel.Kind = kind
					f.registerChecks(t)
					f.seedStatus(t, working)
					hive := f.publishViaBareHive(t)
					f.sel.Task.Status = working
					if path == "post-turn" {
						f.r.Client = f.approveOnTurn(t, working)
					} else {
						// DONE committed on the private task branch before the pass.
						planPath := f.sel.Submodule.PlanPath()
						b, _ := os.ReadFile(planPath)
						nestedCheckWrite(t, planPath, strings.Replace(string(b), "["+string(working)+"]", "[DONE]", 1))
						nestedCheckGit(t, f.root, "commit", "-qam", "approve R1")
						f.r.Client = &mockClient{sess: &mockSession{}}
					}
					switch mode {
					case "write":
						orig := writePlanFile
						writePlanFile = func(string, *plan.Plan) error { return fmt.Errorf("injected write failure") }
						t.Cleanup(func() { writePlanFile = orig })
					case "commit":
						hooks := t.TempDir()
						nestedCheckWrite(t, filepath.Join(hooks, "commit-msg"),
							"#!/bin/sh\nif grep -q '^plan: revert' \"$1\"; then echo rejected >&2; exit 1; fi\n")
						if err := os.Chmod(filepath.Join(hooks, "commit-msg"), 0o755); err != nil {
							t.Fatal(err)
						}
						nestedCheckGit(t, f.root, "config", "core.hooksPath", hooks)
					}
					publishedBefore := nestedCheckGit(t, hive, "rev-parse", "main")

					res, err := f.r.Run(ctx, f.sel, "sys", "first")
					if err == nil || res.Completed {
						t.Fatalf("reversion failure must surface an error and not complete: res=%+v err=%v", res, err)
					}
					if !strings.Contains(err.Error(), "publication suppressed") {
						t.Errorf("error must report suppressed publication: %v", err)
					}
					if _, statErr := os.Stat(f.marker); !os.IsNotExist(statErr) {
						t.Errorf("check launched: %v", statErr)
					}
					if originMainContains(t, f.origin, f.root, "feature.txt") {
						t.Error("unverifiable merge pushed")
					}
					pub := f.publishedPlan(t, hive)
					if task := pub.Find("R1"); task.Status == plan.Done || task.Attempts != 0 {
						t.Fatalf("published R1 must stay nonterminal with attempts=0, got status=%s attempts=%d", task.Status, task.Attempts)
					}
					if dependentReady(pub) {
						t.Error("dependent R2 unblocked by an unverified DONE")
					}
					if after := nestedCheckGit(t, hive, "rev-parse", "main"); after != publishedBefore {
						if strings.Contains(nestedCheckGit(t, hive, "log", "--format=%s", publishedBefore+".."+after), "approve R1") {
							t.Error("the committed DONE approval was published")
						}
					}
				})
			}
		}
	}
}
