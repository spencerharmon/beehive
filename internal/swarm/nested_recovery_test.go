package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/checkpolicy"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

func nestedRecoveryGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git.New(dir).Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(out)
}

func nestedRecoveryWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type nestedRecoveryFixture struct {
	r                 *Runner
	sel               *selectt.Selection
	root, cwd, origin string
	impl, dep, leaf   string
}

// The checked program imports a nested module which itself imports a second-level
// gitlink. Both origins advance beyond the wanted pins, so --remote is incorrect.
func newNestedRecoveryFixture(t *testing.T, route, variant string) nestedRecoveryFixture {
	t.Helper()
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}

	leafDir := t.TempDir()
	gitInit(t, leafDir)
	var leafPins []string
	for i := 0; i < 3; i++ {
		nestedRecoveryWrite(t, leafDir, "value.go", fmt.Sprintf("package leaf\nconst Value = %d\n", i))
		nestedRecoveryGit(t, leafDir, "add", "value.go")
		nestedRecoveryGit(t, leafDir, "commit", "-qm", fmt.Sprintf("leaf %d", i))
		leafPins = append(leafPins, nestedRecoveryGit(t, leafDir, "rev-parse", "HEAD"))
	}
	leafOrigin := filepath.Join(t.TempDir(), "leaf.git")
	nestedRecoveryGit(t, root, "clone", "--bare", leafDir, leafOrigin)
	depDir := t.TempDir()
	gitInit(t, depDir)
	nestedRecoveryWrite(t, depDir, "go.mod", "module example.com/dep\n\ngo 1.22\n")
	nestedRecoveryWrite(t, depDir, "value.go", "package dep\nimport \"example.com/dep/leaf\"\nconst Value = leaf.Value\n")
	nestedRecoveryGit(t, depDir, "submodule", "add", leafOrigin, "leaf")
	var depPins []string
	for i, pin := range leafPins {
		nestedRecoveryGit(t, filepath.Join(depDir, "leaf"), "checkout", "--detach", pin)
		nestedRecoveryGit(t, depDir, "add", ".")
		nestedRecoveryGit(t, depDir, "commit", "-qm", fmt.Sprintf("dependency %d", i))
		depPins = append(depPins, nestedRecoveryGit(t, depDir, "rev-parse", "HEAD"))
	}
	depOrigin := filepath.Join(t.TempDir(), "dep.git")
	nestedRecoveryGit(t, root, "clone", "--bare", depDir, depOrigin)
	wantedDep := depPins[1]
	if variant == "unavailable-origin" || variant == "unavailable-pin" {
		nestedRecoveryGit(t, depDir, "checkout", "--detach", wantedDep)
		nestedRecoveryGit(t, filepath.Join(depDir, "leaf"), "checkout", "--detach", leafPins[1])
		if variant == "unavailable-origin" {
			nestedRecoveryGit(t, depDir, "config", "-f", ".gitmodules", "submodule.leaf.url", filepath.Join(t.TempDir(), "missing.git"))
			nestedRecoveryGit(t, depDir, "add", ".gitmodules")
		} else {
			// A real commit unavailable in the leaf origin, not a syntactically invalid SHA.
			nestedRecoveryWrite(t, leafDir, "private.go", "package leaf\nconst Unpublished = true\n")
			nestedRecoveryGit(t, leafDir, "add", "private.go")
			nestedRecoveryGit(t, leafDir, "commit", "-qm", "unpublished pin")
			missing := nestedRecoveryGit(t, leafDir, "rev-parse", "HEAD")
			nestedRecoveryGit(t, depDir, "update-index", "--cacheinfo", "160000,"+missing+",leaf")
		}
		nestedRecoveryGit(t, depDir, "commit", "-qm", "unavailable recursive dependency")
		wantedDep = nestedRecoveryGit(t, depDir, "rev-parse", "HEAD")
		nestedRecoveryGit(t, depDir, "push", depOrigin, "HEAD:refs/heads/broken")
	}

	origin := bareOriginSeeded(t, g)
	author := filepath.Join(t.TempDir(), "author")
	nestedRecoveryGit(t, root, "clone", origin, author)
	gitConfig(t, author)
	if variant == "changed" {
		nestedRecoveryGit(t, author, "submodule", "add", depOrigin, "deps/lib")
		nestedRecoveryGit(t, filepath.Join(author, "deps/lib"), "checkout", "--detach", depPins[0])
		nestedRecoveryGit(t, author, "add", ".")
		nestedRecoveryGit(t, author, "commit", "-qm", "old nested dependency")
		nestedRecoveryGit(t, author, "push", "origin", "main")
	}
	base := nestedRecoveryGit(t, author, "rev-parse", "HEAD")
	nestedRecoveryGit(t, author, "checkout", "-b", "bee-R1")
	if variant != "changed" {
		nestedRecoveryGit(t, author, "submodule", "add", depOrigin, "deps/lib")
	}
	nestedRecoveryGit(t, filepath.Join(author, "deps/lib"), "checkout", "--detach", wantedDep)
	nestedRecoveryWrite(t, author, "go.mod", "module example.com/check\n\ngo 1.22\nrequire example.com/dep v0.0.0\nreplace example.com/dep => ./deps/lib\n")
	nestedRecoveryWrite(t, author, "nested_test.go", "package check\nimport (\"testing\"; \"example.com/dep\")\nfunc TestNestedValue(t *testing.T) { if dep.Value != 1 { t.Fatalf(\"nested value = %d, want pinned 1\", dep.Value) } }\n")
	nestedRecoveryGit(t, author, "add", ".")
	nestedRecoveryGit(t, author, "commit", "-qm", "check pinned nested source")
	impl := nestedRecoveryGit(t, author, "rev-parse", "HEAD")
	nestedRecoveryGit(t, author, "push", "origin", "bee-R1")
	// Diverge main to ensure finalizeReviewMerge performs an actual merge.
	nestedRecoveryGit(t, author, "checkout", "main")
	nestedRecoveryWrite(t, author, "unrelated.txt", "tracked branch advances\n")
	nestedRecoveryGit(t, author, "add", "unrelated.txt")
	nestedRecoveryGit(t, author, "commit", "-qm", "independent main work")
	if route != "merge" {
		nestedRecoveryGit(t, author, "merge", "--no-ff", "-m", "interrupted review merge", "bee-R1")
	}
	nestedRecoveryGit(t, author, "push", "origin", "main")
	if route == "record" {
		nestedRecoveryGit(t, author, "push", "origin", "--delete", "bee-R1")
	}

	sm := filepath.Join(root, "submodules", "sm")
	nestedRecoveryWrite(t, sm, "docs/bee-R1-R1.md", "<!-- Beehive-Commits: "+impl+" -->\n\nNested source change.\n")
	repoDir := filepath.Join(sm, "repo")
	nestedRecoveryGit(t, root, "clone", origin, repoDir)
	gitConfig(t, repoDir)
	nestedRecoveryGit(t, repoDir, "checkout", "--detach", base)
	status := plan.StatusReview
	if route == "merge" {
		status = plan.StatusDone
	}
	nestedRecoveryWrite(t, sm, "PLAN.md", fmt.Sprintf("## R1 [%s] <!-- attempts=2 deps= commits=%s review=%s -->\nreview\nCheck: go test -count=1 ./...\n", status, impl, impl))
	nestedRecoveryGit(t, root, "add", "submodules/sm")
	nestedRecoveryGit(t, root, "commit", "-qm", "seed nested recovery")
	rp, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	subs, err := rp.Submodules()
	if err != nil || len(subs) != 1 {
		t.Fatalf("submodules = %v, err = %v", subs, err)
	}
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{
		ID: "R1", Status: plan.StatusReview, Attempts: 2, ReviewCommit: impl,
		Body: []string{"review", "Check: go test -count=1 ./..."},
	}}
	r := &Runner{Repo: rp, Git: g, TTL: time.Hour}
	cwd := repoDir
	if route == "merge" {
		var ok bool
		cwd, _, ok = r.setupReviewWorktree(ctx, sel, "bee-R1", root)
		if !ok {
			t.Fatal("setupReviewWorktree failed")
		}
	}
	if variant == "changed" {
		nestedRecoveryGit(t, cwd, "submodule", "update", "--init", "--recursive")
		if got := nestedRecoveryGit(t, filepath.Join(cwd, "deps/lib/leaf"), "rev-parse", "HEAD"); got != leafPins[0] {
			t.Fatalf("fixture must start at old leaf pin: %s", got)
		}
	} else if _, err := os.Stat(filepath.Join(cwd, "deps/lib/value.go")); !os.IsNotExist(err) {
		t.Fatalf("fixture must start without nested source, stat err = %v", err)
	}
	return nestedRecoveryFixture{r: r, sel: sel, root: root, cwd: cwd, origin: origin, impl: impl, dep: wantedDep, leaf: leafPins[1]}
}

