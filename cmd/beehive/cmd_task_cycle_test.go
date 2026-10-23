package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spencerharmon/beehive/internal/swarm"
)

func TestTaskRecurPreservesHistoryAndPublishes(t *testing.T) {
	root, origin := newHive(t)
	const rel = "submodules/mysm/PLAN.md"
	writeFileMW(t, root, rel, "# Plan\n\n"+
		"## watch [TODO] <!-- attempts=2 deps=setup,other:ready weight=8 defers=12 session=owner heartbeat=2026-01-01T00:00:00Z not_before=2026-01-02T00:00:00Z commits=none -->\n"+
		"Measure the running system daily.\nCheck: go test ./...\nFeedback: retain this history.\n")
	commitPush(t, root, "seed standing task")
	before := reparsePlan(t, root, rel).Find("watch")
	peer := seedRemoteAhead(t, origin)
	inDir(t, root, func() {
		c := taskCmd()
		c.SetArgs([]string{"recur", "mysm", "watch", "--every", "24h"})
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	assertHealedAndPushed(t, root, origin, peer, "plan: enable 24h recurrence on watch")
	after := reparsePlan(t, root, rel).Find("watch")
	before.Recurrence = 24 * time.Hour
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("adoption changed more than recurrence:\nbefore: %+v\nafter: %+v", before, after)
	}
	if err := after.Defer(time.Now().Add(time.Hour), time.Now()); err == nil {
		t.Fatal("adoption must not reset the exhausted defer cap")
	}
	// Repeated adoption is idempotent, not a reset of scheduling or counters.
	head := mustGit(t, root, "rev-parse", "HEAD")
	inDir(t, root, func() {
		c := taskCmd()
		c.SetArgs([]string{"recur", "mysm", "watch", "--every", "24h"})
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatal("repeated adoption created a new commit")
	}
}

func TestTaskRecurRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, status, tags, body, every, id, want string
	}{
		{name: "missing period", body: "Check: go test ./...\n", want: "required flag"},
		{name: "malformed period", every: "daily", body: "Check: go test ./...\n", want: "invalid duration"},
		{name: "zero period", every: "0s", body: "Check: go test ./...\n", want: "only 24h"},
		{name: "negative period", every: "-24h", body: "Check: go test ./...\n", want: "only 24h"},
		{name: "unsupported period", every: "12h", body: "Check: go test ./...\n", want: "only 24h"},
		{name: "missing check", every: "24h", want: "requires a Check"},
		{name: "check none", every: "24h", tags: "check=none", want: "requires a Check"},
		{name: "post merge only", every: "24h", body: "Verify-After-Merge: go test ./...\n", want: "requires a Check"},
		{name: "done", every: "24h", status: "DONE", body: "Check: go test ./...\n", want: "only a TODO"},
		{name: "review", every: "24h", status: "NEEDS-REVIEW", body: "Check: go test ./...\n", want: "only a TODO"},
		{name: "arbitration", every: "24h", status: "NEEDS-ARBITRATION", body: "Check: go test ./...\n", want: "only a TODO"},
		{name: "human", every: "24h", status: "NEEDS-HUMAN", body: "Check: go test ./...\n", want: "only a TODO"},
		{name: "missing task", every: "24h", id: "absent", body: "Check: go test ./...\n", want: "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, origin := newHive(t)
			status := tc.status
			if status == "" {
				status = "TODO"
			}
			writeFileMW(t, root, "submodules/mysm/PLAN.md", fmt.Sprintf("# Plan\n\n## watch [%s] <!-- attempts=0 deps= %s -->\nObserve.\n%s", status, tc.tags, tc.body))
			commitPush(t, root, "seed task")
			head := mustGit(t, root, "rev-parse", "HEAD")
			id := tc.id
			if id == "" {
				id = "watch"
			}
			args := []string{"recur", "mysm", id}
			if tc.every != "" {
				args = append(args, "--every", tc.every)
			}
			inDir(t, root, func() {
				c := taskCmd()
				c.SetArgs(args)
				if err := c.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
			})
			if got := mustGit(t, root, "status", "--porcelain"); got != "" {
				t.Fatalf("rejected adoption dirtied hive: %s", got)
			}
			if mustGit(t, root, "rev-parse", "HEAD") != head || mustGit(t, origin, "rev-parse", "main") != head {
				t.Fatal("rejected adoption changed history")
			}
		})
	}
}

