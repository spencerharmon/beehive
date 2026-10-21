package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

func handoffOrphanGit(t *testing.T, g *git.Repo, args ...string) string {
	t.Helper()
	out, err := g.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, g.Dir, err, out)
	}
	return strings.TrimSpace(out)
}

func handoffOrphanWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func handoffOrphanArtifacts(t *testing.T, g *git.Repo, status plan.Status, sha string) {
	t.Helper()
	const planRel = "submodules/sm/PLAN.md"
	const docRel = "submodules/sm/docs/bee-T1-T1.md"
	handoffOrphanWrite(t, filepath.Join(g.Dir, planRel), "## T1 ["+string(status)+"] <!-- attempts=0 deps= commits="+sha+" -->\nImplement current attempt.\n")
	handoffOrphanWrite(t, filepath.Join(g.Dir, docRel), "<!-- Beehive-Commits: "+sha+" -->\n\nCurrent attempt, superseding the abandoned implementation.\n")
	if err := g.CommitPaths(context.Background(), "handoff: truthful plan and doc", planRel, docRel); err != nil {
		t.Fatal(err)
	}
}

// Exercise the gate before landSourceBranch, not merely the push helper: a
// truthful commits= tag used to prevent the orphan reconciliation being reached.
func TestHandoffOrphanWorkReviewCycle(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "published-and-reviewed"
		if blocked {
			name = "reconcile-push-rejected"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			g := gitInit(t, root)
			if err := repo.Init(root); err != nil {
				t.Fatal(err)
			}
			sm := filepath.Join(root, "submodules", "sm")
			if err := os.MkdirAll(filepath.Join(sm, "docs"), 0o755); err != nil {
				t.Fatal(err)
			}
			origin := bareOriginSeeded(t, g)
			oldDir := filepath.Join(t.TempDir(), "old-attempt")
			handoffOrphanGit(t, g, "clone", "--no-local", "-q", origin, oldDir)
			old := gitConfig(t, oldDir)
			handoffOrphanWrite(t, filepath.Join(oldDir, "old-only.txt"), "abandoned implementation\n")
			handoffOrphanWrite(t, filepath.Join(oldDir, "f"), "old attempt\n")
			if err := old.Commit(ctx, "old attempt"); err != nil {
				t.Fatal(err)
			}
			oldSHA := handoffOrphanGit(t, old, "rev-parse", "HEAD")
			handoffOrphanGit(t, old, "push", "origin", "HEAD:refs/heads/bee-T1")
			baseSHA := handoffOrphanGit(t, git.New(origin), "rev-parse", "main")
			repoDir := filepath.Join(sm, "repo")
			handoffOrphanGit(t, g, "clone", "--no-local", "-q", origin, repoDir)
			rg := gitConfig(t, repoDir)
			handoffOrphanWrite(t, filepath.Join(sm, "PLAN.md"), "## T1 [TODO] <!-- attempts=0 deps= -->\nImplement current attempt.\n")
			if err := g.Commit(ctx, "seed"); err != nil {
				t.Fatal(err)
			}
			if blocked {
				blockAllPushes(t, origin)
			}
			rp, err := repo.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			subs, err := rp.Submodules()
			if err != nil || len(subs) != 1 {
				t.Fatalf("submodules: %v, %v", subs, err)
			}
			sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
			wtDir := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
			var newSHA, newTree string
			var prompts []string
			worker := &mockSession{all: &prompts, onTurn: func(turn int) {
				if turn != 1 {
					return // Never rescue the runner by pushing on a later turn.
				}
				wg := git.New(wtDir)
				if got := handoffOrphanGit(t, wg, "rev-parse", "HEAD"); got != baseSHA {
					t.Fatalf("fresh Work base = %s, want main %s", got, baseSHA)
				}
				handoffOrphanWrite(t, filepath.Join(wtDir, "new-only.txt"), "current implementation\n")
				handoffOrphanWrite(t, filepath.Join(wtDir, "f"), "new attempt\n")
				if err := wg.Commit(ctx, "Beehive: T1 submodules/sm/docs/bee-T1-T1.md"); err != nil {
					t.Fatal(err)
				}
				newSHA = handoffOrphanGit(t, wg, "rev-parse", "HEAD")
				newTree = handoffOrphanGit(t, wg, "rev-parse", "HEAD^{tree}")
				if ok, err := wg.IsAncestor(ctx, oldSHA, newSHA); err != nil || ok {
					t.Fatalf("old attempt must diverge from new: ancestor=%v err=%v", ok, err)
				}
				if ok, err := wg.IsAncestor(ctx, newSHA, oldSHA); err != nil || ok {
					t.Fatalf("new attempt must diverge from old: ancestor=%v err=%v", ok, err)
				}
				out, err := wg.Run(ctx, "push", "origin", "bee-T1")
				if err == nil || (!strings.Contains(out+err.Error(), "non-fast-forward") && !strings.Contains(out+err.Error(), "fetch first")) {
					t.Fatalf("plain push must reject divergent old attempt: %v\n%s", err, out)
				}
				handoffOrphanArtifacts(t, g, plan.NeedsReview, newSHA)
			}}
			r := &Runner{Repo: rp, Git: g, Client: &mockClient{sess: worker}, MaxTurns: 2, TTL: time.Hour}
			res, runErr := r.Run(ctx, sel, "sys", "implement")
			if len(prompts) > 0 {
				prompts = prompts[1:] // Only fix-forward prompts are relevant on failure.
			}
			if blocked {
				if res.Completed {
					t.Fatalf("rejected reconciliation must fail closed: %+v", res)
				}
				// A local synthetic merge proves reconciliation was attempted, while
				// the independently advertised origin ref must still be the old tip.
				localTip := handoffOrphanGit(t, rg, "rev-parse", "bee-T1")
				if localTip == newSHA {
					t.Fatalf("gate never attempted orphan reconciliation: result=%+v err=%v prompts=%q", res, runErr, prompts)
				}
				for _, sha := range []string{oldSHA, newSHA} {
					if ok, err := rg.IsAncestor(ctx, sha, localTip); err != nil || !ok {
						t.Fatalf("failed-push local merge lost %s: ancestor=%v err=%v", sha, ok, err)
					}
				}
				if got := handoffOrphanGit(t, git.New(origin), "rev-parse", "bee-T1"); got != oldSHA {
					t.Fatalf("rejected push advanced origin: %s, want %s", got, oldSHA)
				}
				if got := handoffOrphanGit(t, git.New(origin), "rev-parse", "main"); got != baseSHA {
					t.Fatalf("failed handoff advanced main: %s", got)
				}
				// Once the receive-side obstruction clears, the runner's preserved
				// reconciliation makes an ordinary (agent-permitted) push sufficient.
				if err := os.Remove(filepath.Join(origin, "hooks", "pre-receive")); err != nil {
					t.Fatal(err)
				}
				handoffOrphanGit(t, rg, "push", "origin", "bee-T1")
				for _, sha := range []string{oldSHA, newSHA} {
					if ok, err := rg.RemoteContainsCommit(ctx, "origin", "bee-T1", sha); err != nil || !ok {
						t.Fatalf("plain retry lost %s: durable=%v err=%v", sha, ok, err)
					}
				}
				return
			}
			if runErr != nil || !res.Completed || res.GCMarked || res.Turns != 1 || worker.prompts != 1 || res.Warning != "" {
				t.Fatalf("orphan handoff must complete in one turn: result=%+v err=%v prompts=%q", res, runErr, prompts)
			}

			// --no-local avoids shared objects/hardlinks: these assertions must hold
			// using only history another host could fetch, after Work cleanup.
			assertPublished := func(branch string) *git.Repo {
				t.Helper()
				dir := filepath.Join(t.TempDir(), "independent")
				handoffOrphanGit(t, g, "clone", "--no-local", "-q", "--branch", branch, origin, dir)
				cg := git.New(dir)
				for _, sha := range []string{oldSHA, newSHA} {
					if ok, err := cg.IsAncestor(ctx, sha, "HEAD"); err != nil || !ok {
						t.Fatalf("published %s lost history %s: ancestor=%v err=%v", branch, sha, ok, err)
					}
				}
				if tree := handoffOrphanGit(t, cg, "rev-parse", "HEAD^{tree}"); tree != newTree {
					t.Fatalf("published %s tree = %s, want current tree %s", branch, tree, newTree)
				}
				if _, err := os.Stat(filepath.Join(dir, "old-only.txt")); !os.IsNotExist(err) {
					t.Fatalf("published %s resurrected old-only file: %v", branch, err)
				}
				if got := handoffOrphanGit(t, cg, "show", "HEAD:new-only.txt"); got != "current implementation" {
					t.Fatalf("published %s lost new file: %q", branch, got)
				}
				return cg
			}
			assertPublished("bee-T1")
			if got := handoffOrphanGit(t, git.New(origin), "rev-parse", "main"); got != baseSHA {
				t.Fatalf("Work must not merge unreviewed code into main: %s", got)
			}
			committedPlan := handoffOrphanGit(t, g, "show", "HEAD:submodules/sm/PLAN.md")
			p, err := plan.Parse(committedPlan)
			if err != nil || p.Find("T1") == nil {
				t.Fatalf("committed handoff plan: %v\n%s", err, committedPlan)
			}
			task := p.Find("T1")
			if task.Status != plan.NeedsReview || !plan.SameCommitSet(task.Commits, []string{newSHA}) {
				t.Fatalf("Work must preserve truthful authored commits: %+v", task)
			}
			doc := handoffOrphanGit(t, g, "show", "HEAD:submodules/sm/docs/bee-T1-T1.md")
			if shas, set := plan.ParseDocCommits(doc); !set || !plan.SameCommitSet(shas, task.Commits) {
				t.Fatalf("committed Work doc disagrees: %s", doc)
			}

			reviewer := &mockSession{onTurn: func(turn int) {
				if turn != 1 {
					return
				}
				reviewDir := filepath.Join(subs[0].WorktreesDir(), "bee-rev-T1")
				if got := handoffOrphanGit(t, git.New(reviewDir), "rev-parse", "HEAD"); got != baseSHA {
					t.Fatalf("review must inspect from tracked main: %s", got)
				}
				handoffOrphanArtifacts(t, g, plan.Done, newSHA)
			}}
			review := &Runner{Repo: rp, Git: g, Client: &mockClient{sess: reviewer}, MaxTurns: 2, TTL: time.Hour}
			reviewSel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: *task}
			reviewRes, err := review.Run(ctx, reviewSel, "sys", "review")
			if err != nil || !reviewRes.Completed || reviewRes.GCMarked || reviewRes.Turns != 1 || reviewer.prompts != 1 || reviewRes.Warning != "" {
				t.Fatalf("actual Review must publish in one turn without Work-only landing: result=%+v err=%v prompts=%d", reviewRes, err, reviewer.prompts)
			}
			published := assertPublished("main")
			committedPlan = handoffOrphanGit(t, g, "show", "HEAD:submodules/sm/PLAN.md")
			p, err = plan.Parse(committedPlan)
			if err != nil || p.Find("T1") == nil || p.Find("T1").Status != plan.Done {
				t.Fatalf("Review must commit DONE: %v\n%s", err, committedPlan)
			}
			task = p.Find("T1")
			doc = handoffOrphanGit(t, g, "show", "HEAD:submodules/sm/docs/bee-T1-T1.md")
			if shas, set := plan.ParseDocCommits(doc); !set || !task.CommitsSet || len(task.Commits) == 0 || !plan.SameCommitSet(shas, task.Commits) {
				t.Fatalf("Review must commit matching nonempty plan/doc SHAs: task=%+v doc=%s", task, doc)
			}
			for _, sha := range task.Commits {
				if ok, err := published.IsAncestor(ctx, sha, "HEAD"); err != nil || !ok {
					t.Fatalf("Review recorded non-durable SHA %s: ancestor=%v err=%v", sha, ok, err)
				}
			}
		})
	}
}

func TestHandoffOrphanDurabilityDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"missing-object", "does NOT exist"},
		{"missing-object-existing-branch", "does NOT exist"},
		{"missing-remote-branch", "unpushed"},
		{"remote-ancestor", "unpushed"},
		{"wrong-remote-branch", "wrong remote branch"},
		{"pushed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, sel, wtDir, root, sha := durabilityFixture(t, true, false)
			wg := git.New(wtDir)
			switch tc.name {
			case "missing-object", "missing-object-existing-branch":
				if tc.name == "missing-object-existing-branch" {
					handoffOrphanGit(t, wg, "push", "origin", "bee-T1")
				}
				sha = strings.Repeat("0", 40)
				if wg.CommitExists(ctx, sha) {
					t.Fatal("missing-object fixture unexpectedly exists")
				}
			case "remote-ancestor":
				handoffOrphanGit(t, wg, "push", "origin", "HEAD^:refs/heads/bee-T1")
			case "wrong-remote-branch":
				handoffOrphanGit(t, wg, "push", "origin", "HEAD:refs/heads/bee-other")
			case "pushed":
				handoffOrphanGit(t, wg, "push", "origin", "bee-T1")
			}
			// Replace the fixture's read seam with real committed plan/doc artifacts.
			handoffOrphanArtifacts(t, r.Git, plan.NeedsReview, sha)
			r.RunVerify = nil
			before := handoffOrphanGit(t, wg, "ls-remote", "--heads", "origin")
			hint, err := r.verifyGate(ctx, sel, wtDir, root, "bee-T1")
			if err != nil {
				t.Fatalf("diagnostic must be a fix-forward hint, not an infrastructure error: %v", err)
			}
			if tc.want == "" {
				if hint != "" {
					t.Fatalf("pushed control refused: %s", hint)
				}
			} else {
				if !strings.Contains(hint, tc.want) || !strings.Contains(hint, sha) {
					t.Errorf("want %q diagnostic naming %s, got %q", tc.want, sha, hint)
				}
				if !strings.HasPrefix(tc.name, "missing-object") && strings.Contains(hint, "does NOT exist") {
					t.Errorf("present commit must not be reported missing: %s", hint)
				}
			}
			if after := handoffOrphanGit(t, wg, "ls-remote", "--heads", "origin"); after != before {
				t.Fatalf("ordinary durability check must not publish unpushed work:\nbefore %s\nafter %s", before, after)
			}
		})
	}
}

