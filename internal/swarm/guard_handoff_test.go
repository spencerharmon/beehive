package swarm

import (
	"bytes"
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

// Real git + real guard execution through Runner.Run: no injected gate verdicts.
// The stale task ref and tracked main diverge after an obsolete guard version.
// Neither published task history nor the commits tag is a safety baseline.
func TestMutationGuardWorkHandoff(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stale          bool
		protected      bool
		tamper         bool
		removeRegistry bool
		push           bool
		declareNone    bool
		noTrackingRef  bool
	}{
		{name: "empty-stale-attempt", stale: true, declareNone: true},
		{name: "empty-stale-no-tracking-ref", stale: true, declareNone: true, noTrackingRef: true},
		{name: "protected-stale-attempt", stale: true, protected: true},
		{name: "tamper-stale-attempt", stale: true, protected: true, tamper: true},
		{name: "protected-commits-none", stale: true, protected: true, declareNone: true},
		{name: "protected-already-pushed", protected: true, push: true},
		{name: "tamper-already-pushed", protected: true, tamper: true, push: true},
		{name: "registry-removal-already-pushed", protected: true, removeRegistry: true, push: true},
		{name: "protected-not-pushed", protected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			seedDir := filepath.Join(t.TempDir(), "seed")
			handoffOrphanGit(t, g, "clone", "--no-local", "-q", origin, seedDir)
			seed := gitConfig(t, seedDir)
			if err := os.MkdirAll(filepath.Join(seedDir, "guards"), 0o755); err != nil {
				t.Fatal(err)
			}
			handoffOrphanWrite(t, filepath.Join(seedDir, "GUARDS.md"), "## active-release\nProtects: helmrelease-*.yaml\nCommand: sh guards/release.sh\n")
			writeRelease := func(dir, version string) {
				t.Helper()
				for _, color := range []string{"blue", "green", "shared"} {
					handoffOrphanWrite(t, filepath.Join(dir, "helmrelease-"+color+".yaml"), "version: "+version+"\n")
				}
			}
			writeRelease(seedDir, "old")
			handoffOrphanWrite(t, filepath.Join(seedDir, "guards", "release.sh"), "#!/bin/sh\nprintf 'obsolete authority: old.example.net\\n'\nexit 1\n")
			if err := seed.Commit(ctx, "old reviewed baseline"); err != nil {
				t.Fatal(err)
			}
			oldBase := handoffOrphanGit(t, seed, "rev-parse", "HEAD")
			var oldSHA string
			if tc.stale {
				handoffOrphanGit(t, seed, "checkout", "-b", "bee-T1")
				writeRelease(seedDir, "abandoned")
				if err := seed.Commit(ctx, "abandoned protected edits"); err != nil {
					t.Fatal(err)
				}
				oldSHA = handoffOrphanGit(t, seed, "rev-parse", "HEAD")
				handoffOrphanGit(t, seed, "push", "origin", "bee-T1")
				handoffOrphanGit(t, seed, "checkout", "main")
			}
			writeRelease(seedDir, "current")
			handoffOrphanWrite(t, filepath.Join(seedDir, "guards", "release.sh"), "#!/bin/sh\nprintf 'trusted authority: release.example.com\\n'\nprintf 'matched: %s\\n' \"$BEEHIVE_GUARD_MATCHED_FILES\"\nexit 1\n")
			if err := seed.Commit(ctx, "current reviewed baseline"); err != nil {
				t.Fatal(err)
			}
			base := handoffOrphanGit(t, seed, "rev-parse", "HEAD")
			baseTree := handoffOrphanGit(t, seed, "rev-parse", "HEAD^{tree}")
			handoffOrphanGit(t, seed, "push", "origin", "main")
			handoffOrphanGit(t, g, "clone", "--no-local", "-q", origin, filepath.Join(sm, "repo"))
			gitConfig(t, filepath.Join(sm, "repo"))
			handoffOrphanWrite(t, filepath.Join(sm, "PLAN.md"), "## T1 [TODO] <!-- attempts=0 deps= check=none -->\nMeasure current state.\n")
			if err := g.Commit(ctx, "seed hive"); err != nil {
				t.Fatal(err)
			}
			rp, err := repo.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			subs, err := rp.Submodules()
			if err != nil || len(subs) != 1 {
				t.Fatalf("submodules: %v %v", subs, err)
			}
			sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
			wt := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
			var prompts []string
			var candidate, remoteBefore string
			worker := &mockSession{all: &prompts, onTurn: func(turn int) {
				if turn != 1 {
					return
				}
				wg := git.New(wt)
				if got := handoffOrphanGit(t, wg, "rev-parse", "HEAD"); got != base {
					t.Fatalf("start = %s, want %s", got, base)
				}
				if tc.protected {
					handoffOrphanWrite(t, filepath.Join(wt, "helmrelease-green.yaml"), "version: forbidden\n")
				}
				if tc.tamper {
					handoffOrphanWrite(t, filepath.Join(wt, "guards", "release.sh"), "#!/bin/sh\nprintf 'tampered allow\\n'\nexit 0\n")
				}
				if tc.removeRegistry {
					handoffOrphanWrite(t, filepath.Join(wt, "GUARDS.md"), "# disabled by candidate\n")
				}
				if tc.protected || tc.tamper || tc.removeRegistry {
					if err := wg.Commit(ctx, "Beehive: T1 submodules/sm/docs/bee-T1-T1.md"); err != nil {
						t.Fatal(err)
					}
				}
				candidate = handoffOrphanGit(t, wg, "rev-parse", "HEAD")
				if tc.push {
					handoffOrphanGit(t, wg, "push", "origin", "bee-T1")
				}
				if tc.stale {
					if out, err := wg.Run(ctx, "push", "origin", "bee-T1"); err == nil || (!strings.Contains(out+err.Error(), "non-fast-forward") && !strings.Contains(out+err.Error(), "fetch first")) {
						t.Fatalf("expected ordinary stale-attempt push rejection: %s %v", out, err)
					}
					// Show exactly why the old baseline attributed unrelated changes.
					handoffOrphanGit(t, wg, "fetch", "origin", "bee-T1")
					if got := handoffOrphanGit(t, wg, "merge-base", "HEAD", "origin/bee-T1"); got != oldBase {
						t.Fatalf("stale baseline = %s, want %s", got, oldBase)
					}
					if tc.noTrackingRef {
						handoffOrphanGit(t, wg, "update-ref", "-d", "refs/remotes/origin/bee-T1")
					}
				}
				if !tc.protected {
					if diff := handoffOrphanGit(t, wg, "diff", base, "HEAD"); diff != "" {
						t.Fatalf("no-code candidate has diff: %s", diff)
					}
				}
				if status := handoffOrphanGit(t, wg, "status", "--porcelain"); status != "" {
					t.Fatalf("dirty candidate: %s", status)
				}
				remoteBefore = handoffOrphanGit(t, wg, "ls-remote", "--heads", "origin")
				sha := candidate
				if tc.declareNone {
					sha = "none"
				}
				handoffOrphanArtifacts(t, g, plan.NeedsReview, sha)
			}}
			var logs bytes.Buffer
			r := &Runner{Repo: rp, Git: g, Client: &mockClient{sess: worker}, MaxTurns: 2, TTL: time.Hour, Concise: &logs}
			res, err := r.Run(ctx, sel, "sys", "implement")
			if err != nil {
				t.Fatalf("run: %v; result=%+v; prompts=%q", err, res, prompts)
			}
			if !strings.Contains(logs.String(), "baseline="+base+" candidate="+candidate+" branch=bee-T1") {
				t.Fatalf("gate did not identify the actual candidate/baseline: %s", logs.String())
			}
			if tc.protected {
				if res.Completed {
					t.Fatalf("protected candidate accepted: %+v", res)
				}
				feedback := strings.Join(prompts[1:], "\n")
				if !strings.Contains(feedback, "trusted authority: release.example.com") || !strings.Contains(feedback, "matched: helmrelease-green.yaml") || strings.Contains(feedback, "helmrelease-blue.yaml") || strings.Contains(feedback, "helmrelease-shared.yaml") || strings.Contains(feedback, "obsolete authority") || strings.Contains(feedback, "tampered allow") {
					t.Fatalf("must refuse with trusted current baseline, not stale/tampered guard: %s", feedback)
				}
				if after := handoffOrphanGit(t, seed, "ls-remote", "--heads", "origin"); after != remoteBefore {
					t.Fatalf("guard refusal published/reconciled source: before=%s after=%s", remoteBefore, after)
				}
			} else {
				if !res.Completed || res.Turns != 1 || res.Warning != "" {
					t.Fatalf("empty delivery refused: %+v; prompts=%q", res, prompts)
				}
				independent := filepath.Join(t.TempDir(), "published")
				handoffOrphanGit(t, g, "clone", "--no-local", "-q", "--branch", "bee-T1", origin, independent)
				published := git.New(independent)
				if got := handoffOrphanGit(t, published, "rev-parse", "HEAD^{tree}"); got != baseTree {
					t.Fatalf("reclaim changed empty delivery tree: %s, want %s", got, baseTree)
				}
				for _, ancestor := range []string{candidate, oldSHA} {
					if ok, err := published.IsAncestor(ctx, ancestor, "HEAD"); err != nil || !ok {
						t.Fatalf("reclaim lost %s: %v %v", ancestor, ok, err)
					}
				}
				p, err := plan.Parse(handoffOrphanGit(t, g, "show", "HEAD:submodules/sm/PLAN.md"))
				if err != nil {
					t.Fatal(err)
				}
				if task := p.Find("T1"); task == nil || task.Status != plan.NeedsReview || !task.CommitsSet || len(task.Commits) != 0 {
					t.Fatalf("lost no-code bookkeeping: %+v", task)
				}
				doc := handoffOrphanGit(t, g, "show", "HEAD:submodules/sm/docs/bee-T1-T1.md")
				if shas, set := plan.ParseDocCommits(doc); !set || len(shas) != 0 {
					t.Fatalf("no-code doc disagrees: %s", doc)
				}
			}
			if got := handoffOrphanGit(t, git.New(origin), "rev-parse", "main"); got != base {
				t.Fatalf("Work changed tracked main: %s", got)
			}
		})
	}
}
