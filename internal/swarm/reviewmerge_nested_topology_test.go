package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
)

func TestReviewMergeRestoresNestedTopology(t *testing.T) {
	for _, topology := range []string{"add", "remove", "child-add", "child-remove"} {
		for _, pushRetry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/push-retry=%t", topology, pushRetry), func(t *testing.T) {
				f := newNestedCheckFixture(t, false, false)
				peer := filepath.Join(t.TempDir(), "topology")
				nestedCheckGit(t, f.wt, "clone", "-q", "-b", "bee-R1", f.origin, peer)
				gitConfig(t, peer)
				nestedCheckGit(t, peer, "submodule", "update", "--init", "--recursive")
				where, path := peer, "nested"
				if strings.HasPrefix(topology, "child-") {
					where, path = filepath.Join(peer, "nested"), "child"
					gitConfig(t, where)
				}
				if strings.HasSuffix(topology, "add") {
					url := nestedCheckGit(t, filepath.Join(where, path), "remote", "get-url", "origin")
					nestedCheckGit(t, where, "submodule", "add", "-q", url, "added")
				} else {
					nestedCheckGit(t, where, "submodule", "deinit", "--all")
					nestedCheckGit(t, where, "rm", "-q", path)
				}
				nestedCheckGit(t, where, "commit", "-qam", "change nested topology")
				if where != peer {
					nestedCheckGit(t, where, "push", "origin", "HEAD:refs/heads/topology")
					nestedCheckGit(t, peer, "add", "nested")
					nestedCheckGit(t, peer, "commit", "-qm", "pin changed child topology")
				}
				if !pushRetry {
					nestedCheckGit(t, peer, "push", "origin", "HEAD:bee-R1")
				}
				task := f.task(t)
				task.Body = []string{"Review topology.", fmt.Sprintf("Check: touch %q; cmp expected.txt expected.txt", f.marker)}
				nestedCheckWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
				nestedCheckGit(t, f.root, "commit", "-qam", "check topology")

				before := nestedCheckGit(t, f.wt, "rev-parse", "HEAD")
				pins := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive")
				code := f.sel.Submodule.RepoDir()
				otherHead := nestedCheckGit(t, code, "rev-parse", "HEAD")
				otherPins := nestedCheckGit(t, code, "submodule", "status", "--recursive")
				otherStatus := nestedCheckGit(t, code, "status", "--porcelain")
				originHead := before
				failAt := 1
				if pushRetry {
					failAt = 2
				}
				calls := 0
				f.r.RunVerify = func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
					calls++
					o, err := realRunVerify(ctx, dir, name, args...)
					if err != nil || o.exitErr {
						t.Fatalf("real check: %+v %v", o, err)
					}
					if status := nestedCheckGit(t, dir, "status", "--porcelain", "--untracked-files=all"); status != "" {
						t.Errorf("merged topology left stale paths before check: %s", status)
					}
					for _, pin := range strings.Split(nestedCheckGit(t, dir, "submodule", "status", "--recursive"), "\n") {
						if strings.HasPrefix(pin, "-") || strings.HasPrefix(pin, "+") || strings.HasPrefix(pin, "U") {
							t.Errorf("check launched without initialized exact recursive pin: %s", pin)
						}
					}
					if pushRetry && calls == 1 {
						nestedCheckGit(t, peer, "push", "origin", "HEAD:main")
						originHead = nestedCheckGit(t, peer, "rev-parse", "HEAD")
					}
					o.out, o.exitErr = "topology check diagnostic", calls == failAt
					return o, nil
				}
				hint, revert, err := f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root)
				if err != nil || !strings.Contains(hint, "topology check diagnostic") || revert != plan.StatusReview {
					t.Fatalf("failed check must reject merge with diagnostic: hint=%q revert=%s err=%v", hint, revert, err)
				}
				if got := nestedCheckGit(t, f.wt, "rev-parse", "HEAD"); got != before {
					t.Errorf("rollback HEAD = %s, want %s", got, before)
				}
				if got := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive"); got != pins {
					t.Errorf("rollback pins = %s, want %s", got, pins)
				}
				if got := nestedCheckGit(t, f.wt, "status", "--porcelain", "--untracked-files=all"); got != "" {
					t.Errorf("rollback left stale paths: %s", got)
				}
				for _, path := range []string{"added", "nested/added"} {
					if entries, err := os.ReadDir(filepath.Join(f.wt, path)); err == nil && len(entries) != 0 {
						t.Errorf("rollback retained added checkout %s", path)
					} else if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
				if got := nestedCheckGit(t, f.origin, "rev-parse", "main"); got != originHead {
					t.Errorf("failed topology check pushed %s, want %s", got, originHead)
				}
				if hint, _, err := f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root); err != nil || hint != "" {
					t.Errorf("retry must pass: hint=%q err=%v", hint, err)
				}
				if calls != failAt+1 {
					t.Errorf("expected failed check then successful retry, got %d checks", calls)
				}
				if got := nestedCheckGit(t, code, "rev-parse", "HEAD"); got != otherHead {
					t.Errorf("other source HEAD moved: %s -> %s", otherHead, got)
				}
				if got := nestedCheckGit(t, code, "submodule", "status", "--recursive"); got != otherPins {
					t.Errorf("other source pins changed: %s -> %s", otherPins, got)
				}
				if got := nestedCheckGit(t, code, "status", "--porcelain"); got != otherStatus {
					t.Errorf("other source status changed: %s -> %s", otherStatus, got)
				}
			})
		}
	}
}

