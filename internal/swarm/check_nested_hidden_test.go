package swarm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/git"
)

func TestNestedHiddenIndexState(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		for _, level := range []string{"z-later", "z-later/child"} {
			for _, operation := range []string{"check", "pin-drift", "deinit", "handoff", "rollback", "rollback-infrastructure"} {
				t.Run(flag+"/"+level+"/"+operation, func(t *testing.T) {
					f := newNestedCheckFixture(t, false, false)
					ctx := context.Background()
					// Put the hidden edit after a clean sibling in traversal order, so
					// rejecting only when visiting the affected checkout is too late.
					url := nestedCheckGit(t, filepath.Join(f.wt, "nested"), "remote", "get-url", "origin")
					nestedCheckGit(t, f.wt, "submodule", "add", "-q", url, "z-later")
					nestedCheckGit(t, f.wt, "commit", "-qam", "add clean sibling")
					if operation != "pin-drift" && !strings.HasPrefix(operation, "rollback") {
						nestedCheckGit(t, f.wt, "fetch", "origin", "bee-R1")
						nestedCheckGit(t, f.wt, "merge", "--no-edit", "origin/bee-R1")
					}
					if err := git.New(f.wt).PrepareNestedSubmodules(ctx); err != nil {
						t.Fatal(err)
					}
					if operation == "pin-drift" {
						nestedCheckGit(t, f.wt, "fetch", "origin", "bee-R1")
						nestedCheckGit(t, f.wt, "merge", "--no-edit", "origin/bee-R1")
					}
					code := f.sel.Submodule.RepoDir()
					sourceHead := nestedCheckGit(t, code, "rev-parse", "HEAD")
					sourcePins := nestedCheckGit(t, code, "submodule", "status", "--recursive")
					config := nestedCheckGit(t, code, "config", "--local", "--null", "--list")
					var head, pins string
					saved := make(map[string][]byte)
					editAndSnapshot := func() {
						dir := filepath.Join(f.wt, level)
						nestedCheckGit(t, dir, "update-index", flag, "value.txt")
						nestedCheckWrite(t, filepath.Join(dir, "value.txt"), "hidden authored content\n")
						head = nestedCheckGit(t, f.wt, "rev-parse", "HEAD")
						pins = nestedCheckGit(t, f.wt, "submodule", "status", "--recursive")
						for _, path := range []string{"nested", "nested/child", "z-later", "z-later/child"} {
							child := filepath.Join(f.wt, path)
							index := nestedCheckGit(t, child, "rev-parse", "--path-format=absolute", "--git-path", "index")
							for _, file := range []string{index, filepath.Join(child, "value.txt"), filepath.Join(child, ".git")} {
								b, err := os.ReadFile(file)
								if err != nil {
									t.Fatal(err)
								}
								saved[file] = b
							}
						}
					}
					var err error
					if strings.HasPrefix(operation, "rollback") {
						calls := 0
						f.r.RunVerify = func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
							calls++
							out, err := realRunVerify(ctx, dir, name, args...)
							if err != nil || out.exitErr {
								t.Fatalf("real check: %+v %v", out, err)
							}
							editAndSnapshot()
							if operation == "rollback-infrastructure" {
								return verifyOutcome{out: "original check output"}, fmt.Errorf("original infrastructure failure")
							}
							return verifyOutcome{out: "original check output", exitErr: true}, nil
						}
						_, _, err = f.r.finalizeReviewMerge(ctx, f.sel, f.wt, f.root)
						if calls != 1 || err == nil || !strings.Contains(err.Error(), "original check output") || !strings.Contains(err.Error(), "restoring pre-check topology") {
							t.Errorf("rollback must retain check and restoration failures: calls=%d err=%v", calls, err)
						}
						if originMainContains(t, f.origin, f.wt, "feature.txt") {
							t.Error("failed check pushed merge")
						}
					} else {
						editAndSnapshot()
						switch operation {
						case "check", "pin-drift":
							_, err = f.r.runCheckIn(ctx, f.sel, f.task(t).Check(), f.wt)
						case "deinit":
							err = git.New(f.wt).DeinitNestedSubmodules(ctx)
						case "handoff":
							err = git.New(f.wt).PrepareCleanNestedCheckout(ctx)
						}
						if _, statErr := os.Stat(f.marker); !os.IsNotExist(statErr) {
							t.Error("check launched against hidden authored content")
						}
					}
					if err == nil {
						t.Error("hidden index state must fail closed")
					}
					// Read raw index bytes before any status command can refresh them.
					for file, want := range saved {
						if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, want) {
							t.Errorf("modified or removed authored/sibling state at %s: %v", file, err)
						}
					}
					if got := nestedCheckGit(t, f.wt, "rev-parse", "HEAD"); got != head {
						t.Errorf("rejection moved parent HEAD: %s -> %s", head, got)
					}
					if got := nestedCheckGit(t, f.wt, "submodule", "status", "--recursive"); got != pins {
						t.Errorf("rejection moved or removed nested pins: %s -> %s", pins, got)
					}
					if got := nestedCheckGit(t, code, "rev-parse", "HEAD"); got != sourceHead {
						t.Error("shared source HEAD changed")
					}
					if got := nestedCheckGit(t, code, "submodule", "status", "--recursive"); got != sourcePins {
						t.Error("shared source pins changed")
					}
					if got := nestedCheckGit(t, code, "config", "--local", "--null", "--list"); got != config {
						t.Error("shared source configuration changed")
					}
				})
			}
		}
	}
}
