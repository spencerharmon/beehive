package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spencerharmon/beehive/internal/plan"
)

// task status is the deterministic honeybee handoff: it flips the status along a
// legal state-machine edge, records the commits= tag, mirrors that set into the
// change doc's Beehive-Commits header, and COMMITS both to the LOCAL hive branch
// WITHOUT publishing to origin (the runner owns the merge to main).
func TestTaskStatusWorkFlipLocalCommit(t *testing.T) {
	root, origin := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## base-job [TODO] <!-- attempts=0 deps= session=s1 heartbeat=2026-07-01T00:00:00Z -->\nland the base job.\n")
	writeFileMW(t, root, "submodules/flux/docs/bee-base-job-base-job.md",
		"# base-job\nDid the work. Test: go test ./... passes.\n")
	commitPush(t, root, "seed flux plan + doc")
	originTipBefore := strings.TrimSpace(mustGit(t, origin, "rev-parse", "main"))

	inDir(t, root, func() {
		c := taskStatusCmd()
		c.SetArgs([]string{"flux", "base-job", "NEEDS-REVIEW", "--commits", "abc1234,def5678"})
		if err := c.Execute(); err != nil {
			t.Fatalf("task status: %v", err)
		}
	})

	// PLAN.md flip + commits tag applied and the claim released.
	b, _ := os.ReadFile(filepath.Join(root, "submodules/flux/PLAN.md"))
	p, _ := plan.Parse(string(b))
	tk := p.Find("base-job")
	if tk == nil || tk.Status != plan.StatusReview {
		t.Fatalf("status not flipped: %+v", tk)
	}
	if !tk.CommitsSet || len(tk.Commits) != 2 || tk.Commits[0] != "abc1234" || tk.Commits[1] != "def5678" {
		t.Fatalf("commits tag wrong: set=%v %v", tk.CommitsSet, tk.Commits)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatalf("claim not released: %+v", tk)
	}

	// Doc's Beehive-Commits header mirrors the same set.
	db, _ := os.ReadFile(filepath.Join(root, "submodules/flux/docs/bee-base-job-base-job.md"))
	shas, ok := plan.ParseDocCommits(string(db))
	if !ok || !plan.SameCommitSet(shas, []string{"abc1234", "def5678"}) {
		t.Fatalf("doc header not mirrored: ok=%v shas=%v\n%s", ok, shas, db)
	}

	// Committed LOCALLY (HEAD carries the flip) but NOT published to origin.
	logs := mustGit(t, root, "log", "--format=%s", "HEAD")
	if !strings.Contains(logs, "plan: base-job TODO -> NEEDS-REVIEW") {
		t.Fatalf("flip not committed locally:\n%s", logs)
	}
	originTipAfter := strings.TrimSpace(mustGit(t, origin, "rev-parse", "main"))
	if originTipAfter != originTipBefore {
		t.Fatalf("task status must NOT publish to origin: tip moved %s -> %s", originTipBefore, originTipAfter)
	}
}

// commits designation is mandatory and an illegal state-machine edge (a work pass
// self-approving TODO -> DONE) is refused deterministically.
func TestTaskStatusGuards(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## t1 [TODO] <!-- attempts=0 deps= -->\ndo it.\n")
	writeFileMW(t, root, "submodules/flux/docs/bee-t1-t1.md", "# t1\nwork.\n")
	commitPush(t, root, "seed")

	cases := []struct {
		name string
		args []string
	}{
		{"missing-commits", []string{"flux", "t1", "NEEDS-REVIEW"}},
		{"both-commits", []string{"flux", "t1", "NEEDS-REVIEW", "--commits", "abc", "--commits-none"}},
		{"illegal-edge-self-approve", []string{"flux", "t1", "DONE", "--commits-none"}},
		{"escalation-rejected", []string{"flux", "t1", "NEEDS-HUMAN", "--commits-none"}},
		{"bogus-status", []string{"flux", "t1", "FROBNICATE", "--commits-none"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inDir(t, root, func() {
				c := taskStatusCmd()
				c.SetArgs(tc.args)
				if err := c.Execute(); err == nil {
					t.Fatalf("%s: expected error, got nil", tc.name)
				}
			})
		})
	}
}

// A missing change doc is refused: the handoff gate requires it committed with a
// matching Beehive-Commits header, so the flip cannot proceed without it.
func TestTaskStatusRequiresDoc(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## t1 [TODO] <!-- attempts=0 deps= -->\ndo it.\n")
	commitPush(t, root, "seed")
	inDir(t, root, func() {
		c := taskStatusCmd()
		c.SetArgs([]string{"flux", "t1", "NEEDS-REVIEW", "--commits-none"})
		if err := c.Execute(); err == nil {
			t.Fatal("flip without a change doc must error")
		}
	})
}

// A Review/Arbitrate APPROVE (a review/arbitration state -> DONE) does NOT require
// a commits designation: the runner performs the submodule merge and stamps the
// real merge sha. The command flips the status and leaves commits= UNSET (the
// runner fills it on completion).
func TestTaskStatusApproveNoCommitsRunnerStamps(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## r1 [NEEDS-REVIEW] <!-- attempts=0 deps= commits=abc1234 -->\nreview it.\n")
	writeFileMW(t, root, "submodules/flux/docs/bee-r1-r1.md",
		"<!-- Beehive-Commits: abc1234 -->\n\n<!-- Beehive-Check: pass — ok -->\nreviewed.\n")
	commitPush(t, root, "seed review")

	inDir(t, root, func() {
		c := taskStatusCmd()
		c.SetArgs([]string{"flux", "r1", "DONE"}) // NO --commits / --commits-none
		if err := c.Execute(); err != nil {
			t.Fatalf("approve without commits must succeed (runner stamps): %v", err)
		}
	})

	b, _ := os.ReadFile(filepath.Join(root, "submodules/flux/PLAN.md"))
	p, _ := plan.Parse(string(b))
	tk := p.Find("r1")
	if tk == nil || tk.Status != plan.StatusDone {
		t.Fatalf("status not flipped to DONE: %+v", tk)
	}
	// The command must NOT overwrite/require a commits designation for an approve —
	// it leaves the existing tag as-is (the runner stamps the real merge sha later).
	if len(tk.Commits) != 1 || tk.Commits[0] != "abc1234" {
		t.Fatalf("approve must leave the commits tag untouched for the runner to stamp, got %v", tk.Commits)
	}
	// The flip is committed locally on the hive branch.
	logs := mustGit(t, root, "log", "--format=%s", "HEAD")
	if !strings.Contains(logs, "plan: r1 NEEDS-REVIEW -> DONE") {
		t.Fatalf("approve flip not committed locally:\n%s", logs)
	}
}

// The commits designation stays MANDATORY for a worker handoff (X -> NEEDS-REVIEW):
// the worker authored code and must name its sha(s) or declare none.
func TestTaskStatusWorkerHandoffStillRequiresCommits(t *testing.T) {
	root, _ := newHive(t)
	writeFileMW(t, root, "submodules/flux/PLAN.md",
		"<!-- Beehive-ROI: deadbeef -->\n# Plan\n\n## w1 [TODO] <!-- attempts=0 deps= -->\ndo it.\n")
	writeFileMW(t, root, "submodules/flux/docs/bee-w1-w1.md", "# w1\ndid it.\n")
	commitPush(t, root, "seed work")
	inDir(t, root, func() {
		c := taskStatusCmd()
		c.SetArgs([]string{"flux", "w1", "NEEDS-REVIEW"}) // missing commits designation
		if err := c.Execute(); err == nil {
			t.Fatal("worker handoff without --commits/--commits-none must error")
		}
	})
}