func TestReviewMergeTopologyRollbackPreservesEdits(t *testing.T) {
	for _, edit := range []string{"tracked", "staged", "staged-pin", "untracked", "ignored"} {
		for _, infrastructure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/infrastructure=%t", edit, infrastructure), func(t *testing.T) {
				f := newNestedCheckFixture(t, false, false)
				child := filepath.Join(f.wt, "nested", "child")
				path := filepath.Join(child, "value.txt")
				if edit == "untracked" || edit == "ignored" {
					path = filepath.Join(child, "agent-notes")
				}
				var checkHead string
				var stagedPin string
				f.r.RunVerify = func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
					o, err := realRunVerify(ctx, dir, name, args...)
					if err != nil || o.exitErr {
						t.Fatalf("real check: %+v %v", o, err)
					}
					checkHead = nestedCheckGit(t, dir, "rev-parse", "HEAD")
					if edit == "staged-pin" {
						stagedPin = nestedCheckGit(t, child, "rev-parse", "HEAD^")
						nestedCheckGit(t, filepath.Dir(child), "update-index", "--cacheinfo", "160000,"+stagedPin+",child")
					} else {
						nestedCheckWrite(t, path, "preserve this edit\n")
					}
					if edit == "staged" {
						nestedCheckGit(t, child, "add", "value.txt")
					}
					if edit == "ignored" {
						exclude := nestedCheckGit(t, child, "rev-parse", "--git-path", "info/exclude")
						if !filepath.IsAbs(exclude) {
							exclude = filepath.Join(child, exclude)
						}
						nestedCheckWrite(t, exclude, "agent-notes\n")
					}
					if infrastructure {
						o.out = "original check output before infrastructure failure"
						return o, fmt.Errorf("original infrastructure diagnostic")
					}
					return verifyOutcome{out: "original check diagnostic", exitErr: true}, nil
				}
				_, _, err := f.r.finalizeReviewMerge(context.Background(), f.sel, f.wt, f.root)
				diagnostic := "original check diagnostic"
				if infrastructure {
					diagnostic = "original infrastructure diagnostic"
				}
				if err == nil || !strings.Contains(err.Error(), diagnostic) || !strings.Contains(err.Error(), "restoring pre-check topology") {
					t.Errorf("must preserve original and recovery diagnostics: %v", err)
				}
				if infrastructure && (err == nil || !strings.Contains(err.Error(), "original check output before infrastructure failure")) {
					t.Errorf("must preserve check output alongside infrastructure failure: %v", err)
				}
				if edit == "staged-pin" {
					if got := nestedCheckGit(t, filepath.Dir(child), "rev-parse", ":child"); got != stagedPin {
						t.Errorf("rollback discarded staged gitlink: got %s, want %s", got, stagedPin)
					}
				} else if content, err := os.ReadFile(path); err != nil || string(content) != "preserve this edit\n" {
					t.Errorf("rollback discarded %s edit: %q %v", edit, content, err)
				}
				if got := nestedCheckGit(t, f.wt, "rev-parse", "HEAD"); got != checkHead {
					t.Errorf("unsafe rollback moved HEAD: %s -> %s", checkHead, got)
				}
				if edit == "staged" && nestedCheckGit(t, child, "show", ":value.txt") != "preserve this edit" {
					t.Error("rollback discarded staged edit")
				}
				if originMainContains(t, f.origin, f.wt, "feature.txt") {
					t.Error("unsafe rollback pushed unverified merge")
				}
			})
		}
	}
}

func TestReviewMergeTopologyDeinitPreflightsStagedPins(t *testing.T) {
	f := newNestedCheckFixture(t, false, false)
	url := nestedCheckGit(t, filepath.Join(f.wt, "nested"), "remote", "get-url", "origin")
	nestedCheckGit(t, f.wt, "submodule", "add", "-q", url, "z-later")
	nestedCheckGit(t, f.wt, "commit", "-qam", "add second nested checkout")
	nestedCheckGit(t, f.wt, "submodule", "update", "--init", "--recursive")
	later := filepath.Join(f.wt, "z-later")
	old := nestedCheckGit(t, filepath.Join(later, "child"), "rev-parse", "HEAD^")
	nestedCheckGit(t, later, "update-index", "--cacheinfo", "160000,"+old+",child")
	pins := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive")
	// Rejection must precede ALL removals, not just protect the dirty checkout.
	if err := git.New(f.wt).DeinitNestedSubmodules(context.Background()); err == nil {
		t.Fatal("staged nested gitlink must block deinit")
	}
	if got := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive"); got != pins {
		t.Errorf("failed preflight removed another checkout:\nbefore: %s\nafter: %s", pins, got)
	}
	if got := nestedCheckGit(t, later, "rev-parse", ":child"); got != old {
		t.Errorf("preflight lost staged gitlink: %s, want %s", got, old)
	}
}
