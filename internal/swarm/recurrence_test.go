package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/claim"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

const cyclePlanRel = "submodules/sm/PLAN.md"
const cycleDocRel = "submodules/sm/docs/bee-standing-standing.md"

type cycleFixture struct {
	runner *Runner
	sel    *selectt.Selection
	code   *git.Repo
	sha    string
}

func cycleGit(t *testing.T, g *git.Repo, args ...string) string {
	t.Helper()
	out, err := g.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, g.Dir, err)
	}
	return out
}

func cycleWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cycleTask(t *testing.T, text string) *plan.Task {
	t.Helper()
	p, err := plan.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	task := p.Find("standing")
	if task == nil {
		t.Fatal("standing task missing")
	}
	return task
}

func newCycleFixture(t *testing.T) *cycleFixture {
	t.Helper()
	t.Setenv("BEEHIVE_CYCLE_TEST_MODE", "")
	root := t.TempDir()
	g := gitInit(t, root)
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	sub := repo.Submodule{Name: "sm", Path: filepath.Join(root, "submodules", "sm")}
	// A real test runner, not an exit-code stub: its output proves execution in
	// the tracked code checkout, and the modes exercise failed/mutating checks.
	cycleWrite(t, filepath.Join(sub.RepoDir(), "cycle_test.go"), `package cycle
import (
    "os"
    "testing"
)
func TestMeasurement(t *testing.T) {
    b, err := os.ReadFile("measurement.txt")
    if err != nil || string(b) != "healthy\n" {
        t.Fatalf("measurement: %q, %v", b, err)
    }
    t.Log("recurrence measurement executed")
    switch os.Getenv("BEEHIVE_CYCLE_TEST_MODE") {
    case "fail":
        t.Fatal("measurement is unhealthy")
    case "dirty":
        if err := os.WriteFile("measurement.txt", []byte("changed\n"), 0644); err != nil {
            t.Fatal(err)
        }
    }
}
`)
	cycleWrite(t, filepath.Join(sub.RepoDir(), "measurement.txt"), "healthy\n")
	code := gitInit(t, sub.RepoDir())
	if err := code.Commit(context.Background(), "seed measurement test"); err != nil {
		t.Fatal(err)
	}
	sha := cycleGit(t, code, "rev-parse", "HEAD")
	cycleWrite(t, filepath.Join(root, ".gitmodules"), "[submodule \"sm\"]\n\tpath = submodules/sm/repo\n\turl = ./submodules/sm/repo\n\tbranch = main\n")
	cycleWrite(t, sub.ChecksPath(), "# Checks\n\n## go-test <!-- category=unit -->\nMatch: (^|&&|;|\\s)go\\s+test\\b\n")
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	task := plan.Task{
		ID: "standing", Status: plan.TODO, Recurrence: 24 * time.Hour,
		Session: "bee-cycle-session", Heartbeat: now, Defers: 12,
		Body: []string{"Measure the standing task.", "Check: go test -count=1 -v cycle_test.go"},
	}
	cycleWrite(t, sub.PlanPath(), task.Card()+"\n")
	if err := g.Commit(context.Background(), "seed recurring task and gitlink"); err != nil {
		t.Fatal(err)
	}
	if got := cycleGit(t, g, "ls-tree", "HEAD", "submodules/sm/repo"); !strings.HasPrefix(got, "160000 commit "+sha) {
		t.Fatalf("fixture needs a real gitlink to clean code HEAD: %s", got)
	}
	cycleGit(t, g, "switch", "-c", task.Session)
	r := &Runner{
		Repo: &repo.Repo{Root: root}, Git: g, Session: task.Session,
		Now: func() time.Time { return now }, TTL: time.Hour, MaxTurns: 2,
	}
	return &cycleFixture{runner: r, sel: &selectt.Selection{Kind: selectt.Work, Submodule: sub, Task: task}, code: code, sha: sha}
}