func TestNestedSubmoduleMergeRecovery(t *testing.T) {
	for _, route := range []string{"merge", "branch", "record"} {
		for _, policy := range []string{"nil-policy", "configured-policy"} {
			for _, variant := range []string{"introduced", "changed", "unavailable-origin", "unavailable-pin"} {
				t.Run(route+"/"+policy+"/"+variant, func(t *testing.T) {
					f := newNestedRecoveryFixture(t, route, variant)
					if policy == "configured-policy" {
						f.r.CheckPolicy = &checkpolicy.Policy{Sandbox: checkpolicy.SandboxOff}
					}
					ctx := context.Background()
					before := nestedRecoveryGit(t, f.root, "rev-parse", "HEAD")
					originBefore := nestedRecoveryGit(t, f.origin, "rev-parse", "main")
					planBefore, err := os.ReadFile(f.sel.Submodule.PlanPath())
					if err != nil {
						t.Fatal(err)
					}
					checks, publishes := 0, 0
					var checkOutput string
					f.r.RunVerify = func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
						checks++
						if dir != f.cwd {
							t.Errorf("check cwd = %s, want merged tree %s", dir, f.cwd)
						}
						o, err := realRunVerify(ctx, dir, name, args...)
						checkOutput = o.out
						return o, err
					}
					f.r.Publish = func(context.Context) error { publishes++; return nil }
					var handled bool
					var hint string
					var revert plan.Status
					switch route {
					case "merge":
						hint, revert, err = f.r.finalizeReviewMerge(ctx, f.sel, f.cwd, f.root)
						handled = err == nil && hint == ""
					case "branch":
						handled, err = f.r.finalizeIfAlreadyMerged(ctx, f.sel, "bee-R1", f.root)
					case "record":
						handled, err = f.r.finalizeIfMergedByRecord(ctx, f.sel, f.root)
					}
					pb, readErr := os.ReadFile(f.sel.Submodule.PlanPath())
					if readErr != nil {
						t.Fatal(readErr)
					}
					p, parseErr := plan.Parse(string(pb))
					if parseErr != nil || p.Find("R1") == nil {
						t.Fatalf("invalid resulting plan: %v\n%s", parseErr, pb)
					}
					task := p.Find("R1")
					if strings.HasPrefix(variant, "unavailable-") {
						var pv policyViolationError
						if err == nil || errors.As(err, &pv) || handled || hint != "" || revert != "" {
							t.Errorf("setup failure must be infrastructure error, not rejection/completion: handled=%v hint=%q revert=%q err=%v; check output:\n%s", handled, hint, revert, err, checkOutput)
						}
						if checks != 0 || publishes != 0 {
							t.Errorf("setup failure ran %d checks and %d publishes, want zero", checks, publishes)
						}
						if string(pb) != string(planBefore) || task.Attempts != 2 {
							t.Errorf("setup failure changed task or bumped attempts:\n%s", pb)
						}
						if got := nestedRecoveryGit(t, f.root, "rev-parse", "HEAD"); got != before {
							t.Errorf("setup failure committed hive bookkeeping: %s -> %s", before, got)
						}
						if got := nestedRecoveryGit(t, f.origin, "rev-parse", "main"); got != originBefore {
							t.Errorf("setup failure pushed source: %s -> %s", originBefore, got)
						}
						return
					}
					if err != nil || !handled || hint != "" || revert != "" || task.Status != plan.StatusDone || task.Attempts != 2 {
						t.Fatalf("nested source must finalize without false reopen: handled=%v hint=%q revert=%q err=%v task=%+v; check output:\n%s", handled, hint, revert, err, task, checkOutput)
					}
					if checks != 1 || !strings.Contains(checkOutput, "ok") {
						t.Fatalf("want one real passing Go test, got %d checks: %s", checks, checkOutput)
					}
					for path, pin := range map[string]string{"deps/lib": f.dep, "deps/lib/leaf": f.leaf} {
						if got := nestedRecoveryGit(t, filepath.Join(f.cwd, path), "rev-parse", "HEAD"); got != pin {
							t.Errorf("%s HEAD = %s, want committed pin %s", path, got, pin)
						}
					}
					if !plan.SameCommitSet(task.Commits, []string{f.impl}) {
						t.Errorf("recorded commits = %v, want implementer %s", task.Commits, f.impl)
					}
					if route == "merge" {
						tip := nestedRecoveryGit(t, f.cwd, "rev-parse", "HEAD")
						if got := nestedRecoveryGit(t, f.origin, "rev-parse", "main"); got != tip || got == originBefore {
							t.Errorf("merged source not pushed: origin=%s merged=%s before=%s", got, tip, originBefore)
						}
						if parents := strings.Fields(nestedRecoveryGit(t, f.cwd, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 {
							t.Errorf("expected actual two-parent review merge, got %v", parents)
						}
					} else {
						if publishes != 1 {
							t.Errorf("recovery publishes = %d, want 1", publishes)
						}
						if got := nestedRecoveryGit(t, f.root, "rev-parse", "HEAD:submodules/sm/repo"); got != originBefore {
							t.Errorf("recovery gitlink = %s, want tracked tip %s", got, originBefore)
						}
					}
				})
			}
		}
	}
}