func TestTaskSetCheckNoneRejectsRecurringTask(t *testing.T) {
	root, origin := newHive(t)
	const rel = "submodules/mysm/PLAN.md"
	const content = "# Plan\n\n" +
		"## watch [TODO] <!-- attempts=2 deps=setup,other:ready weight=8 defers=12 session=owner heartbeat=2026-01-02T00:00:00Z not_before=2026-01-03T00:00:00Z recurrence=24h cycles=4 last_cycle=2026-01-01T00:00:00Z cycle_session=previous commits=none -->\n" +
		"Measure the running system daily.\nCheck: go test ./...\nFeedback: retain this history.\n"
	writeFileMW(t, root, rel, content)
	commitPush(t, root, "seed recurring task history")
	before := reparsePlan(t, root, rel)
	head := mustGit(t, root, "rev-parse", "HEAD")
	inDir(t, root, func() {
		c := taskCmd()
		c.SetArgs([]string{"set-check", "mysm", "watch", "--check-none"})
		if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "requires a Check") {
			t.Fatalf("want recurring check refusal, got %v", err)
		}
	})
	after, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != content {
		t.Fatalf("rejected check removal changed PLAN.md:\n%s", after)
	}
	if !reflect.DeepEqual(before, reparsePlan(t, root, rel)) {
		t.Fatal("rejected check removal changed task history")
	}
	if got := mustGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("rejected check removal dirtied hive: %s", got)
	}
	if mustGit(t, root, "rev-parse", "HEAD") != head || mustGit(t, origin, "rev-parse", "main") != head {
		t.Fatal("rejected check removal changed git history")
	}
}

func TestTaskCycleGuards(t *testing.T) {
	for _, tc := range []struct {
		name, branch, claim, id, want string
		flags                         []string
	}{
		{name: "primary", branch: "main", claim: "main", want: "non-primary"},
		{name: "detached", branch: "HEAD", claim: "owner", want: "non-primary"},
		{name: "unclaimed", branch: "owner", want: "matching task claim"},
		{name: "different owner", branch: "owner", claim: "peer", want: "matching task claim"},
		{name: "missing task", branch: "owner", claim: "owner", id: "absent", want: "not found"},
		{name: "no caller timestamp", branch: "owner", claim: "owner", flags: []string{"--until", "24h"}, want: "unknown flag"},
		{name: "no fake result", branch: "owner", claim: "owner", flags: []string{"--passed"}, want: "unknown flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, origin := newHive(t)
			claim := ""
			if tc.claim != "" {
				claim = " session=" + tc.claim + " heartbeat=2026-01-01T00:00:00Z"
			}
			writeFileMW(t, root, "submodules/mysm/PLAN.md", "# Plan\n\n## watch [TODO] <!-- attempts=0 deps= recurrence=24h"+claim+" -->\nObserve.\nCheck: go test ./...\n")
			commitPush(t, root, "seed standing task")
			if tc.branch == "HEAD" {
				mustGit(t, root, "checkout", "--detach")
			} else if tc.branch != "main" {
				mustGit(t, root, "checkout", "-b", tc.branch)
			}
			head := mustGit(t, root, "rev-parse", "HEAD")
			t.Setenv("BEEHIVE_WORKTREE", root)
			t.Setenv("SUBMODULE_WORKTREE", t.TempDir())
			id := tc.id
			if id == "" {
				id = "watch"
			}
			// Deliberately run outside the hive: the explicit worktree must win.
			inDir(t, t.TempDir(), func() {
				c := taskCmd()
				c.SetArgs(append([]string{"cycle", "mysm", id}, tc.flags...))
				if err := c.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
			})
			if got := mustGit(t, root, "status", "--porcelain"); got != "" {
				t.Fatalf("rejected cycle dirtied hive: %s", got)
			}
			if mustGit(t, root, "rev-parse", "HEAD") != head || mustGit(t, origin, "rev-parse", "main") != head {
				t.Fatal("rejected cycle changed history")
			}
			if task := reparsePlan(t, root, "submodules/mysm/PLAN.md").Find("watch"); task.Status != plan.StatusTODO || task.Cycles != 0 {
				t.Fatalf("rejected cycle recorded success: %+v", task)
			}
		})
	}
}