func (f *cycleFixture) assertRecorded(t *testing.T, ref string) {
	t.Helper()
	r := f.runner
	task := cycleTask(t, cycleGit(t, r.Git, "show", ref+":"+cyclePlanRel))
	if task.Status != plan.TODO || task.Cycles != f.sel.Task.Cycles+1 || task.Defers != 12 ||
		task.Session != "" || !task.Heartbeat.IsZero() || task.CycleSession != r.Session ||
		!task.LastCycle.Equal(r.now()) || !task.NotBefore.Equal(r.now().Add(24*time.Hour)) ||
		!task.CommitsSet || len(task.Commits) != 0 {
		t.Fatalf("incorrect committed cycle state: %s", task.Card())
	}
	doc := cycleGit(t, r.Git, "show", ref+":"+cycleDocRel)
	for _, want := range []string{
		"<!-- Beehive-Commits: none -->", fmt.Sprintf("## Successful cycle %d", task.Cycles),
		cycleMarker(task, f.sha), "Check: `" + task.Check() + "`",
		"Code: `" + f.sha + "`; exit 0; next run: `" + task.NotBefore.Format(time.RFC3339) + "`",
		"recurrence measurement executed", "--- PASS: TestMeasurement",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("committed cycle doc missing %q:\n%s", want, doc)
		}
	}
}

func TestRecordCycleSuccess(t *testing.T) {
	f := newCycleFixture(t)
	r := f.runner
	before := cycleGit(t, r.Git, "rev-parse", "HEAD")
	r.Publish = func(context.Context) error {
		t.Error("RecordCycle must not publish")
		return nil
	}
	if err := r.RecordCycle(context.Background(), "sm", "standing", f.code.Dir); err != nil {
		t.Fatal(err)
	}
	f.assertRecorded(t, "HEAD")
	if got := cycleGit(t, r.Git, "rev-list", "--count", before+"..HEAD"); got != "1" {
		t.Fatalf("cycle must atomically commit plan and doc once, got %s commits", got)
	}
	if got := cycleGit(t, r.Git, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"); got != cyclePlanRel+"\n"+cycleDocRel {
		t.Fatalf("cycle committed unexpected paths: %s", got)
	}
	if got := cycleGit(t, r.Git, "rev-parse", "main"); got != before {
		t.Fatal("RecordCycle advanced main")
	}
	if got := cycleGit(t, r.Git, "status", "--porcelain"); got != "" {
		t.Fatalf("cycle left dirty hive: %s", got)
	}
	if got := cycleGit(t, f.code, "rev-parse", "HEAD"); got != f.sha {
		t.Fatal("cycle changed code HEAD")
	}
	if done, err := r.complete(f.sel, "bee-standing"); err != nil || !done {
		t.Fatalf("committed cycle not complete: done=%v err=%v", done, err)
	}
	// Runner.Run creates this source branch before the uniform mutation guard.
	cycleGit(t, f.code, "branch", "bee-standing")
	if hint, err := r.verifyGate(context.Background(), f.sel, f.code.Dir, r.Repo.Root, "bee-standing"); err != nil || hint != "" {
		t.Fatalf("committed cycle failed gate: %q %v", hint, err)
	}
	// Publication may take longer than the recurrence interval; evidence remains
	// valid for this pass even after the re-arm timestamp has elapsed.
	later := r.now().Add(48 * time.Hour)
	r.Now = func() time.Time { return later }
	if done, err := r.complete(f.sel, "bee-standing"); err != nil || !done {
		t.Fatalf("delayed publication lost completion: done=%v err=%v", done, err)
	}
}

func TestRecordCycleRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		edit func(*testing.T, *cycleFixture)
	}{
		{"failed check", "cycle check FAILED", func(t *testing.T, f *cycleFixture) { t.Setenv("BEEHIVE_CYCLE_TEST_MODE", "fail") }},
		{"check dirties code", "cycle check dirtied code", func(t *testing.T, f *cycleFixture) { t.Setenv("BEEHIVE_CYCLE_TEST_MODE", "dirty") }},
		{"dirty code", "clean code checkout", func(t *testing.T, f *cycleFixture) {
			cycleWrite(t, filepath.Join(f.code.Dir, "measurement.txt"), "dirty\n")
		}},
		{"staged code", "clean code checkout", func(t *testing.T, f *cycleFixture) {
			cycleWrite(t, filepath.Join(f.code.Dir, "measurement.txt"), "staged\n")
			cycleGit(t, f.code, "add", "measurement.txt")
		}},
		{"untracked code", "clean code checkout", func(t *testing.T, f *cycleFixture) {
			cycleWrite(t, filepath.Join(f.code.Dir, "new.txt"), "new\n")
		}},
		{"new code commit", "tracked gitlink", func(t *testing.T, f *cycleFixture) {
			cycleWrite(t, filepath.Join(f.code.Dir, "new.txt"), "new\n")
			if err := f.code.Commit(context.Background(), "new code needs review"); err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong session branch", "owning private hive session branch", func(t *testing.T, f *cycleFixture) { f.runner.Session = "other-session" }},
		{"wrong claim owner", "new owning session", func(t *testing.T, f *cycleFixture) {
			task := f.sel.Task
			task.Session = "other-owner"
			cycleWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
		}},
		{"unmet local dependency", "blocked by dependency prerequisite", func(t *testing.T, f *cycleFixture) {
			task := f.sel.Task
			task.Deps = []string{"prerequisite"}
			cycleWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n\n## prerequisite [TODO]\nNot ready.\n")
		}},
		{"unmet cross dependency", "blocked by dependency other:prerequisite", func(t *testing.T, f *cycleFixture) {
			task := f.sel.Task
			task.Deps = []string{"other:prerequisite"}
			cycleWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
			cycleWrite(t, filepath.Join(f.runner.Repo.Root, "submodules/other/PLAN.md"), "## prerequisite [TODO]\nNot ready.\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCycleFixture(t)
			tc.edit(t, f)
			before := cycleGit(t, f.runner.Git, "rev-parse", "HEAD")
			planBefore, err := os.ReadFile(f.sel.Submodule.PlanPath())
			if err != nil {
				t.Fatal(err)
			}
			err = f.runner.RecordCycle(context.Background(), "sm", "standing", f.code.Dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal containing %q, got %v", tc.want, err)
			}
			if tc.name == "failed check" && !strings.Contains(err.Error(), "measurement is unhealthy") {
				t.Fatalf("failure did not report actual test output: %v", err)
			}
			if after := cycleGit(t, f.runner.Git, "rev-parse", "HEAD"); after != before {
				t.Fatal("refused cycle created a hive commit")
			}
			planAfter, err := os.ReadFile(f.sel.Submodule.PlanPath())
			if err != nil || string(planAfter) != string(planBefore) {
				t.Fatalf("refused cycle mutated plan: %s, %v", planAfter, err)
			}
			if _, err := os.Stat(filepath.Join(f.runner.Repo.Root, cycleDocRel)); !os.IsNotExist(err) {
				t.Fatalf("refused cycle wrote evidence: %v", err)
			}
		})
	}
}

func TestCycleCompletionRequiresCommittedStateAndDoc(t *testing.T) {
	for _, committed := range []string{"neither", "plan only", "doc only"} {
		t.Run(committed, func(t *testing.T) {
			f := newCycleFixture(t)
			r := f.runner
			task := f.sel.Task
			if err := task.CompleteCycle(r.Session, r.now()); err != nil {
				t.Fatal(err)
			}
			task.CommitsSet = true
			cycleWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
			cycleWrite(t, filepath.Join(r.Repo.Root, cycleDocRel), "<!-- Beehive-Commits: none -->\n"+cycleMarker(&task, f.sha)+"\n")
			if committed != "neither" {
				rel := cyclePlanRel
				if committed == "doc only" {
					rel = cycleDocRel
				}
				if err := r.Git.CommitPaths(context.Background(), "partial cycle", rel); err != nil {
					t.Fatal(err)
				}
			}
			if done, err := r.complete(f.sel, "bee-standing"); err != nil || done {
				t.Fatalf("uncommitted cycle accepted: done=%v err=%v", done, err)
			}
			if hint, err := r.verifyGate(context.Background(), f.sel, f.code.Dir, r.Repo.Root, "bee-standing"); err != nil || !strings.Contains(hint, "committed current-session check evidence") {
				t.Fatalf("gate did not reject partial cycle: %q %v", hint, err)
			}
			if err := r.Git.CommitPaths(context.Background(), "complete cycle artifacts", cyclePlanRel, cycleDocRel); err != nil {
				t.Fatal(err)
			}
			if done, err := r.complete(f.sel, "bee-standing"); err != nil || !done {
				t.Fatalf("committing both artifacts did not satisfy completion: done=%v err=%v", done, err)
			}
		})
	}
}

func TestCycleCompletionRejectsStaleEvidence(t *testing.T) {
	for _, change := range []string{"owner", "check", "code", "indented receipt", "defers"} {
		t.Run(change, func(t *testing.T) {
			f := newCycleFixture(t)
			r := f.runner
			if err := r.RecordCycle(context.Background(), "sm", "standing", f.code.Dir); err != nil {
				t.Fatal(err)
			}
			task := cycleTask(t, cycleGit(t, r.Git, "show", "HEAD:"+cyclePlanRel))
			doc := cycleGit(t, r.Git, "show", "HEAD:"+cycleDocRel)
			switch change {
			case "owner":
				task.CycleSession = "old-session"
			case "check":
				if err := task.ReplaceCheck("go test -count=2 -v cycle_test.go"); err != nil {
					t.Fatal(err)
				}
			case "code":
				doc = strings.ReplaceAll(doc, f.sha, strings.Repeat("0", len(f.sha)))
			case "indented receipt":
				doc = strings.ReplaceAll(doc, "\n<!-- Beehive-Cycle:", "\n    <!-- Beehive-Cycle:")
			case "defers":
				task.Defers++
			}
			cycleWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
			cycleWrite(t, filepath.Join(r.Repo.Root, cycleDocRel), doc+"\n")
			if err := r.Git.CommitPaths(context.Background(), "mismatched cycle evidence", cyclePlanRel, cycleDocRel); err != nil {
				t.Fatal(err)
			}
			if done, err := r.complete(f.sel, "bee-standing"); err != nil || done {
				t.Fatalf("mismatched cycle evidence accepted: done=%v err=%v", done, err)
			}
			if hint, err := r.verifyGate(context.Background(), f.sel, f.code.Dir, r.Repo.Root, "bee-standing"); err != nil || hint == "" {
				t.Fatalf("gate accepted mismatched cycle: %q %v", hint, err)
			}
		})
	}
}

func TestRecordCycleFailurePreservesPreviousCycle(t *testing.T) {
	f := newCycleFixture(t)
	r := f.runner
	if err := r.RecordCycle(context.Background(), "sm", "standing", f.code.Dir); err != nil {
		t.Fatal(err)
	}
	task := cycleTask(t, cycleGit(t, r.Git, "show", "HEAD:"+cyclePlanRel))
	docBefore := cycleGit(t, r.Git, "show", "HEAD:"+cycleDocRel)
	next := task.NotBefore
	r.Now = func() time.Time { return next }
	r.Session = "bee-next-cycle-session"
	cycleGit(t, r.Git, "switch", "-c", r.Session)
	task.Session, task.Heartbeat = r.Session, next
	f.sel.Task = *task
	cycleWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
	if err := r.Git.CommitPaths(context.Background(), "claim next cycle", cyclePlanRel); err != nil {
		t.Fatal(err)
	}
	before := cycleGit(t, r.Git, "rev-parse", "HEAD")
	t.Setenv("BEEHIVE_CYCLE_TEST_MODE", "fail")
	if err := r.RecordCycle(context.Background(), "sm", "standing", f.code.Dir); err == nil || !strings.Contains(err.Error(), "measurement is unhealthy") {
		t.Fatalf("expected actual measurement failure, got %v", err)
	}
	if got := cycleGit(t, r.Git, "rev-parse", "HEAD"); got != before {
		t.Fatal("failed later check committed a cycle")
	}
	if got := cycleGit(t, r.Git, "show", "HEAD:"+cycleDocRel); got != docBefore {
		t.Fatal("failed later check changed previous successful evidence")
	}
	if got := cycleGit(t, r.Git, "status", "--porcelain"); got != "" {
		t.Fatalf("failed later check left uncommitted cycle state: %s", got)
	}
	if done, err := r.complete(f.sel, "bee-standing"); err != nil || done {
		t.Fatalf("previous cycle evidence completed failed next pass: done=%v err=%v", done, err)
	}
}

func TestRunRecurringCyclePublishesRearmedTODO(t *testing.T) {
	f := newCycleFixture(t)
	r := f.runner
	publishes := 0
	r.Publish = func(ctx context.Context) error {
		publishes++
		// main is not checked out in this fixture; advance it only to committed
		// session history, as the real worktree publisher does.
		_, err := r.Git.Run(ctx, "branch", "-f", "main", "HEAD")
		return err
	}
	var cycleErr error
	client := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		if turn != 1 {
			cycleErr = fmt.Errorf("cycle required unexpected turn %d", turn)
			return
		}
		checkout := filepath.Join(f.sel.Submodule.WorktreesDir(), "bee-standing")
		cycleErr = r.RecordCycle(context.Background(), "sm", "standing", checkout)
	}}}
	r.Client = client
	res, err := r.Run(context.Background(), f.sel, "system", "measure this cycle")
	if cycleErr != nil {
		t.Fatalf("agent RecordCycle: %v", cycleErr)
	}
	if err != nil || !res.Completed || res.GCMarked || res.Lost || res.Turns != 1 || res.Warning != "" {
		t.Fatalf("cycle run: result=%+v err=%v", res, err)
	}
	if publishes == 0 {
		t.Fatal("runner reported completion without publishing")
	}
	f.assertRecorded(t, "main")
	if got := cycleGit(t, r.Git, "rev-parse", "main:submodules/sm/repo"); got != f.sha {
		t.Fatal("cycle publication changed tracked code")
	}
	if _, err := os.Stat(filepath.Join(f.sel.Submodule.WorktreesDir(), "bee-standing")); !os.IsNotExist(err) {
		t.Fatalf("completed cycle did not clean up code worktree: %v", err)
	}
}

