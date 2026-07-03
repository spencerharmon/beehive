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

	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// mockClient records prompts and lets the test drive the submodule to terminal.
type mockClient struct {
	sess      *mockSession
	gotSystem *string // when set, records the system prompt Open was given
}

func (m *mockClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	if m.gotSystem != nil {
		*m.gotSystem = system
	}
	if m.sess == nil {
		m.sess = &mockSession{}
	}
	return m.sess, nil
}

type mockSession struct {
	prompts int
	onTurn  func(turn int)
	capture *string   // when set, records the first prompt text
	all     *[]string // when set, records every prompt text in order
}

func (s *mockSession) Prompt(ctx context.Context, text string) (string, error) {
	s.prompts++
	if s.capture != nil && s.prompts == 1 {
		*s.capture = text
	}
	if s.all != nil {
		*s.all = append(*s.all, text)
	}
	if s.onTurn != nil {
		s.onTurn(s.prompts)
	}
	return "", nil
}
func (s *mockSession) Close() error { return nil }

func (s *mockSession) Messages(ctx context.Context) ([]Message, error) { return nil, nil }

func gitInit(t *testing.T, dir string) *git.Repo {
	g := git.New(dir)
	ctx := context.Background()
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		g.Run(ctx, a...)
	}
	return g
}

func TestRunCompletes(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	mc := &mockClient{}
	r := &Runner{Repo: rp, Git: g, Client: mc, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r.Client = cl
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed || res.GCMarked {
		t.Fatalf("res %+v", res)
	}
}

func TestRunGCCap(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.MkdirAll(filepath.Join(sm, "repo"), 0o755)
	gitInit(t, filepath.Join(sm, "repo"))
	os.WriteFile(filepath.Join(sm, "repo", "f"), []byte("x"), 0o644)
	git.New(filepath.Join(sm, "repo")).Commit(context.Background(), "base")
	os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")
	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
	r := &Runner{Repo: rp, Git: g, Client: &mockClient{}, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed || !res.GCMarked || res.Turns != 4 {
		t.Fatalf("want gc cap, got %+v", res)
	}
}

// TestRunGCCapReclaimsWorktree proves the turn/wall cap path RECLAIMS the agent's
// orphaned code worktree (mirroring the DONE path) so stale trees don't pile up,
// while DELIBERATELY leaving the task status and its now-going-stale
// session+heartbeat claim intact — there is no IN-PROGRESS status under the
// unified claim model, so that lingering claim is the only signal stale-claim GC
// uses to reclaim/re-TODO the task. The stub Client never completes the task.
func TestRunGCCapReclaimsWorktree(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	// Seed an actively-claimed task (session + heartbeat) so we can prove the cap
	// path leaves the claim behind rather than releasing it.
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= session=bee-z heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	wtDir := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
	// Record that the code worktree physically existed mid-run, so a later absence
	// is a real reclaim and not merely "never created".
	var existedMidRun bool
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		if fi, err := os.Stat(wtDir); err == nil && fi.IsDir() {
			existedMidRun = true
		}
	}}}
	fixed := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	r := &Runner{
		Repo: rp, Git: g, Client: cl, MaxTurns: 2, WallCap: time.Hour, TTL: time.Hour,
		Session: "bee-z", Now: func() time.Time { return fixed },
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed || !res.GCMarked {
		t.Fatalf("want gc cap (incomplete), got %+v", res)
	}
	if !existedMidRun {
		t.Fatal("worktree was never created during the run; the test cannot prove a reclaim")
	}
	// 1) The orphaned code worktree must be reclaimed at the cap.
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatalf("cap path did not reclaim the worktree %s (stat err=%v)", wtDir, err)
	}
	// 2) status unchanged and 3) the claim (session+heartbeat) still present, so
	// stale-claim GC can later reclaim it — the runner must NOT Release or flip
	// status at the cap.
	b, _ := os.ReadFile(planPath)
	p, perr := plan.Parse(string(b))
	if perr != nil {
		t.Fatalf("plan parse: %v", perr)
	}
	tk := p.Find("T1")
	if tk == nil {
		t.Fatal("task T1 vanished from PLAN.md")
	}
	if tk.Status != plan.TODO {
		t.Fatalf("cap path changed task status to %s; it must stay TODO", tk.Status)
	}
	if tk.Session != "bee-z" || tk.Heartbeat.IsZero() {
		t.Fatalf("cap path cleared the claim (session=%q heartbeat=%v); a stale claim must remain for GC", tk.Session, tk.Heartbeat)
	}
}