type taskCycleClient struct{ session taskCycleSession }

func (c taskCycleClient) Open(context.Context, string, string) (swarm.Session, error) {
	return c.session, nil
}

type taskCycleSession func(context.Context) (string, error)

func (s taskCycleSession) Prompt(ctx context.Context, _ string) (string, error) { return s(ctx) }
func (taskCycleSession) Messages(context.Context) ([]swarm.Message, error)      { return nil, nil }
func (taskCycleSession) Close() error                                           { return nil }

func TestTaskCycleRunnerPublishesRearmedTODO(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mustGit(t, root, "init", "-q", "-b", "main")
	mustGit(t, root, "config", "user.email", "test@example.com")
	mustGit(t, root, "config", "user.name", "test")
	if err := repo.Init(root); err != nil {
		t.Fatal(err)
	}
	sm := repo.Submodule{Name: "sm", Path: filepath.Join(root, "submodules", "sm")}
	writeFileMW(t, sm.RepoDir(), "cycle_test.go", `package cycle
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
}
`)
	writeFileMW(t, sm.RepoDir(), "measurement.txt", "healthy\n")
	mustGit(t, sm.RepoDir(), "init", "-q", "-b", "main")
	mustGit(t, sm.RepoDir(), "config", "user.email", "test@example.com")
	mustGit(t, sm.RepoDir(), "config", "user.name", "test")
	if err := git.New(sm.RepoDir()).Commit(ctx, "seed measurement test"); err != nil {
		t.Fatal(err)
	}
	sha := mustGit(t, sm.RepoDir(), "rev-parse", "HEAD")
	writeFileMW(t, root, ".gitmodules", "[submodule \"sm\"]\n\tpath = submodules/sm/repo\n\turl = ./submodules/sm/repo\n\tbranch = main\n")
	writeFileMW(t, sm.Path, "CHECKS.md", "# Checks\n\n## go-test <!-- category=unit -->\nMatch: (^|&&|;|\\s)go\\s+test\\b\n")
	task := plan.Task{
		ID: "watch", Status: plan.TODO, Recurrence: 24 * time.Hour, Defers: 12,
		Session: "bee-cycle-cli-session", Heartbeat: time.Now().UTC().Truncate(time.Second),
		Body: []string{"Measure the standing task.", "Check: go test -count=1 -v cycle_test.go"},
	}
	const planRel = "submodules/sm/PLAN.md"
	const docRel = "submodules/sm/docs/bee-watch-watch.md"
	writeFileMW(t, root, planRel, task.Card()+"\n")
	g := git.New(root)
	if err := g.Commit(ctx, "seed recurring task and gitlink"); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, root, "ls-tree", "HEAD", "submodules/sm/repo"); !strings.HasPrefix(got, "160000 commit "+sha) {
		t.Fatalf("fixture requires a real gitlink: %s", got)
	}
	mustGit(t, root, "switch", "-c", task.Session)
	configDir := t.TempDir()
	writeFileMW(t, configDir, "config.yaml", "check_sandbox: off\n")
	t.Setenv("BEEHIVE_CONFIG_DIR", configDir)
	t.Setenv("BEEHIVE_WORKTREE", root)
	checkout := filepath.Join(sm.WorktreesDir(), "bee-watch")
	t.Setenv("SUBMODULE_WORKTREE", checkout)

	publishes, prompts := 0, 0
	var cliErr error
	var output bytes.Buffer
	r := swarm.Runner{
		Repo: &repo.Repo{Root: root}, Git: g, Session: task.Session,
		Now: time.Now, TTL: time.Hour, MaxTurns: 2,
		Publish: func(ctx context.Context) error {
			publishes++
			// main is not checked out; publish only committed session history.
			_, err := g.Run(ctx, "branch", "-f", "main", "HEAD")
			return err
		},
		Client: taskCycleClient{session: func(ctx context.Context) (string, error) {
			prompts++
			if prompts != 1 {
				return "", fmt.Errorf("unexpected cycle prompt %d", prompts)
			}
			if got := mustGit(t, checkout, "branch", "--show-current"); got != "bee-watch" {
				t.Fatalf("runner did not create the task code worktree: %q", got)
			}
			mainBefore := mustGit(t, root, "rev-parse", "main")
			c := taskCmd()
			c.SetArgs([]string{"cycle", "sm", "watch"})
			c.SetOut(&output)
			c.SetErr(&output)
			cliErr = c.ExecuteContext(ctx)
			if got := mustGit(t, root, "rev-parse", "main"); got != mainBefore {
				t.Fatal("CLI published instead of leaving publication to the runner")
			}
			return output.String(), cliErr
		}},
	}
	started := time.Now().UTC().Truncate(time.Second)
	// Neither CLI worktree may be inferred from cwd: both explicit env vars win.
	inDir(t, t.TempDir(), func() {
		res, err := r.Run(ctx, &selectt.Selection{Kind: selectt.Work, Submodule: sm, Task: task}, "system", "measure this cycle")
		if cliErr != nil {
			t.Fatalf("task cycle: %v\n%s", cliErr, output.String())
		}
		if err != nil || !res.Completed || res.GCMarked || res.Lost || res.Turns != 1 || res.Warning != "" {
			t.Fatalf("cycle run: result=%+v err=%v", res, err)
		}
	})
	if prompts != 1 || publishes == 0 || !strings.Contains(output.String(), "successful cycle committed") {
		t.Fatalf("CLI/runner handoff: prompts=%d publishes=%d output=%s", prompts, publishes, output.String())
	}
	p, err := plan.Parse(mustGit(t, root, "show", "main:"+planRel))
	if err != nil {
		t.Fatal(err)
	}
	after := p.Find("watch")
	if after == nil || after.Status != plan.TODO || after.Defers != 12 || after.Cycles != 1 ||
		after.Session != "" || !after.Heartbeat.IsZero() || after.CycleSession != task.Session ||
		after.Recurrence != 24*time.Hour || after.LastCycle.Before(started) || after.LastCycle.After(time.Now()) ||
		!after.NotBefore.Equal(after.LastCycle.Add(24*time.Hour)) || !after.CommitsSet || len(after.Commits) != 0 {
		t.Fatalf("incorrect published cycle state: %+v", after)
	}
	doc := mustGit(t, root, "show", "main:"+docRel)
	for _, want := range []string{
		"<!-- Beehive-Commits: none -->", "## Successful cycle 1",
		"<!-- Beehive-Cycle: 1 " + task.Session + " " + after.LastCycle.Format(time.RFC3339) + " " + sha + " ",
		"Check: `" + task.Check() + "`",
		"Code: `" + sha + "`; exit 0; next run: `" + after.NotBefore.Format(time.RFC3339) + "`",
		"recurrence measurement executed", "--- PASS: TestMeasurement",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("published evidence missing %q:\n%s", want, doc)
		}
	}
	if got := mustGit(t, root, "rev-parse", "main:submodules/sm/repo"); got != sha {
		t.Fatal("cycle publication changed tracked code")
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("completed cycle did not clean up code worktree: %v", err)
	}
}