func TestCycleGateRollbackPreservesHistoryAndAllowsRetry(t *testing.T) {
	for _, mode := range []string{"fabricated receipt with failing measurement", "dirty checkout after committed cycle"} {
		t.Run(mode, func(t *testing.T) {
			f := newCycleFixture(t)
			r := f.runner
			ctx := context.Background()
			if err := r.RecordCycle(ctx, "sm", "standing", f.code.Dir); err != nil {
				t.Fatal(err)
			}
			previousDoc := cycleGit(t, r.Git, "show", "HEAD:"+cycleDocRel)
			task := cycleTask(t, cycleGit(t, r.Git, "show", "HEAD:"+cyclePlanRel))
			next := task.NotBefore
			r.Now = func() time.Time { return next }
			r.Session = "bee-retry-cycle-session"
			cycleGit(t, r.Git, "switch", "-c", r.Session)
			cycleGit(t, f.code, "branch", "bee-standing")
			task.Claim(r.Session, next)
			f.sel.Task = *task
			cycleWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
			if err := r.Git.CommitPaths(ctx, "claim next cycle", cyclePlanRel); err != nil {
				t.Fatal(err)
			}
			original := *task
			wantHint := "UNCOMMITTED changes"
			if mode == "fabricated receipt with failing measurement" {
				// Commit plausible evidence without measuring; only the gate's real
				// check can distinguish this receipt from a legitimate cycle.
				if err := task.CompleteCycle(r.Session, next); err != nil {
					t.Fatal(err)
				}
				cycleWrite(t, f.sel.Submodule.PlanPath(), task.Card()+"\n")
				cycleWrite(t, filepath.Join(r.Repo.Root, cycleDocRel), previousDoc+"\n\n"+cycleMarker(task, f.sha)+"\n")
				if err := r.Git.CommitPaths(ctx, "fabricated cycle", cyclePlanRel, cycleDocRel); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BEEHIVE_CYCLE_TEST_MODE", "fail")
				wantHint = "measurement is unhealthy"
			} else {
				if err := r.RecordCycle(ctx, "sm", "standing", f.code.Dir); err != nil {
					t.Fatal(err)
				}
				cycleWrite(t, filepath.Join(f.code.Dir, "measurement.txt"), "dirty\n")
			}
			if done, err := r.complete(f.sel, "bee-standing"); err != nil || !done {
				t.Fatalf("fixture did not reach committed-cycle gate: done=%v err=%v", done, err)
			}
			if hint, err := r.verifyGate(ctx, f.sel, f.code.Dir, r.Repo.Root, "bee-standing"); err != nil || !strings.Contains(hint, wantHint) {
				t.Fatalf("gate must reject cycle for %q: hint=%q err=%v", wantHint, hint, err)
			}
			rejectedHead := cycleGit(t, r.Git, "rev-parse", "HEAD")
			if err := r.revertGatedFlip(ctx, f.sel, r.Repo.Root, plan.TODO); err != nil {
				t.Fatal(err)
			}
			restored := cycleTask(t, cycleGit(t, r.Git, "show", "HEAD:"+cyclePlanRel))
			if restored.Card() != original.Card() {
				t.Fatalf("rollback must restore history, not_before, defers and claim:\nwant %s\ngot %s", original.Card(), restored.Card())
			}
			if got := cycleGit(t, r.Git, "rev-parse", "HEAD^"); got != rejectedHead {
				t.Fatal("rollback must commit a repair, not reset history")
			}
			if doc := cycleGit(t, r.Git, "show", "HEAD:"+cycleDocRel); !strings.HasPrefix(doc, previousDoc) {
				t.Fatal("rollback discarded previous successful evidence")
			}
			if done, err := r.complete(f.sel, "bee-standing"); done || err != nil {
				t.Fatalf("rejected cycle still complete: done=%v err=%v", done, err)
			}
			// Repair the measurement/checkout and retry in the restored claim.
			t.Setenv("BEEHIVE_CYCLE_TEST_MODE", "")
			cycleWrite(t, filepath.Join(f.code.Dir, "measurement.txt"), "healthy\n")
			next = next.Add(time.Minute)
			if err := r.RecordCycle(ctx, "sm", "standing", f.code.Dir); err != nil {
				t.Fatalf("retry after rollback: %v", err)
			}
			f.assertRecorded(t, "HEAD")
			if hint, err := r.verifyGate(ctx, f.sel, f.code.Dir, r.Repo.Root, "bee-standing"); err != nil || hint != "" {
				t.Fatalf("repaired retry failed gate: %q %v", hint, err)
			}
			if done, err := r.complete(f.sel, "bee-standing"); err != nil || !done {
				t.Fatalf("retry not complete: done=%v err=%v", done, err)
			}
			if doc := cycleGit(t, r.Git, "show", "HEAD:"+cycleDocRel); !strings.HasPrefix(doc, previousDoc) {
				t.Fatal("retry discarded previous successful evidence")
			}
		})
	}
}