func TestHandoffOrphanPublicationGate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"guard-refused", "protected-file", false},
		{"missing-plan", "reading published orphan claim", true},
		{"corrupt-plan", "parsing published orphan claim", true},
		{"missing-task", "fresh published claim", false},
		{"missing-claim", "fresh published claim", false},
		{"other-session", "fresh published claim", false},
		{"expired", "fresh published claim", false},
		{"current-session", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, sel, wtDir, root, _ := durabilityFixture(t, true, false)
			wg := git.New(wtDir)
			origin := handoffOrphanGit(t, wg, "remote", "get-url", "origin")
			if tc.name == "guard-refused" {
				handoffOrphanWrite(t, filepath.Join(wtDir, "GUARDS.md"), "## protected-file\nProtects: f\nCommand: sh guards/refuse.sh\n")
				if err := os.MkdirAll(filepath.Join(wtDir, "guards"), 0o755); err != nil {
					t.Fatal(err)
				}
				handoffOrphanWrite(t, filepath.Join(wtDir, "guards", "refuse.sh"), "#!/bin/sh\nprintf 'protected edit refused\\n'\nexit 1\n")
				if err := wg.Commit(ctx, "baseline mutation guard"); err != nil {
					t.Fatal(err)
				}
			}
			oldDir := filepath.Join(t.TempDir(), "old-attempt")
			handoffOrphanGit(t, wg, "clone", "--no-local", "-q", "--branch", "bee-T1", wtDir, oldDir)
			old := gitConfig(t, oldDir)
			handoffOrphanGit(t, old, "remote", "set-url", "origin", origin)
			handoffOrphanWrite(t, filepath.Join(oldDir, "f"), "old attempt\n")
			if err := old.Commit(ctx, "abandoned attempt"); err != nil {
				t.Fatal(err)
			}
			oldSHA := handoffOrphanGit(t, old, "rev-parse", "HEAD")
			handoffOrphanGit(t, old, "push", "origin", "bee-T1")
			handoffOrphanGit(t, wg, "fetch", "origin", "bee-T1")
			handoffOrphanWrite(t, filepath.Join(wtDir, "f"), "current attempt\n")
			if err := wg.Commit(ctx, "current attempt"); err != nil {
				t.Fatal(err)
			}
			sha := handoffOrphanGit(t, wg, "rev-parse", "HEAD")
			tree := handoffOrphanGit(t, wg, "rev-parse", "HEAD^{tree}")
			for _, pair := range [][2]string{{oldSHA, sha}, {sha, oldSHA}} {
				if ok, err := wg.IsAncestor(ctx, pair[0], pair[1]); err != nil || ok {
					t.Fatalf("fixture must diverge: ancestor=%v err=%v", ok, err)
				}
			}

			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			r.Now = func() time.Time { return now }
			r.Session = "current-session"
			r.Publish = func(context.Context) error {
				t.Fatal("gate must not publish the hive")
				return nil
			}
			claim := "session=" + r.Session + " heartbeat=" + now.Format(time.RFC3339)
			switch tc.name {
			case "missing-claim":
				claim = ""
			case "other-session":
				claim = "session=other-session heartbeat=" + now.Format(time.RFC3339)
			case "expired":
				claim = "session=" + r.Session + " heartbeat=" + now.Add(-2*r.TTL).Format(time.RFC3339)
			case "corrupt-plan":
				claim = "session=" + r.Session + " heartbeat=invalid"
			}
			planRel := "submodules/sm/PLAN.md"
			body := "## T1 [TODO] <!-- attempts=0 deps= " + claim + " -->\nPublished task.\n"
			if tc.name == "missing-task" {
				body = strings.ReplaceAll(body, "T1", "T2")
			}
			if tc.name == "missing-plan" {
				handoffOrphanGit(t, r.Git, "rm", planRel)
			} else {
				handoffOrphanWrite(t, filepath.Join(root, planRel), body)
			}
			if err := r.Git.Commit(ctx, "published ownership"); err != nil {
				t.Fatal(err)
			}
			hiveOrigin := filepath.Join(t.TempDir(), "hive.git")
			handoffOrphanGit(t, r.Git, "clone", "--bare", "--no-local", "-q", root, hiveOrigin)
			handoffOrphanGit(t, r.Git, "remote", "add", "origin", hiveOrigin)
			r.Remote = "origin"
			handoffOrphanGit(t, r.Git, "checkout", "-b", "handoff")
			handoffOrphanArtifacts(t, r.Git, plan.NeedsReview, sha)
			r.RunVerify = nil

			localBefore := handoffOrphanGit(t, wg, "show-ref", "--heads")
			remoteBefore := handoffOrphanGit(t, wg, "ls-remote", "--heads", "origin")
			hint, err := r.verifyGate(ctx, sel, wtDir, root, "bee-T1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("hint=%q err=%v, want error=%v", hint, err, tc.wantErr)
			}
			if tc.want != "" {
				diagnostic := hint
				if err != nil {
					diagnostic = err.Error()
				}
				if !strings.Contains(diagnostic, tc.want) {
					t.Errorf("want %q, got %q", tc.want, diagnostic)
				}
				if after := handoffOrphanGit(t, wg, "show-ref", "--heads"); after != localBefore {
					t.Errorf("refusal advanced local refs:\nbefore %s\nafter %s", localBefore, after)
				}
				if after := handoffOrphanGit(t, wg, "ls-remote", "--heads", "origin"); after != remoteBefore {
					t.Errorf("refusal advanced remote refs:\nbefore %s\nafter %s", remoteBefore, after)
				}
				return
			}
			if hint != "" {
				t.Fatalf("fresh current owner refused: %s", hint)
			}
			published := git.New(origin)
			for _, ancestor := range []string{oldSHA, sha} {
				if ok, err := published.IsAncestor(ctx, ancestor, "bee-T1"); err != nil || !ok {
					t.Errorf("published history lost %s: ancestor=%v err=%v", ancestor, ok, err)
				}
			}
			if got := handoffOrphanGit(t, published, "rev-parse", "bee-T1^{tree}"); got != tree {
				t.Errorf("published tree=%s, want current tree %s", got, tree)
			}
		})
	}
}

