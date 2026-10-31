package swarm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

func nestedGateArtifacts(t *testing.T, f nestedRecoveryFixture, status plan.Status) {
	t.Helper()
	b, err := os.ReadFile(f.sel.Submodule.PlanPath())
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	p.Find("R1").Status = status
	if err := plan.WriteFile(f.sel.Submodule.PlanPath(), p); err != nil {
		t.Fatal(err)
	}
	nestedRecoveryWrite(t, f.sel.Submodule.Path, "CHECKS.md", "# Checks\n\n## go-test <!-- category=unit -->\nMatch: (^|&&|;|\\s)go\\s+test\\b\n")
	nestedRecoveryWrite(t, f.sel.Submodule.Path, "docs/bee-R1-R1.md", "<!-- Beehive-Commits: "+f.impl+" -->\n<!-- Beehive-Check: pass - pinned nested source tested -->\n")
	nestedRecoveryGit(t, f.root, "add", "submodules/sm/PLAN.md", "submodules/sm/CHECKS.md", "submodules/sm/docs/bee-R1-R1.md")
	nestedRecoveryGit(t, f.root, "commit", "-qm", "record gate artifacts")
}

func TestNestedPremergedGate(t *testing.T) {
	for _, kind := range []selectt.Kind{selectt.Review, selectt.Arbitrate, selectt.Work} {
		for _, dirty := range []string{"clean", "root", "nested", "leaf", "untracked", "staged", "root-gitlink", "nested-gitlink"} {
			t.Run(string(kind)+"/"+dirty, func(t *testing.T) {
				f := newNestedRecoveryFixture(t, "merge", "changed")
				f.sel.Kind = kind
				status := plan.Done
				if kind == selectt.Work {
					status = plan.NeedsReview
				}
				nestedGateArtifacts(t, f, status)
				oldDep := nestedRecoveryGit(t, filepath.Join(f.cwd, "deps/lib"), "rev-parse", "HEAD")
				oldLeaf := nestedRecoveryGit(t, filepath.Join(f.cwd, "deps/lib/leaf"), "rev-parse", "HEAD")
				nestedRecoveryGit(t, f.cwd, "-c", "submodule.recurse=false", "merge", "--no-ff", "-m", "agent premerge", "origin/bee-R1")
				head := nestedRecoveryGit(t, f.cwd, "rev-parse", "HEAD")
				var authored string
				switch dirty {
				case "root":
					authored = "unrelated.txt"
				case "nested", "staged":
					authored = "deps/lib/value.go"
				case "leaf":
					authored = "deps/lib/leaf/value.go"
				case "untracked":
					authored = "deps/lib/leaf/local-only.txt"
				case "root-gitlink":
					nestedRecoveryGit(t, f.cwd, "add", "deps/lib")
				case "nested-gitlink":
					nestedRecoveryGit(t, filepath.Join(f.cwd, "deps/lib"), "update-index", "--cacheinfo", "160000,"+f.leaf+",leaf")
				}
				if authored != "" {
					nestedRecoveryWrite(t, f.cwd, authored, "authored data must survive\n")
					if dirty == "staged" {
						nestedRecoveryGit(t, filepath.Join(f.cwd, "deps/lib"), "add", "value.go")
					}
				}
				before := nestedRecoveryGit(t, f.cwd, "status", "--porcelain")
				indexBefore := nestedRecoveryGit(t, f.cwd, "diff", "--cached", "--raw", "--ignore-submodules=none")
				nestedIndexBefore := nestedRecoveryGit(t, filepath.Join(f.cwd, "deps/lib"), "diff", "--cached", "--raw", "--ignore-submodules=none")
				hint, err := f.r.verifyGate(context.Background(), f.sel, f.cwd, f.root, "bee-R1")
				if err != nil {
					t.Fatal(err)
				}
				if dirty == "clean" && kind != selectt.Work {
					if hint != "" {
						t.Fatalf("clean premerge rejected: %s", hint)
					}
					assertNestedWorktreePins(t, f.cwd, f.dep, f.leaf)
				} else {
					if !strings.Contains(hint, "UNCOMMITTED") {
						t.Fatalf("authored changes/pin drift must be rejected: %q", hint)
					}
					if after := nestedRecoveryGit(t, f.cwd, "status", "--porcelain"); after != before {
						t.Errorf("gate changed dirty status: %q -> %q", before, after)
					}
					for path, pin := range map[string]string{"deps/lib": oldDep, "deps/lib/leaf": oldLeaf} {
						if got := nestedRecoveryGit(t, filepath.Join(f.cwd, path), "rev-parse", "HEAD"); got != pin {
							t.Errorf("rejected gate moved %s: %s -> %s", path, pin, got)
						}
					}
					if authored != "" {
						body, err := os.ReadFile(filepath.Join(f.cwd, authored))
						if err != nil || string(body) != "authored data must survive\n" {
							t.Fatalf("authored data changed: %q, %v", body, err)
						}
					}
				}
				if got := nestedRecoveryGit(t, f.cwd, "rev-parse", "HEAD"); got != head {
					t.Errorf("gate moved source HEAD: %s -> %s", head, got)
				}
				if got := nestedRecoveryGit(t, f.cwd, "diff", "--cached", "--raw", "--ignore-submodules=none"); got != indexBefore {
					t.Errorf("gate changed source index: %q -> %q", indexBefore, got)
				}
				if got := nestedRecoveryGit(t, filepath.Join(f.cwd, "deps/lib"), "diff", "--cached", "--raw", "--ignore-submodules=none"); got != nestedIndexBefore {
					t.Errorf("gate changed nested index: %q -> %q", nestedIndexBefore, got)
				}
			})
		}
	}
}