func TestRecordCycleToleratesHeartbeatDuringCheck(t *testing.T) {
	f := newCycleFixture(t)
	r := f.runner
	ctx := context.Background()
	beat := r.now().Add(time.Minute)
	c := &claim.Claimer{Repo: r.Repo, Sub: f.sel.Submodule, Git: r.Git, Session: r.Session, TTL: r.TTL, Now: func() time.Time { return beat }}
	checks := 0
	r.RunVerify = func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
		if name == "sh" && len(args) == 2 && args[0] == "-c" && args[1] == f.sel.Task.Check() {
			checks++
			if dir != f.code.Dir {
				t.Fatalf("measurement ran in %s, want %s", dir, f.code.Dir)
			}
			if err := c.Heartbeat(ctx, "standing", plan.TODO, beat); err != nil {
				t.Fatalf("heartbeat during runCheck: %v", err)
			}
			want := f.sel.Task
			want.Heartbeat = beat
			got := cycleTask(t, cycleGit(t, r.Git, "show", "HEAD:"+cyclePlanRel))
			if got.Card() != want.Card() {
				t.Fatalf("heartbeat changed more than heartbeat:\nwant %s\ngot %s", want.Card(), got.Card())
			}
		}
		return realRunVerify(ctx, dir, name, args...)
	}
	if err := r.RecordCycle(ctx, "sm", "standing", f.code.Dir); err != nil {
		t.Fatalf("heartbeat-only change must not invalidate measurement: %v", err)
	}
	if checks != 1 {
		t.Fatalf("heartbeat seam ran %d times, want one actual check", checks)
	}
	f.assertRecorded(t, "HEAD")
}

