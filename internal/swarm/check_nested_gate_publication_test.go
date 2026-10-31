package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// TestRunnerHandoffPreparationFailsClosedAtPublication drives the WHOLE runner
// through review/arbitration x post-turn/heartbeat completion where the agent
// already committed a source pre-merge whose handed checkout cannot be prepared
// (unavailable nested pin, ordinary or hidden-index nested tracked edits). The
// verify gate's preparation failure must durably revert the committed DONE
// before publication (or suppress publication when the reversion cannot be
// written/committed): published R1 stays working with attempts untouched, R2
// stays blocked, no check launches, no source push, authored bytes and index
// are preserved, and the original preparation diagnostic is reported.
func TestRunnerHandoffPreparationFailsClosedAtPublication(t *testing.T) {
	type tc struct{ scenario, failure string }
	var cases []tc
	for _, s := range []string{"unavailable", "dirty", "assume-unchanged", "skip-worktree"} {
		cases = append(cases, tc{s, "none"})
	}
	cases = append(cases, tc{"unavailable", "write"}, tc{"unavailable", "commit"}, tc{"skip-worktree", "commit"})
	for _, kind := range []selectt.Kind{selectt.Review, selectt.Arbitrate} {
		for _, path := range []string{"post-turn", "heartbeat"} {
			for _, c := range cases {
				t.Run(fmt.Sprintf("%s/%s/%s/%s", kind, path, c.scenario, c.failure), func(t *testing.T) {
					ctx := context.Background()
					f := newNestedCheckFixture(t, false, c.scenario == "unavailable")
					working := plan.NeedsReview
					if kind == selectt.Arbitrate {
						working = plan.StatusArb
					}
					f.sel.Kind = kind
					f.registerChecks(t)
					f.seedStatus(t, working)
					hive := f.publishViaBareHive(t)
					f.sel.Task.Status = working
					edited := filepath.Join(f.wt, "nested", "child", "value.txt")
					const authored = "authored nested edit\n"
					var once sync.Once
					premerge := func() {
						once.Do(func() {
							nestedCheckGit(t, f.wt, "fetch", "-q", "origin", "bee-R1")
							nestedCheckGit(t, f.wt, "merge", "-q", "--no-edit", "origin/bee-R1")
							if c.scenario == "unavailable" {
								return
							}
							if err := git.New(f.wt).PrepareNestedSubmodules(ctx); err != nil {
								t.Errorf("prepare: %v", err)
								return
							}
							if c.scenario != "dirty" {
								nestedCheckGit(t, filepath.Dir(edited), "update-index", "--"+c.scenario, "value.txt")
							}
							nestedCheckWrite(t, edited, authored)
						})
					}
					if path == "post-turn" {
						client := f.approveOnTurn(t, working)
						approve := client.sess.onTurn
						client.sess.onTurn = func(turn int) { premerge(); approve(turn) }
						f.r.Client = client
					} else {
						p := f.sel.Submodule.PlanPath()
						b, err := os.ReadFile(p)
						if err != nil {
							t.Fatal(err)
						}
						nestedCheckWrite(t, p, strings.Replace(string(b), "["+string(working)+"]", "[DONE]", 1))
						nestedCheckGit(t, f.root, "commit", "-qam", "approve R1")
						f.r.Client = &mockClient{sess: &mockSession{}}
						f.r.RestoreConfig = func(context.Context) { premerge() }
					}
					switch c.failure {
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
						t.Fatalf("handoff preparation failure must surface an error and not complete: res=%+v err=%v", res, err)
					}
					if !strings.Contains(err.Error(), "preparing reused nested checkout") {
						t.Errorf("original preparation diagnostic lost: %v", err)
					}
					if c.failure != "none" && !strings.Contains(err.Error(), "publication suppressed") {
						t.Errorf("error must report suppressed publication: %v", err)
					}
					if _, statErr := os.Stat(f.marker); !os.IsNotExist(statErr) {
						t.Errorf("check launched: %v", statErr)
					}
					if originMainContains(t, f.origin, f.root, "feature.txt") {
						t.Error("unverified merge pushed to the tracked branch")
					}
					pub := f.publishedPlan(t, hive)
					if task := pub.Find("R1"); task.Status != working || task.Attempts != 0 {
						t.Errorf("published R1 must stay %s attempts=0, got %s attempts=%d", working, task.Status, task.Attempts)
					}
					if dependentReady(pub) {
						t.Error("dependent R2 unblocked by unverified DONE")
					}
					after := nestedCheckGit(t, hive, "rev-parse", "main")
					if c.failure == "none" {
						if log := nestedCheckGit(t, hive, "log", "--format=%s", "main"); !strings.Contains(log, "plan: revert R1 DONE -> "+string(working)) {
							t.Errorf("publication did not carry the durable revert:\n%s", log)
						}
					} else if after != publishedBefore {
						if strings.Contains(nestedCheckGit(t, hive, "log", "--format=%s", publishedBefore+".."+after), "approve R1") {
							t.Error("the committed DONE approval was published despite failed reversion")
						}
					}
					if c.scenario != "unavailable" {
						if b, rerr := os.ReadFile(edited); rerr != nil || string(b) != authored {
							t.Errorf("authored nested bytes not preserved: %q %v", b, rerr)
						}
						if c.scenario != "dirty" {
							v := nestedCheckGit(t, filepath.Dir(edited), "ls-files", "-v", "value.txt")
							want := map[string]string{"assume-unchanged": "h", "skip-worktree": "S"}[c.scenario]
							if !strings.HasPrefix(v, want+" ") {
								t.Errorf("hidden index flag not preserved: %q", v)
							}
						}
					}
				})
			}
		}
	}
}