func TestNestedRunGateInfrastructureFailure(t *testing.T) {
	for _, failure := range []string{"unavailable-origin", "unavailable-pin", "verify-infrastructure"} {
		for _, heartbeat := range []bool{false, true} {
			for _, revertFails := range []bool{false, true} {
				name := failure + "/post-turn"
				if heartbeat {
					name = failure + "/heartbeat"
				}
				if revertFails {
					name += "/revert-fails"
				}
				t.Run(name, func(t *testing.T) {
					variant := failure
					if failure == "verify-infrastructure" {
						variant = "introduced"
					}
					f := newNestedRecoveryFixture(t, "merge", variant)
					nestedGateArtifacts(t, f, plan.NeedsReview)
					nestedRecoveryGit(t, f.root, "checkout", "-b", "gate-pass")
					before := nestedRecoveryGit(t, f.origin, "show-ref", "--heads")
					publishes, afterGate := 0, 0
					gateStarted := false
					f.r.Publish = func(context.Context) error {
						publishes++
						if gateStarted {
							afterGate++
						}
						body := nestedRecoveryGit(t, f.root, "show", "HEAD:submodules/sm/PLAN.md")
						p, err := plan.Parse(body)
						if err != nil || p.Find("R1").Status != plan.NeedsReview {
							t.Errorf("publishing unaccepted status: %s (err %v)", body, err)
						}
						nestedRecoveryGit(t, f.root, "update-ref", "refs/heads/main", "HEAD")
						return nil
					}
					f.r.RunVerify = func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
						if name == "git" && len(args) > 0 && args[0] == "status" {
							gateStarted = true
							if revertFails {
								nestedRecoveryWrite(t, f.root, ".git/index.lock", "block revert commit\n")
							}
							if failure == "verify-infrastructure" {
								return verifyOutcome{}, errors.New("injected gate infrastructure failure")
							}
						}
						return realRunVerify(ctx, dir, name, args...)
					}
					sess := &mockSession{onTurn: func(int) { nestedGateArtifacts(t, f, plan.Done) }}
					if heartbeat {
						// Simulate a committed flip observed by the top-of-turn heartbeat.
						nestedGateArtifacts(t, f, plan.Done)
					}
					f.r.Client, f.r.MaxTurns = &mockClient{sess: sess}, 1
					res, err := f.r.Run(context.Background(), f.sel, "system", "first")
					if err == nil || res.Completed || !gateStarted {
						t.Fatalf("Run = %+v, err = %v, gate started = %v", res, err, gateStarted)
					}
					wantError := "prepare nested submodules"
					if failure == "verify-infrastructure" {
						wantError = "injected gate infrastructure failure"
					}
					if !strings.Contains(err.Error(), wantError) {
						t.Errorf("wrong infrastructure failure: %v", err)
					}
					if revertFails {
						if afterGate != 0 || !strings.Contains(err.Error(), "revert gated flip") {
							t.Errorf("failed revert must not finish/publish: publishes=%d err=%v", afterGate, err)
						}
					} else if afterGate != 1 {
						t.Errorf("want reverted publication, got %d after gate (%d total)", afterGate, publishes)
					}
					body := nestedRecoveryGit(t, f.root, "show", "main:submodules/sm/PLAN.md")
					p, parseErr := plan.Parse(body)
					if parseErr != nil || p.Find("R1").Status != plan.NeedsReview {
						t.Errorf("published main must remain NEEDS-REVIEW: %s, %v", body, parseErr)
					}
					if after := nestedRecoveryGit(t, f.origin, "show-ref", "--heads"); after != before {
						t.Errorf("infrastructure failure pushed source: %s -> %s", before, after)
					}
				})
			}
		}
	}
}