func TestCycleMainAdvancedRequiresPublishedEvidence(t *testing.T) {
	f := newCycleFixture(t)
	r := f.runner
	ctx := context.Background()
	if err := r.RecordCycle(ctx, "sm", "standing", f.code.Dir); err != nil {
		t.Fatal(err)
	}
	mainBefore := cycleGit(t, r.Git, "rev-parse", "main")
	r.Publish = func(context.Context) error { return nil }
	if err := r.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if advanced, err := r.mainAdvanced(ctx, f.sel); err != nil || advanced {
		t.Fatalf("successful callback without publication accepted: advanced=%v err=%v", advanced, err)
	}
	if got := cycleGit(t, r.Git, "rev-parse", "main"); got != mainBefore {
		t.Fatal("no-op publisher unexpectedly advanced main")
	}
	// Another code change can land before cycle publication. The receipt is
	// historical evidence and must not be compared to main's newer gitlink.
	r.Publish = func(ctx context.Context) error {
		cycleWrite(t, filepath.Join(f.code.Dir, "new.txt"), "independent code change\n")
		if err := f.code.Commit(ctx, "advance tracked code"); err != nil {
			return err
		}
		if err := r.Git.CommitPaths(ctx, "publish advanced gitlink", "submodules/sm/repo"); err != nil {
			return err
		}
		_, err := r.Git.Run(ctx, "branch", "-f", "main", "HEAD")
		return err
	}
	if err := r.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if got := cycleGit(t, r.Git, "rev-parse", "main:submodules/sm/repo"); got == f.sha || got != cycleGit(t, f.code, "rev-parse", "HEAD") {
		t.Fatalf("publication did not advance gitlink: %s", got)
	}
	f.assertRecorded(t, "main")
	if advanced, err := r.mainAdvanced(ctx, f.sel); err != nil || !advanced {
		t.Fatalf("published cycle with historical measured SHA rejected: advanced=%v err=%v", advanced, err)
	}
}

func TestRecurringTaskCannotYieldByOnlyMovingNotBefore(t *testing.T) {
	f := newCycleFixture(t)
	task := f.sel.Task
	task.NotBefore = f.runner.now().Add(24 * time.Hour)
	p, err := plan.Parse(task.Card() + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if task.Defers != 12 || task.Cycles != f.sel.Task.Cycles || task.Defers != f.sel.Task.Defers {
		t.Fatal("fixture must move only not_before with cycles/defers unchanged at defers=12")
	}
	if yielded, err := f.runner.taskYieldedBlocked(f.sel, p, p.Find(task.ID)); yielded || err == nil || !strings.Contains(err.Error(), "not merely move not_before") {
		t.Fatalf("timestamp-only recurring yield must fail: yielded=%v err=%v", yielded, err)
	}
}
