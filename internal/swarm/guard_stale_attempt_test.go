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

// Guard scripts for the stale-attempt fixture. The OBSOLETE guard is the one
// reviewed main carried when a never-landed bee-T1 attempt was cut; it refuses
// every protected edit with a retired-host marker. The CURRENT guard is what
// reviewed main carries now. Identifiers are neutral (RFC2606).
const (
	staleGuardRegistry = "## protected-release\nProtects: deploy/release-*.yaml\nCommand: sh guards/release.sh\n"
	obsoleteGuard      = "#!/bin/sh\nprintf 'OBSOLETE guard probing retired.example.net\\n'\nexit 1\n"
	currentGuard       = "#!/bin/sh\nprintf 'CURRENT guard probing live.example.com refused: %s\\n' \"$BEEHIVE_GUARD_MATCHED_FILES\"\nexit 1\n"
)

// TestGuardStaleAttemptWorkHandoff drives the REAL Work handoff (Runner.Run ->
// verifyGate -> guardGate -> landSourceBranch) against a real-git origin where
// bee-T1 holds a stale, never-landed attempt cut from an OLDER main, and main has
// since advanced (reviewed protected-file changes + an updated guard).
//
//   - empty: the pass delivers no code (HEAD == tracked tip, commits=none). The
//     gate must NOT borrow main's reviewed protected changes as if they were this
//     pass's diff, nor run the obsolete guard from the stale fork point. The
//     runner still owns orphan reclamation (ours-merge, old attempt reachable).
//   - protected: a real protected edit is refused by the CURRENT baseline guard
//     (never the obsolete one) and fails closed.
//   - tamper: a protected edit that also rewrites the guard to `exit 0` is still
//     refused — the gate runs the trusted pre-change baseline guard.
func TestGuardStaleAttemptWorkHandoff(t *testing.T) {
	for _, mode := range []string{"empty", "protected", "tamper"} {
		t.Run(mode, func(t *testing.T) {
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

			// Old reviewed main (M0): obsolete guard + protected release files.
			seed := filepath.Join(t.TempDir(), "seed")
			handoffOrphanGit(t, g, "clone", "--no-local", "-q", origin, seed)
			sg := gitConfig(t, seed)
			writeTree := func(dir string, files map[string]string) {
				t.Helper()
				for rel, body := range files {
					p := filepath.Join(dir, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						t.Fatal(err)
					}
					handoffOrphanWrite(t, p, body)
				}
			}
			writeTree(seed, map[string]string{
				"GUARDS.md":                  staleGuardRegistry,
				"guards/release.sh":          obsoleteGuard,
				"deploy/release-blue.yaml":   "host: retired.example.net\n",
				"deploy/release-green.yaml":  "host: retired.example.net\n",
				"deploy/release-shared.yaml": "host: retired.example.net\n",
			})
			if err := sg.Commit(ctx, "old reviewed main"); err != nil {
				t.Fatal(err)
			}
			handoffOrphanGit(t, sg, "push", "-q", "origin", "HEAD:main")
			m0 := handoffOrphanGit(t, sg, "rev-parse", "HEAD")

			// Stale attempt cut from M0, pushed to bee-T1, never landed.
			writeTree(seed, map[string]string{"stale-only.txt": "abandoned attempt\n"})
			if err := sg.Commit(ctx, "stale attempt"); err != nil {
				t.Fatal(err)
			}
			staleSHA := handoffOrphanGit(t, sg, "rev-parse", "HEAD")
			handoffOrphanGit(t, sg, "push", "-q", "origin", "HEAD:refs/heads/bee-T1")

			// Reviewed main advances (M1): protected files + current guard.
			handoffOrphanGit(t, sg, "reset", "-q", "--hard", m0)
			writeTree(seed, map[string]string{
				"guards/release.sh":          currentGuard,
				"deploy/release-blue.yaml":   "host: live.example.com\n",
				"deploy/release-green.yaml":  "host: live.example.com\n",
				"deploy/release-shared.yaml": "host: live.example.com\n",
			})
			if err := sg.Commit(ctx, "reviewed main advance"); err != nil {
				t.Fatal(err)
			}
			handoffOrphanGit(t, sg, "push", "-q", "origin", "HEAD:main")
			m1 := handoffOrphanGit(t, sg, "rev-parse", "HEAD")

			repoDir := filepath.Join(sm, "repo")
			handoffOrphanGit(t, g, "clone", "--no-local", "-q", origin, repoDir)
			handoffOrphanWrite(t, filepath.Join(sm, "PLAN.md"), "## T1 [TODO] <!-- attempts=0 deps= -->\nDeliver.\n")
			if err := g.Commit(ctx, "seed"); err != nil {
				t.Fatal(err)
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

			var prompts []string
			var candidate string
			worker := &mockSession{all: &prompts, onTurn: func(turn int) {
				if turn != 1 {
					return
				}
				wg := git.New(wtDir)
				if got := handoffOrphanGit(t, wg, "rev-parse", "HEAD"); got != m1 {
					t.Fatalf("Work must start at the tracked tip %s, got %s", m1, got)
				}
				sha := "none"
				if mode != "empty" {
					writeTree(wtDir, map[string]string{"deploy/release-green.yaml": "host: changed.example.com\n"})
					if mode == "tamper" {
						writeTree(wtDir, map[string]string{"guards/release.sh": "#!/bin/sh\nexit 0\n"})
					}
					if err := wg.Commit(ctx, "Beehive: T1 submodules/sm/docs/bee-T1-T1.md"); err != nil {
						t.Fatal(err)
					}
					sha = handoffOrphanGit(t, wg, "rev-parse", "HEAD")
					// The agent's ordinary push is rejected by the stale attempt; it
					// must not force or reconcile (the runner owns reclamation).
					if out, err := wg.Run(ctx, "push", "origin", "bee-T1"); err == nil {
						t.Fatalf("plain push over the stale attempt must be rejected: %s", out)
					}
				}
				candidate = handoffOrphanGit(t, wg, "rev-parse", "HEAD")
				handoffOrphanArtifacts(t, g, plan.NeedsReview, sha)
			}}
			r := &Runner{Repo: rp, Git: g, Client: &mockClient{sess: worker}, MaxTurns: 2, TTL: time.Hour}
			res, runErr := r.Run(ctx, sel, "sys", "implement")
			fixForward := strings.Join(prompts[min(1, len(prompts)):], "\n")
			if strings.Contains(fixForward, "retired.example.net") || strings.Contains(fixForward, "OBSOLETE") {
				t.Fatalf("obsolete guard from the stale attempt's fork point was executed:\n%s", fixForward)
			}
			originG := git.New(origin)

			if mode == "empty" {
				if runErr != nil || !res.Completed || res.Turns != 1 || res.Warning != "" {
					t.Fatalf("empty delivery must hand off in one turn: result=%+v err=%v prompts=%q", res, runErr, prompts)
				}
				if strings.Contains(fixForward, "mutation guard") {
					t.Fatalf("empty delivery borrowed protected mutations:\n%s", fixForward)
				}
				if candidate != m1 {
					t.Fatalf("empty candidate = %s, want tracked tip %s", candidate, m1)
				}
				// Runner-owned reclamation preserved: the stale attempt stays reachable,
				// the published tree is exactly the (empty) candidate's.
				if ok, err := originG.IsAncestor(ctx, staleSHA, "bee-T1"); err != nil || !ok {
					t.Fatalf("orphan reclamation lost stale attempt: ancestor=%v err=%v", ok, err)
				}
				if got, want := handoffOrphanGit(t, originG, "rev-parse", "bee-T1^{tree}"), handoffOrphanGit(t, originG, "rev-parse", m1+"^{tree}"); got != want {
					t.Fatalf("published bee-T1 tree %s, want candidate tree %s", got, want)
				}
				if got := handoffOrphanGit(t, originG, "rev-parse", "main"); got != m1 {
					t.Fatalf("Work advanced main: %s", got)
				}
				committed := handoffOrphanGit(t, g, "show", "HEAD:submodules/sm/PLAN.md")
				if p, err := plan.Parse(committed); err != nil || p.Find("T1") == nil || p.Find("T1").Status != plan.NeedsReview {
					t.Fatalf("committed handoff: %v\n%s", err, committed)
				}
				return
			}
			if res.Completed {
				t.Fatalf("%s: protected change must be refused: result=%+v prompts=%q", mode, res, prompts)
			}
			if !strings.Contains(fixForward, "protected-release") || !strings.Contains(fixForward, "CURRENT guard probing live.example.com") ||
				!strings.Contains(fixForward, "deploy/release-green.yaml") {
				t.Fatalf("%s: want refusal by the CURRENT baseline guard naming the edit, got:\n%s", mode, fixForward)
			}
			for _, f := range []string{"deploy/release-blue.yaml", "deploy/release-shared.yaml"} {
				if strings.Contains(fixForward, f) {
					t.Fatalf("%s: refusal borrowed reviewed main change %s:\n%s", mode, f, fixForward)
				}
			}
			if got := handoffOrphanGit(t, originG, "rev-parse", "bee-T1"); got != staleSHA {
				t.Fatalf("%s: refused handoff published bee-T1: %s", mode, got)
			}
			if got := handoffOrphanGit(t, originG, "rev-parse", "main"); got != m1 {
				t.Fatalf("%s: refused handoff advanced main: %s", mode, got)
			}
		})
	}
}