// TestTaskRemovedGuard: an operator removes the honeybee's task from PLAN.md on
// main after it started; the next turn's guard detects it and aborts with a
// warning recorded in the session file.
func TestTaskRemovedGuard(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	ctx := context.Background()
	base, err := g.RevParse(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	// Operator removes T1 from the plan on main after we captured base.
	os.WriteFile(planPath, []byte("## T2 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	g.Commit(ctx, "operator: drop T1")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
	r := &Runner{Repo: rp, Git: g, Client: &mockClient{}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, BaseMain: base}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Warning == "" || res.Completed {
		t.Fatalf("want abort warning, got %+v", res)
	}
	body, _ := os.ReadFile(filepath.Join(subs[0].SessionsDir(), res.SessionID+".md"))
	if !filepath.IsAbs(subs[0].SessionsDir()) {
		t.Fatalf("sessions dir not absolute: %s", subs[0].SessionsDir())
	}
	if !contains(string(body), "warning") {
		t.Fatalf("session file missing warning: %q", string(body))
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// scriptClient returns a session whose Prompt runs onPrompt and whose Messages
// grow, so the recorder records and flushes.
type scriptClient struct {
	onPrompt func()
	calls    int
}

func (c *scriptClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	return &scriptSession{c: c}, nil
}

type scriptSession struct{ c *scriptClient }

func (s *scriptSession) Prompt(ctx context.Context, text string) (string, error) {
	s.c.calls++
	if s.c.onPrompt != nil {
		s.c.onPrompt()
	}
	return "", nil
}
func (s *scriptSession) Close() error { return nil }
func (s *scriptSession) Messages(ctx context.Context) ([]Message, error) {
	return []Message{{ID: "m1", Role: "assistant", Parts: []Part{
		{Type: "text", Text: strings.Repeat("x", s.c.calls)},
	}}}, nil
}

// TestRunPublishesSessionToMain proves a no-remote honeybee, recording in its
// dedicated SESSION worktree, lands its session file on main's primary working
// tree — what the beehived UI reads — while the agent's bootstrapped PLAN.md
// lands via the (separate) agent worktree. Neither clobbers the other.
func TestRunPublishesSessionToMain(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\nbuild it\n"), 0o644)
	g.Commit(context.Background(), "seed roi")

	ctx := context.Background()
	// Agent beehive worktree (PLAN/docs) and a separate session worktree, exactly
	// as cmd/honeybee creates them.
	wtPath := filepath.Join(root, ".worktrees", "bee-1")
	if err := g.WorktreeAdd(ctx, wtPath, "bee-1", "main"); err != nil {
		t.Fatal(err)
	}
	sessPath := filepath.Join(root, ".worktrees", "bee-1-session")
	if err := g.WorktreeAdd(ctx, sessPath, "bee-1-session", "main"); err != nil {
		t.Fatal(err)
	}
	wrp, _ := repo.Open(wtPath)
	wg := git.New(wtPath)
	sessGit := git.New(sessPath)
	subs, _ := wrp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Bootstrap, Submodule: subs[0]}

	cl := &scriptClient{}
	cl.onPrompt = func() {
		// The agent bootstraps the plan inside ITS worktree and commits it there,
		// as opencode would.
		os.WriteFile(subs[0].PlanPath(), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		_ = wg.CommitPaths(ctx, "plan: bootstrap", "submodules/sm/PLAN.md")
	}
	r := &Runner{
		Repo: wrp, Git: wg, Client: cl, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour,
		Publish:        func(ctx context.Context) error { return wg.PublishToMain(ctx, "") },
		SessionGit:     sessGit,
		SessionRoot:    sessPath,
		SessionBranch:  "bee-1-session",
		SessionPublish: func(ctx context.Context) error { return sessGit.PublishToMain(ctx, "") },
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	// The final transcript must be committed in the SESSION worktree (the squashed
	// end-of-session commit; the live copy is streamed to the session branch).
	if _, err := os.Stat(filepath.Join(sessPath, "submodules", "sm", "sessions", res.SessionID+".md")); err != nil {
		t.Fatalf("session not written in session worktree: %v", err)
	}
	// Both the session transcript AND the bootstrapped plan must converge on main's
	// primary working tree, each via its own worktree's publish.
	if _, err := os.Stat(filepath.Join(root, "submodules", "sm", "sessions", res.SessionID+".md")); err != nil {
		t.Fatalf("session not published to main working tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "submodules", "sm", "PLAN.md")); err != nil {
		t.Fatalf("PLAN.md not published to main working tree: %v", err)
	}
}

func TestRunKeepsSessionUnpublishedWhenFinalPublishFails(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\nbuild it\n"), 0o644)
	g.Commit(context.Background(), "seed roi")

	ctx := context.Background()
	wtPath := filepath.Join(root, ".worktrees", "bee-fail")
	if err := g.WorktreeAdd(ctx, wtPath, "bee-fail", "main"); err != nil {
		t.Fatal(err)
	}
	sessPath := filepath.Join(root, ".worktrees", "bee-fail-session")
	if err := g.WorktreeAdd(ctx, sessPath, "bee-fail-session", "main"); err != nil {
		t.Fatal(err)
	}
	wrp, _ := repo.Open(wtPath)
	wg := git.New(wtPath)
	sessGit := git.New(sessPath)
	subs, _ := wrp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Bootstrap, Submodule: subs[0]}

	cl := &scriptClient{}
	cl.onPrompt = func() {
		os.WriteFile(subs[0].PlanPath(), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		_ = wg.CommitPaths(ctx, "plan: bootstrap", "submodules/sm/PLAN.md")
	}
	publishCalls := 0
	r := &Runner{
		Repo: wrp, Git: wg, Client: cl, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour,
		Publish:    func(ctx context.Context) error { return wg.PublishToMain(ctx, "") },
		SessionGit: sessGit, SessionRoot: sessPath, SessionBranch: "bee-fail-session",
		SessionPublish: func(ctx context.Context) error {
			publishCalls++
			if publishCalls == 2 {
				return errors.New("final session publish failed")
			}
			return sessGit.PublishToMain(ctx, "")
		},
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err == nil || !strings.Contains(err.Error(), "final session publish failed") {
		t.Fatalf("run error = %v, want final publish error", err)
	}
	if res.Completed || res.SessionPublished {
		t.Fatalf("final session publish failure must block completion and branch deletion, got %+v", res)
	}
	if publishCalls != 2 {
		t.Fatalf("SessionPublish calls = %d, want start + final", publishCalls)
	}

	sessRel := "submodules/sm/sessions/" + res.SessionID + ".md"
	mainBody, err := g.Show(ctx, "main", sessRel)
	if err != nil {
		t.Fatalf("main missing session stub: %v", err)
	}
	if _, ok := repo.ParseSessionStub(mainBody); !ok {
		t.Fatalf("main session body should remain live stub after failed final publish, got %q", mainBody)
	}
	branchBody, err := g.Show(ctx, "bee-fail-session", sessRel)
	if err != nil {
		t.Fatalf("session branch missing final transcript: %v", err)
	}
	if strings.Contains(branchBody, repo.SessionStub("bee-fail-session")) || !strings.Contains(branchBody, "# session "+res.SessionID) {
		t.Fatalf("session branch should retain final transcript, got %q", branchBody)
	}
}

// TestSessionAndPlanOnSeparateBranches guards the deconfliction invariant: the
// session transcript is AUTHORED on the session branch and the plan on the agent
// branch (never the same index), and both converge on main. (After publish the
// agent branch also carries the session file, but only via an index-aware
// git-merge from main — not an out-of-band commit — so nothing is clobbered.)
func TestSessionAndPlanOnSeparateBranches(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\n"), 0o644)
	g.Commit(context.Background(), "seed")
	ctx := context.Background()

	wtPath := filepath.Join(root, ".worktrees", "bee-2")
	g.WorktreeAdd(ctx, wtPath, "bee-2", "main")
	sessPath := filepath.Join(root, ".worktrees", "bee-2-session")
	g.WorktreeAdd(ctx, sessPath, "bee-2-session", "main")
	wrp, _ := repo.Open(wtPath)
	wg := git.New(wtPath)
	sessGit := git.New(sessPath)
	subs, _ := wrp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Bootstrap, Submodule: subs[0]}

	cl := &scriptClient{}
	cl.onPrompt = func() {
		os.WriteFile(subs[0].PlanPath(), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		_ = wg.CommitPaths(ctx, "plan: bootstrap", "submodules/sm/PLAN.md")
	}
	r := &Runner{
		Repo: wrp, Git: wg, Client: cl, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour,
		Publish:        func(ctx context.Context) error { return wg.PublishToMain(ctx, "") },
		SessionGit:     sessGit,
		SessionRoot:    sessPath,
		SessionBranch:  "bee-2-session",
		SessionPublish: func(ctx context.Context) error { return sessGit.PublishToMain(ctx, "") },
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	sessRel := "submodules/sm/sessions/" + res.SessionID + ".md"

	has := func(g *git.Repo, ref, path string) bool {
		_, err := g.Run(ctx, "cat-file", "-e", ref+":"+path)
		return err == nil
	}
	// The session transcript is authored on the session branch, the plan on the
	// agent branch — separate indexes, no out-of-band commits.
	if !has(g, "bee-2", "submodules/sm/PLAN.md") {
		t.Error("agent branch missing PLAN.md")
	}
	if !has(g, "bee-2-session", sessRel) {
		t.Error("session branch missing the session file")
	}
	// The session branch published before the agent's plan reached main, so it
	// never merged the plan: proof the two are authored independently.
	if has(g, "bee-2-session", "submodules/sm/PLAN.md") {
		t.Error("session branch unexpectedly has PLAN.md (indexes intermingled)")
	}
	// main: both, neither clobbered.
	if !has(g, "main", "submodules/sm/PLAN.md") || !has(g, "main", sessRel) {
		t.Error("main missing plan or session after convergence")
	}
}

// TestHeartbeatTerminalNotFatal is the regression for the "turn 2 heartbeat:
// task not in progress -> exit status 1" failure. The agent drives the task to
// NEEDS-REVIEW during turn 1 but does NOT place the change doc where the
// completion check looks (submodules/<sm>/docs/<branch>-<taskid>.md), so turn 1
// does not complete. The OLD runner then crashed on turn 2's heartbeat. The
// fixed runner must exit cleanly (no error) with a warning naming the expected
// doc path, leaving the task in NEEDS-REVIEW for a human/reviewer.
func TestHeartbeatTerminalNotFatal(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	// On turn 1 the agent flips to NEEDS-REVIEW (clearing heartbeat) but writes NO
	// doc at the expected path -> completion check fails -> loop continues to turn 2.
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run must not error on a terminal-but-incomplete task, got: %v", err)
	}
	if res.Completed {
		t.Fatalf("task has no doc at the expected path; must not report Completed: %+v", res)
	}
	if !contains(res.Warning, "completion check failed") || !contains(res.Warning, "T1") {
		t.Fatalf("warning should report the incomplete handoff, got %q", res.Warning)
	}
}

// TestWorkPreambleHasDocPath proves the runner's injected Context preamble (which
// ships in the binary, not the on-disk AGENTS.md) tells the agent the exact
// change-doc path the completion check requires.
func TestWorkPreambleHasDocPath(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	var firstPrompt string
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	cl.sess.capture = &firstPrompt
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	if _, err := r.Run(context.Background(), sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !contains(firstPrompt, "submodules/sm/docs/bee-T1-T1.md") {
		t.Fatalf("preamble missing exact doc path; got:\n%s", firstPrompt)
	}
	if !contains(firstPrompt, "submodules/sm/worktrees/bee-T1") {
		t.Fatalf("preamble missing code worktree path; got:\n%s", firstPrompt)
	}
}

// TestReviewCompletesOnStatusChange: a Review session is NOT claimed/clobbered
// and completes the moment the agent moves the task out of NEEDS-REVIEW (here
// -> DONE on approve). No code worktree, no heartbeat, no change-doc requirement
// — proving the runner treats a review as a judgement, not fresh Work.
func TestReviewCompletesOnStatusChange(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}

	var firstPrompt string
	cl := &mockClient{sess: &mockSession{capture: &firstPrompt, onTurn: func(turn int) {
		// Approve: move out of NEEDS-REVIEW to DONE (no doc written).
		os.WriteFile(planPath, []byte("## R1 [DONE] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("review should complete on status change, got %+v", res)
	}
	// The status on disk must NOT have been clobbered to IN-PROGRESS by the runner.
	if !contains(firstPrompt, "REVIEW") || !contains(firstPrompt, "do NOT reimplement") {
		t.Fatalf("review preamble missing review framing; got:\n%s", firstPrompt)
	}
	if !contains(firstPrompt, "bee-R1") {
		t.Fatalf("review preamble missing implementer branch ref; got:\n%s", firstPrompt)
	}
}

// gateClient/gateSession model a turn that is BUSY for a while before going idle:
// Prompt signals it started, blocks until the test releases it, then produces the
// completion artifacts and returns. Because ocSession.Prompt now blocks until the
// turn is idle, the runner's deterministic completion check runs ONLY after Prompt
// returns — so a busy turn can never be observed as complete. This stub drives
// that contract at the runner level.
type gateClient struct{ sess *gateSession }

func (c *gateClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	return c.sess, nil
}

type gateSession struct {
	started chan struct{}
	release chan struct{}
	onIdle  func()
	busied  bool
}

func (s *gateSession) Prompt(ctx context.Context, text string) (string, error) {
	if !s.busied {
		s.busied = true
		close(s.started) // turn 1 has entered: it is now "busy"
		select {
		case <-s.release: // stay busy until the test lets the turn settle
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if s.onIdle != nil {
			s.onIdle() // completion artifacts appear only as the turn goes idle
		}
	}
	return "", nil
}
func (s *gateSession) Close() error                                    { return nil }
func (s *gateSession) Messages(ctx context.Context) ([]Message, error) { return nil, nil }

// TestCompletionWaitsForTurnIdle proves the runner does not run its completion
// check while the turn is busy: with a stub Client whose Prompt blocks (busy),
// Run must not report completion until the turn is released (idle) — even though
// the completion artifacts are written exactly at the busy->idle transition.
func TestCompletionWaitsForTurnIdle(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	sess := &gateSession{started: make(chan struct{}), release: make(chan struct{}), onIdle: func() {
		// Land the completion artifacts only as the turn settles.
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}
	r := &Runner{Repo: rp, Git: g, Client: &gateClient{sess: sess}, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour}

	type out struct {
		res Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := r.Run(context.Background(), sel, "sys", "first")
		done <- out{res, err}
	}()

	<-sess.started // turn 1 is now busy (Prompt is blocked)
	// While busy, the run must NOT have completed: the completion check cannot run
	// until Prompt returns. Give it a window to (wrongly) proceed.
	select {
	case o := <-done:
		t.Fatalf("Run completed while the turn was still busy: %+v / %v", o.res, o.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(sess.release) // let the turn settle (idle); artifacts now exist
	o := <-done
	if o.err != nil {
		t.Fatalf("run: %v", o.err)
	}
	if !o.res.Completed || o.res.GCMarked {
		t.Fatalf("want completed after the turn settled, got %+v", o.res)
	}
}

// blockingSession's Prompt never returns until its context is canceled, modeling
// a stalled opencode call.
type blockingClient struct{}

func (blockingClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	return blockingSession{}, nil
}

type blockingSession struct{}

func (blockingSession) Prompt(ctx context.Context, text string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (blockingSession) Messages(ctx context.Context) ([]Message, error) {
	return []Message{{ID: "m", Role: "assistant", Parts: []Part{{Type: "text", Text: "working"}}}}, nil
}
func (blockingSession) Close() error { return nil }

// TestTurnTimeoutAbandonsForGC proves a stalled agent turn is canceled at
// TurnTimeout and the pass abandons the task for GC with a clear warning —
// instead of wedging on the dead call until the process backstop. This is the
// regression for the multi-hour zombie honeybees.
func TestTurnTimeoutAbandonsForGC(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\n"), 0o644)
	ctx := context.Background()
	g.Commit(ctx, "seed")
	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Bootstrap, Submodule: subs[0]}

	r := &Runner{
		Repo: rp, Git: g, Client: blockingClient{}, MaxTurns: 5,
		WallCap: time.Hour, TTL: time.Hour, TurnTimeout: 50 * time.Millisecond,
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("a turn timeout must not be a fatal error, got: %v", err)
	}
	if !res.GCMarked {
		t.Fatalf("want GCMarked on a stalled turn, got %+v", res)
	}
	if !contains(res.Warning, "per-turn timeout") {
		t.Fatalf("warning should name the per-turn timeout, got %q", res.Warning)
	}
}

// TestReconciledPrefixMatch proves the Reconcile completion check (Runner.reconciled)
// matches the PLAN.md ROI stamp against the full ROI head sha by PREFIX. The stamp
// is frequently abbreviated while head is the full %H sha, so the prior exact `==`
// compare never fired and reconcile reported "never done". A short stamp that
// prefixes the full head must clear (fire once); a full stamp still clears; a
// non-prefix or empty stamp must NOT clear.
func TestReconciledPrefixMatch(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("intent\n"), 0o644)
	ctx := context.Background()
	g.Commit(ctx, "seed roi")
	head, err := g.LastCommit(ctx, "submodules/sm/ROI.md")
	if err != nil || head == "" {
		t.Fatalf("ROI head: %q %v", head, err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Reconcile, Submodule: subs[0]}
	r := &Runner{Repo: rp, Git: g}
	planPath := filepath.Join(sm, "PLAN.md")

	cases := []struct {
		name  string
		stamp string
		want  bool
	}{
		{"short-prefix", head[:12], true}, // the real-world case the old `==` missed
		{"full-sha", head, true},
		{"non-prefix", "deadbeefcafe", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := "## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"
			if c.stamp != "" {
				body = "<!-- Beehive-ROI: " + c.stamp + " -->\n" + body
			}
			if err := os.WriteFile(planPath, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := r.reconciled(sel)
			if err != nil {
				t.Fatalf("reconciled: %v", err)
			}
			if got != c.want {
				t.Fatalf("stamp %q: got reconciled=%v want %v (head %s)", c.stamp, got, c.want, head)
			}
		})
	}
}

// TestRunPublishFailureBlocksCompletion guards correctness: a task that reaches
// its terminal state LOCALLY but whose publish to main fails must NOT be reported
// Completed (that would be a phantom DONE whose work never landed). Instead the
// run leaves the claim unreleased and marks itself for GC so the work is re-driven.
func TestRunPublishFailureBlocksCompletion(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	// Agent drives the task terminal + writes the change doc (completion check passes)…
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	// …but publishing to main fails once the work is terminal. Gate on [DONE] so the
	// per-turn claim heartbeat (which shares this closure, pre-Prompt while still
	// IN-PROGRESS) still succeeds; only the completion publish fails.
	r := &Runner{
		Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		Publish: func(ctx context.Context) error {
			if b, _ := os.ReadFile(planPath); strings.Contains(string(b), "[DONE]") {
				return errors.New("publish boom")
			}
			return nil
		},
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed {
		t.Fatalf("publish failed — task must NOT report Completed (phantom DONE): %+v", res)
	}
	if !res.GCMarked {
		t.Fatalf("publish failed — run must mark GC so the work is re-driven: %+v", res)
	}
	if res.Warning == "" || !strings.Contains(res.Warning, "publishing to main failed") {
		t.Fatalf("publish failure should be surfaced in the warning: %+v", res)
	}
}

// TestWorkSyncsWorktreeBaseToTrackedTip proves the Work setup fetches and
// hard-resets the submodule checkout to the tracked-branch tip BEFORE branching
// the code worktree, so a honeybee starts from the live code rather than the
// stale recorded pointer — and that the resulting pointer move is committed in
// the beehive worktree (the reviewless auto-advance). The submodule's origin gets
// an EXTRA commit after the local checkout is cloned, exactly the stale-pointer
// scenario the task targets.
func TestWorkSyncsWorktreeBaseToTrackedTip(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	ctx := context.Background()

	// A temp origin for the submodule with a base commit, then an EXTRA commit
	// added AFTER the local checkout clones it — so the clone's recorded pointer
	// (base) lags the tracked tip.
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
	os.WriteFile(filepath.Join(origin, "f"), []byte("tip"), 0o644)
	if err := og.Commit(ctx, "tip"); err != nil {
		t.Fatalf("origin tip: %v", err)
	}
	originTip, err := og.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("origin tip rev: %v", err)
	}

	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	// Seed records the submodule gitlink at the stale base (the clone's HEAD).
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	wtDir := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
	var wtBase string
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		// Capture the code worktree's base commit before completion reclaims it.
		if b, err := git.New(wtDir).RevParse(ctx, "HEAD"); err == nil {
			wtBase = b
		}
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	// 1) The code worktree must branch off the SYNCED tip, not the stale pointer.
	if wtBase == "" {
		t.Fatal("worktree base was never captured; the worktree was not created")
	}
	if wtBase != originTip {
		t.Fatalf("worktree base %s != origin tip %s: the checkout was not synced before branching", wtBase, originTip)
	}
	// 2) The beehive pointer move must have been committed in r.Git.
	ptr, err := g.Run(ctx, "rev-parse", "HEAD:submodules/sm/repo")
	if err != nil {
		t.Fatalf("read committed gitlink: %v", err)
	}
	if ptr != originTip {
		t.Fatalf("committed submodule pointer %s != origin tip %s: the sync pointer bump was not committed", ptr, originTip)
	}
}

// TestWorkNoRemoteKeepsRecordedPointer proves the sync is a no-op without a
// submodule remote (single-host install / nested test checkout): the worktree
// still branches off the recorded pointer (HEAD) and NO spurious pointer-bump
// commit is made.
func TestWorkNoRemoteKeepsRecordedPointer(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	ctx := context.Background()
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir) // no remote configured
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(ctx, "base")
	subTip, err := git.New(repoDir).RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("sub tip: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	wtDir := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
	var wtBase string
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		if b, err := git.New(wtDir).RevParse(ctx, "HEAD"); err == nil {
			wtBase = b
		}
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	if _, err := r.Run(ctx, sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if wtBase != subTip {
		t.Fatalf("worktree base %s != recorded sub tip %s", wtBase, subTip)
	}
	// No remote => the sync makes NO pointer-bump commit: the recorded gitlink in
	// the beehive worktree stays the original pointer (per-turn heartbeat commits
	// re-stamp PLAN.md but never touch the unchanged submodule gitlink).
	ptr, err := g.Run(ctx, "rev-parse", "HEAD:submodules/sm/repo")
	if err != nil {
		t.Fatalf("read committed gitlink: %v", err)
	}
	if ptr != subTip {
		t.Fatalf("no-remote run moved the submodule pointer (%s != recorded %s)", ptr, subTip)
	}
}

// modelClient implements ModelClient, recording which open path each pass took:
// the per-kind OpenModel (capturing the model string) or the plain default Open.
// It lets the routing tests prove a pass is dispatched to the configured per-kind
// model, and falls back to the client default when a kind has no route.
type modelClient struct {
	sess         *mockSession
	openModels   []string // model arg of each OpenModel (per-kind route) call
	defaultOpens int      // count of plain Open (client-default) calls
}

var _ ModelClient = (*modelClient)(nil)

func (m *modelClient) session() *mockSession {
	if m.sess == nil {
		m.sess = &mockSession{}
	}
	return m.sess
}

func (m *modelClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	m.defaultOpens++
	return m.session(), nil
}

func (m *modelClient) OpenModel(ctx context.Context, cwd, system, model string) (Session, error) {
	m.openModels = append(m.openModels, model)
	return m.session(), nil
}

// seedWorkTask stands up a beehive repo with one submodule 'sm', a committed code
// checkout, and a claimable Work task T1 (seeded [IN-PROGRESS], which parses to
// TODO). It mirrors the inline fixture the other Work tests use, factored out for
// the routing/idle tests, and returns the runner-facing handles plus the submodule
// dir and PLAN.md path.
func seedWorkTask(t *testing.T) (*repo.Repo, *git.Repo, *selectt.Selection, string, string) {
	t.Helper()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(context.Background(), "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [IN-PROGRESS] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")
	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
	return rp, g, sel, sm, planPath
}

// completeOnTurn drives the standard Work task T1 to DONE with its expected change
// doc, so a pass completes on the turn it runs.
func completeOnTurn(sm, planPath string) func(int) {
	return func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}
}

// TestRunRoutesModelByKind proves a pass whose kind has a configured model is
// dispatched to that model via the ModelClient path — the per-kind routing that
// sends a cheap model to trivial passes and reserves the strong model for real
// Work.
func TestRunRoutesModelByKind(t *testing.T) {
	rp, g, sel, sm, planPath := seedWorkTask(t)
	mc := &modelClient{sess: &mockSession{onTurn: completeOnTurn(sm, planPath)}}
	r := &Runner{
		Repo: rp, Git: g, Client: mc, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		ModelByKind: map[string]string{"work": "cheap/x"},
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	if len(mc.openModels) != 1 || mc.openModels[0] != "cheap/x" {
		t.Fatalf("want one OpenModel(cheap/x), got openModels=%v defaultOpens=%d", mc.openModels, mc.defaultOpens)
	}
	if mc.defaultOpens != 0 {
		t.Fatalf("a routed kind must not open on the default model, defaultOpens=%d", mc.defaultOpens)
	}
}

// TestRunNoModelRouteUsesDefault proves a kind with NO configured model opens on
// the client's default model (plain Open) even when other kinds are routed — so a
// single-model host, or any unrouted kind, is unaffected.
func TestRunNoModelRouteUsesDefault(t *testing.T) {
	rp, g, sel, sm, planPath := seedWorkTask(t)
	mc := &modelClient{sess: &mockSession{onTurn: completeOnTurn(sm, planPath)}}
	r := &Runner{
		Repo: rp, Git: g, Client: mc, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		ModelByKind: map[string]string{"reconcile": "cheap/x"}, // no "work" route
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed, got %+v", res)
	}
	if mc.defaultOpens != 1 || len(mc.openModels) != 0 {
		t.Fatalf("an unrouted kind must open on the default model, got defaultOpens=%d openModels=%v", mc.defaultOpens, mc.openModels)
	}
}

// TestModelRouteInertWithoutModelClient proves model routing is inert when the
// Client cannot honor it: a plain Client (not a ModelClient) with ModelByKind set
// still runs the pass on its single model rather than failing — routing is an
// optional capability, never a hard dependency.
func TestModelRouteInertWithoutModelClient(t *testing.T) {
	rp, g, sel, sm, planPath := seedWorkTask(t)
	cl := &mockClient{sess: &mockSession{onTurn: completeOnTurn(sm, planPath)}}
	r := &Runner{
		Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		ModelByKind: map[string]string{"work": "cheap/x"}, // set, but cl cannot honor it
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("routing must be inert (fall back to Open) for a non-ModelClient, got %+v", res)
	}
}

// TestRunStopsOnNoForwardProgress proves the no-forward-progress cap: a pass that
// keeps running turns without changing any artifact it acts on (no code-worktree
// or change-doc movement) is abandoned for GC once it repeats MaxIdleTurns times —
// well before the raw turn cap — with a warning naming the stuck pass.
func TestRunStopsOnNoForwardProgress(t *testing.T) {
	rp, g, sel, _, _ := seedWorkTask(t)
	// The agent does nothing each turn (no onTurn): every turn is idle.
	r := &Runner{
		Repo: rp, Git: g, Client: &mockClient{}, MaxTurns: 20, WallCap: time.Hour, TTL: time.Hour,
		MaxIdleTurns: 2,
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed {
		t.Fatalf("an idle pass must not report completion, got %+v", res)
	}
	if !res.GCMarked {
		t.Fatalf("want GCMarked on a no-progress pass, got %+v", res)
	}
	if res.Turns != 2 {
		t.Fatalf("idle cap must fire at MaxIdleTurns (2), not the turn cap; got Turns=%d", res.Turns)
	}
	if !contains(res.Warning, "no forward progress") {
		t.Fatalf("warning should name the stalled progress, got %q", res.Warning)
	}
}

// TestRunNoIdleCapWhenDisabled proves MaxIdleTurns==0 disables the detector
// entirely: an idle pass runs all the way to the raw turn cap (Turns==MaxTurns+1)
// exactly as before, and no idle warning is produced — the feature is opt-in.
func TestRunNoIdleCapWhenDisabled(t *testing.T) {
	rp, g, sel, _, _ := seedWorkTask(t)
	r := &Runner{
		Repo: rp, Git: g, Client: &mockClient{}, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour,
		MaxIdleTurns: 0,
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.GCMarked || res.Turns != 4 {
		t.Fatalf("disabled idle cap must run to the turn cap (Turns=4, GCMarked), got %+v", res)
	}
	if contains(res.Warning, "no forward progress") {
		t.Fatalf("no idle warning must appear when the cap is disabled, got %q", res.Warning)
	}
}

// TestRunIdleResetsOnProgress proves a pass that makes real progress every turn (a
// new change doc each turn) never trips the idle cap: the consecutive-idle counter
// resets on each change, so the run continues to the turn cap rather than being
// abandoned mid-flight.
func TestRunIdleResetsOnProgress(t *testing.T) {
	rp, g, sel, sm, _ := seedWorkTask(t)
	// Each turn writes a NEW doc (never the completion stem; status stays TODO) so
	// progressSig advances every turn while the task never completes.
	sess := &mockSession{}
	sess.onTurn = func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", fmt.Sprintf("progress-%d.md", turn)), []byte("x"), 0o644)
	}
	r := &Runner{
		Repo: rp, Git: g, Client: &mockClient{sess: sess}, MaxTurns: 4, WallCap: time.Hour, TTL: time.Hour,
		MaxIdleTurns: 2,
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Turns != 5 {
		t.Fatalf("progress every turn must reset the idle counter and reach the turn cap (Turns=5), got %d", res.Turns)
	}
	if contains(res.Warning, "no forward progress") {
		t.Fatalf("a progressing pass must not trip the idle cap, got %q", res.Warning)
	}
	if !res.GCMarked {
		t.Fatalf("want GCMarked at the turn cap, got %+v", res)
	}
}