func TestHandoffOrphanLandingIsWorkOnly(t *testing.T) {
	for _, kind := range []selectt.Kind{selectt.Review, selectt.Arbitrate, selectt.Bootstrap, selectt.Reconcile} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			r, sel, wtDir, _, _ := durabilityFixture(t, true, false)
			sel.Kind = kind
			wg := git.New(wtDir)
			beforeRefs := handoffOrphanGit(t, wg, "ls-remote", "--heads", "origin")
			beforePlan := handoffOrphanGit(t, r.Git, "show", "HEAD:submodules/sm/PLAN.md")
			warning, demoted, err := r.landSourceBranch(ctx, sel, wg, "bee-T1")
			if err != nil || warning != "" || demoted {
				t.Fatalf("%s source landing must no-op even with a non-nil git handle: warning=%q demoted=%v err=%v", kind, warning, demoted, err)
			}
			if after := handoffOrphanGit(t, wg, "ls-remote", "--heads", "origin"); after != beforeRefs {
				t.Errorf("%s source landing published a Work branch:\nbefore %s\nafter %s", kind, beforeRefs, after)
			}
			if after := handoffOrphanGit(t, r.Git, "show", "HEAD:submodules/sm/PLAN.md"); after != beforePlan {
				t.Errorf("%s source landing changed the plan:\n%s", kind, after)
			}
		})
	}
}
