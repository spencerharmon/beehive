package swarm

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// TestWorkBriefInjected proves that with LeanBrief on, a Work dispatch injects a
// precomputed task brief carrying the resolved worktree path, branch, submodule
// pointer + tracked tip, the mandated (provided, not derived) change-doc path and
// commit stamp, and head excerpts of the task's OWN
// files — and that a file NOT in the card's Files: line is not pulled in (scoped
// to the task's working set, not the whole tree).
func TestWorkBriefInjected(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	ctx := context.Background()

	// Submodule origin: a base commit, cloned, then a tip commit adding the two
	// task files plus a DECOY file — so there is a real base sha == tracked tip and
	// the synced worktree carries all three files.
	origin := t.TempDir()
	og := gitInit(t, origin)
	os.WriteFile(filepath.Join(origin, "f"), []byte("base"), 0o644)
	if err := og.Commit(ctx, "base"); err != nil {
		t.Fatalf("origin base: %v", err)
	}
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	os.WriteFile(filepath.Join(origin, "alpha.go"), []byte("package a\n\nfunc Alpha() {}\n"), 0o644)
	os.WriteFile(filepath.Join(origin, "beta.go"), []byte("package b\n\nfunc Beta() {}\n"), 0o644)
	os.WriteFile(filepath.Join(origin, "gamma.go"), []byte("package g\n\nfunc GammaSecret() {}\n"), 0o644)
	if err := og.Commit(ctx, "tip"); err != nil {
		t.Fatalf("origin tip: %v", err)
	}
	originTip, err := og.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("origin tip rev: %v", err)
	}

	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	task := plan.Task{ID: "T1", Status: plan.TODO, Body: []string{
		"Do the thing.",
		"Files: alpha.go, beta.go (the two touched files).",
		"Doc: docs/tasks/T1.md",
	}}
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: task}

	var firstPrompt string
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("<!-- Beehive-Commits: none -->\n\ndoc\n"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= commits=none -->\ngo\n"), 0o644)
	}}}
	cl.sess.capture = &firstPrompt
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, TTL: time.Hour, LeanBrief: true}
	if _, err := r.Run(ctx, sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}

	wants := []string{
		"# Task brief (precomputed by the runner",
		"Branch: bee-T1",
		"submodules/sm/worktrees/bee-T1",
		"Submodule pointer (the worktree branched from this commit): " + originTip,
		"Tracked tip (origin/main): " + originTip,
		"REQUIRED change-doc path (write it EXACTLY here): submodules/sm/docs/bee-T1-T1.md",
		"Commit stamp (put this line on your submodule commit): Beehive: T1 submodules/sm/docs/bee-T1-T1.md",
		"## Your task",
		"## T1 [TODO]",             // the card header, canonically rendered
		"Files: alpha.go, beta.go", // the card body (incl the Files: line)
		"### alpha.go (first",      // task-file excerpt heading
		"func Alpha()",             // alpha.go excerpt content
		"### beta.go (first",
		"func Beta()",
	}
	for _, w := range wants {
		if !contains(firstPrompt, w) {
			t.Fatalf("brief missing %q; got:\n%s", w, firstPrompt)
		}
	}
	// Scoped to the task's Files: a file present in the worktree but NOT named in
	// the card is never pulled into the brief.
	if contains(firstPrompt, "gamma.go") || contains(firstPrompt, "GammaSecret") {
		t.Fatalf("brief leaked a non-task file (gamma.go); got:\n%s", firstPrompt)
	}
}

// TestWorkBriefInertByDefault proves the brief ships inert: with LeanBrief off
// (the default) no brief is injected, so the preamble stays byte-identical to the
// historical path.
func TestWorkBriefInertByDefault(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	ctx := context.Background()
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "alpha.go"), []byte("package a\n"), 0o644)
	git.New(repoDir).Commit(ctx, "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	task := plan.Task{ID: "T1", Status: plan.TODO, Body: []string{"Files: alpha.go"}}
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: task}

	var firstPrompt string
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("<!-- Beehive-Commits: none -->\n\ndoc\n"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= commits=none -->\ngo\n"), 0o644)
	}}}
	cl.sess.capture = &firstPrompt
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, TTL: time.Hour} // LeanBrief off
	if _, err := r.Run(ctx, sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if contains(firstPrompt, "# Task brief (precomputed by the runner") {
		t.Fatalf("brief was injected with LeanBrief off; got:\n%s", firstPrompt)
	}
	// The historical Context preamble is still present unchanged.
	if !contains(firstPrompt, "submodules/sm/docs/bee-T1-T1.md") {
		t.Fatalf("default preamble missing doc path; got:\n%s", firstPrompt)
	}
}

func TestFilesFromCard(t *testing.T) {
	cases := []struct {
		name string
		body []string
		want []string
	}{
		{
			name: "annotation and trailing period",
			body: []string{"Files: internal/swarm (brief assembly + injection; reuse worktree), swarm_test.go."},
			want: []string{"internal/swarm", "swarm_test.go"},
		},
		{
			name: "leading descriptor word and glob",
			body: []string{"Files: new internal/artifacts/*, internal/web/web.go, internal/web/env.go."},
			want: []string{"internal/artifacts/*", "internal/web/web.go", "internal/web/env.go"},
		},
		{
			name: "plus-joined paths with parenthetical asides",
			body: []string{"Files: internal/config/hook.go (co-locate installer) + cmd/beehive (init + reinstall), hook_test.go."},
			want: []string{"internal/config/hook.go", "cmd/beehive", "hook_test.go"},
		},
		{
			name: "non-path phrases are dropped",
			body: []string{"Files: internal/web/web.go, cmd/beehive/cmd_submodule.go, shared helper pkg, tests."},
			want: []string{"internal/web/web.go", "cmd/beehive/cmd_submodule.go"},
		},
		{
			name: "dedupe repeats",
			body: []string{"Files: a/b.go, a/b.go."},
			want: []string{"a/b.go"},
		},
		{
			name: "no files line",
			body: []string{"Just a body with no files marker."},
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filesFromCard(c.body)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("filesFromCard(%q) = %#v, want %#v", c.body, got, c.want)
			}
		})
	}
}
