package swarm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/audit"
	"github.com/spencerharmon/beehive/internal/claim"
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
		Model:          "test/model-x",
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
	// The transcript header must carry the model this pass ran on, so the stats page
	// can derive per-model performance from git.
	body, err := os.ReadFile(filepath.Join(root, "submodules", "sm", "sessions", res.SessionID+".md"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.Contains(string(body), "· model: test/model-x") {
		t.Fatalf("transcript header missing model stamp; got head:\n%.200s", body)
	}
}

func TestRunTranscriptPublishFailureDoesNotBlockWork(t *testing.T) {
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
	// The transcript publish to main is a convenience decoupled from the work: its
	// failure must NOT fail the run nor block completion (the work — here the
	// bootstrap PLAN.md — still lands on main).
	if err != nil {
		t.Fatalf("run error = %v, want nil (transcript publish failure is non-fatal)", err)
	}
	if !res.Completed {
		t.Fatalf("work must still complete despite transcript publish failure, got %+v", res)
	}
	// But SessionPublished stays false so the stream branch is KEPT as the
	// transcript source, and main keeps the live stub.
	if res.SessionPublished {
		t.Fatalf("SessionPublished must be false when the transcript publish failed, got %+v", res)
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
	// Work now publishes before the transcript, so the transcript publish merges
	// main (which already carries the plan) into the session branch via an
	// index-aware git-merge — not an out-of-band commit. The session branch thus
	// legitimately gains PLAN.md while its transcript survives intact (the real
	// anti-clobber invariant: separate authorship, nothing overwritten).
	if !has(g, "bee-2-session", "submodules/sm/PLAN.md") {
		t.Error("session branch should carry PLAN.md via the index-aware merge from main")
	}
	sbody, err := g.Show(ctx, "bee-2-session", sessRel)
	if err != nil || !strings.Contains(sbody, "# session "+res.SessionID) {
		t.Errorf("session branch transcript clobbered by the merge: err=%v body=%q", err, sbody)
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

// refusingClient fails the test if Open is EVER called — used to prove a
// dispatch-time short-circuit (the F-LIVE review-dispatch reachability guard)
// never spends a single agent turn.
type refusingClient struct{ t *testing.T }

func (c *refusingClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	c.t.Fatal("Client.Open called: a session must not be opened for this dispatch")
	return nil, nil
}

// TestReviewDispatchBouncesUnreachableCommit is the F-LIVE review-dispatch-side
// guard (session-audit-003): a task is NEEDS-REVIEW but the submodule pointer
// (gitlink) recorded for it — the reviewable commit a Work pass bumps as part
// of completing to NEEDS-REVIEW — is a PHANTOM sha that exists nowhere: not in
// the submodule's object database, not on origin. bee-orphan-x itself DOES
// exist (a real local ref, just at a DIFFERENT tip than the phantom gitlink) —
// deliberately distinguishing this from the needs-review-auto-recover-lost-work
// guard's "branch absent everywhere" shape (recoverIfLost's check 1 finds the
// local ref present and lets this fall through unchanged), isolating this test
// to the bounce guard it is named for. This is the "reviewable commit exists
// nowhere" shape that otherwise spawns a review pass doomed to spelunk git
// internals and idle-time-out forever. Run() must bounce the task straight to
// NEEDS-ARBITRATION with a concrete reason (naming the unreachable sha) and
// NEVER open a session (asserted by refusingClient), reporting Completed so the
// dispatch itself is not retried.
func TestReviewDispatchBouncesUnreachableCommit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	sg := gitConfig(t, repoDir)
	// bee-orphan-x is a REAL local ref (a genuine, if unmerged, prior attempt) —
	// just not at the sha the (corrupted) gitlink names, so recoverIfLost must
	// NOT treat this as "lost everywhere" and must fall through to the bounce
	// guard below.
	if _, err := sg.Run(ctx, "checkout", "-q", "-b", "bee-orphan-x"); err != nil {
		t.Fatalf("branch bee-orphan-x: %v", err)
	}
	os.WriteFile(filepath.Join(repoDir, "g"), []byte("orphan attempt\n"), 0o644)
	if err := sg.Commit(ctx, "orphan attempt"); err != nil {
		t.Fatalf("commit orphan attempt: %v", err)
	}

	// The gitlink records a PHANTOM commit: the submodule pointer a Work pass
	// would have bumped for this task, but the commit exists NOWHERE (never
	// committed anywhere this test keeps) — the exact F-LIVE shape.
	phantom := strings.Repeat("f", 40)
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+phantom+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed phantom gitlink: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## orphan-x [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "orphan-x", Status: plan.NeedsReview}}

	r := &Runner{Repo: rp, Git: g, Client: &refusingClient{t: t}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a resolved dispatch-time bounce should report Completed, got %+v", res)
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("orphan-x")
	if tk == nil {
		t.Fatal("task orphan-x vanished from PLAN.md")
	}
	if tk.Status != plan.NeedsArb {
		t.Fatalf("status = %s, want NEEDS-ARBITRATION", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("bounce must release the claim")
	}
	joined := strings.Join(tk.Body, "\n")
	if !contains(joined, "reviewable commit unreachable") || !contains(joined, phantom) {
		t.Fatalf("plan body missing bounce reason naming the unreachable sha %s:\n%s", phantom, joined)
	}
}

// TestReviewDispatchRecoversTrulyLostWork is the needs-review-auto-recover-
// lost-work guard's core case: a task is NEEDS-REVIEW but its bee-<taskid>
// branch never landed ANYWHERE — no local ref, no ref on the submodule's
// remote (even after a prune-fetch), the gitlink was never advanced onto it
// (still whatever it was before this task's Work pass), and no change doc
// exists on disk. This is the exact "publish never landed" shape a
// crash/kill/failed-push leaves behind. Run() must reset the task straight to
// TODO (claim cleared, attempts incremented) and NEVER open a session
// (refusingClient) NOR bounce it to NEEDS-ARBITRATION (which would be equally
// doomed — there is nothing there for an arbitration pass to judge either),
// reporting Completed so the dispatch itself is not retried.
func TestReviewDispatchRecoversTrulyLostWork(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)

	// The gitlink still names the submodule's pre-task base commit (whatever
	// origin/main resolves to): the Work pass's own publish never landed, so
	// the gitlink was never bumped onto bee-lost-1 at all. bee-lost-1 is never
	// created, locally or on origin, and no change doc is written either — the
	// exact "publish never landed" shape.
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## lost-1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/repo", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "lost-1", Status: plan.NeedsReview}}

	r := &Runner{Repo: rp, Git: g, Client: &refusingClient{t: t}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a resolved dispatch-time recovery should report Completed, got %+v", res)
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("lost-1")
	if tk == nil {
		t.Fatal("task lost-1 vanished from PLAN.md")
	}
	if tk.Status != plan.StatusTODO {
		t.Fatalf("status = %s, want TODO", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("recovery must release the claim")
	}
	if tk.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", tk.Attempts)
	}
	joined := strings.Join(tk.Body, "\n")
	if !contains(joined, "unrecoverable") || !contains(joined, "bee-lost-1") {
		t.Fatalf("plan body missing recovery reason naming bee-lost-1:\n%s", joined)
	}
}

// TestArbitrationDispatchRecoversTrulyLostWork proves the guard also covers
// NEEDS-ARBITRATION dispatch (not just NEEDS-REVIEW): the same "lost
// everywhere" shape on a task an arbitration pass would otherwise be dispatched
// against must reset straight to TODO instead.
func TestArbitrationDispatchRecoversTrulyLostWork(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)

	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## lost-2 [NEEDS-ARBITRATION] <!-- attempts=1 deps= -->\narbitrate\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/repo", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Arbitrate, Submodule: subs[0], Task: plan.Task{ID: "lost-2", Status: plan.NeedsArb}}

	r := &Runner{Repo: rp, Git: g, Client: &refusingClient{t: t}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a resolved dispatch-time recovery should report Completed, got %+v", res)
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("lost-2")
	if tk == nil {
		t.Fatal("task lost-2 vanished from PLAN.md")
	}
	if tk.Status != plan.StatusTODO {
		t.Fatalf("status = %s, want TODO", tk.Status)
	}
	if tk.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", tk.Attempts)
	}
}

// TestReviewDispatchDoesNotRecoverWithDocPresent proves recoverIfLost's
// conservative guard: even with no local branch and no remote branch, a
// PRESENT change doc is a single positive signal that must abort the recovery
// (no false-positive data loss) and fall through to the normal reachability
// guard (bounceIfUnreachable), which — the commit still being unreachable —
// bounces to NEEDS-ARBITRATION exactly as before this guard existed.
func TestReviewDispatchDoesNotRecoverWithDocPresent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)

	// A change doc DOES exist (as if the work pass wrote it before an
	// interrupted publish), even though the gitlink names a PHANTOM commit
	// that exists nowhere (the same unreachable shape
	// TestReviewDispatchBouncesUnreachableCommit uses) — bounceIfUnreachable is
	// exactly the guard that must still fire here, once recoverIfLost declines
	// (a doc is present) and falls through.
	phantom := strings.Repeat("f", 40)
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+phantom+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed phantom gitlink: %v", err)
	}
	os.WriteFile(filepath.Join(sm, "docs", "bee-doc-only-doc-only.md"), []byte("# doc\n"), 0o644)

	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## doc-only [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md", "submodules/sm/docs"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "doc-only", Status: plan.NeedsReview}}

	r := &Runner{Repo: rp, Git: g, Client: &refusingClient{t: t}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a resolved dispatch-time bounce should report Completed, got %+v", res)
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("doc-only")
	if tk == nil {
		t.Fatal("task doc-only vanished from PLAN.md")
	}
	if tk.Status != plan.NeedsArb {
		t.Fatalf("status = %s, want NEEDS-ARBITRATION (recovery must NOT fire with a doc present)", tk.Status)
	}
}

// TestReviewDispatchReachableLocalSharingUnchanged is BOTH the guard's happy
// path AND the F-LIVE fetch-fallback fix: the submodule pointer recorded for
// the task is bumped to a real implementer commit that lives ONLY as a local
// ref in a submodule checkout with NO remote at all (local-sharing / a shared
// checkout — the exact mode where the OLD "fetch origin" recovery used to
// hard-fail with "origin does not appear to be a git repository"), and is
// deliberately NOT the checkout's own checked-out HEAD (proving the check
// resolves the SPECIFIC recorded sha, not just "whatever is checked out"). The
// guard must find it reachable via the shared module store WITHOUT attempting
// any fetch, and dispatch the review completely normally — a session opens and
// completes the task, byte-identical to the pre-existing behavior. It also
// doubles as the "genuinely pending, not yet merged" case for
// finalizeIfAlreadyMerged (which now runs first): bee-R1's tip is never merged
// into local main here, so that guard must correctly find it NOT an ancestor
// and fall through to this same reachable-and-dispatches-normally path — no
// false-positive auto-DONE on real pending work.
func TestReviewDispatchReachableLocalSharingUnchanged(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	sg := gitInit(t, repoDir) // a real checkout, deliberately NO remote (local-sharing)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("base\n"), 0o644)
	if err := sg.Commit(ctx, "base"); err != nil {
		t.Fatalf("submodule base commit: %v", err)
	}
	// The implementer's work: a real commit on branch bee-R1 — present as a
	// plain LOCAL ref only, never pushed anywhere — exactly what a shared
	// honeybee worktree of this SAME repo leaves behind in local-sharing mode.
	// Checked out back to main afterward so the gitlink sha below is NOT
	// repoDir's own checked-out HEAD.
	if _, err := sg.Run(ctx, "checkout", "-q", "-b", "bee-R1"); err != nil {
		t.Fatalf("checkout bee-R1: %v", err)
	}
	os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("work\n"), 0o644)
	if err := sg.Commit(ctx, "feat: work"); err != nil {
		t.Fatalf("commit work: %v", err)
	}
	implSHA, err := sg.RevParse(ctx, "bee-R1")
	if err != nil {
		t.Fatalf("rev-parse bee-R1: %v", err)
	}
	if _, err := sg.Run(ctx, "checkout", "-q", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	// The beehive repo's gitlink is bumped to the implementer's commit (what a
	// real Work-task completion does).
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+implSHA+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed bumped gitlink: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte("## R1 [DONE] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a reachable review should complete normally, got %+v", res)
	}
	if cl.sess.prompts == 0 {
		t.Fatal("a reachable review must actually dispatch a session (not bounce)")
	}
	b, _ := os.ReadFile(planPath)
	p, _ := plan.Parse(string(b))
	if tk := p.Find("R1"); tk == nil || tk.Status != plan.Done {
		t.Fatalf("status = %+v, want DONE (review dispatched and approved normally)", tk)
	}
}

// TestReviewDispatchFinalizesAlreadyMergedRemote is the review-already-merged-
// guard's remote-mode happy path (session-audit-005 F-1, the symmetric
// counterpart of TestReviewDispatchBouncesUnreachableCommit): the task's
// recorded submodule pointer (bumped by a Work pass) is ALREADY an ancestor of
// the submodule's tracked origin/main — the shape a review leaves behind when
// it merges bee-<taskid> into tracked main and pushes (durable) but is
// interrupted before it can commit the hive-layer half (gitlink bump + PLAN.md
// DONE). Tracked main is deliberately pushed PAST the implementer's own commit
// (an unrelated follow-up commit lands on top) so the assertion that the
// gitlink advances to the CURRENT tracked tip — not merely the implementer's
// own sha — actually proves something. The runner must finalize
// deterministically: NEVER spawn a second review session (refusingClient fails
// the test if Open is ever called), bump the gitlink to the tracked tip, and
// flip the task straight to DONE with a note — regression: before this guard a
// whole redundant review pass would be dispatched to re-discover the merge.
func TestReviewDispatchFinalizesAlreadyMergedRemote(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)

	// A prior (interrupted) review already merged bee-R1 into tracked main and
	// pushed it; an unrelated follow-up commit then also landed on tracked main
	// (a second, already-completed approval), so the recorded (pre-merge)
	// implementer commit is an ancestor of tracked main WITHOUT being its tip.
	sc := filepath.Join(t.TempDir(), "push")
	if _, err := g.Run(ctx, "clone", "-q", origin, sc); err != nil {
		t.Fatalf("scratch clone: %v", err)
	}
	scg := gitConfig(t, sc)
	os.WriteFile(filepath.Join(sc, "feature.txt"), []byte("work\n"), 0o644)
	if err := scg.Commit(ctx, "feat: work"); err != nil {
		t.Fatalf("commit implementer work: %v", err)
	}
	implSHA, err := scg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse implementer commit: %v", err)
	}
	// The implementer's source branch is pushed too — a real interrupted review
	// leaves bee-R1 on origin exactly like this: reclaiming it is gated on the
	// task reaching DONE (reclaimSourceBranch), which never happened here. This
	// is what makes the pre-fix failure mode the RIGHT one: bounceIfUnreachable
	// finds bee-R1 genuinely reachable and dispatches a normal (redundant)
	// review, rather than incorrectly bouncing to NEEDS-ARBITRATION for an
	// unrelated reason.
	if _, err := scg.Run(ctx, "push", "origin", "HEAD:refs/heads/bee-R1"); err != nil {
		t.Fatalf("push bee-R1 (still unreclaimed): %v", err)
	}
	os.WriteFile(filepath.Join(sc, "unrelated.txt"), []byte("other\n"), 0o644)
	if err := scg.Commit(ctx, "unrelated follow-up merge"); err != nil {
		t.Fatalf("commit follow-up: %v", err)
	}
	if err := scg.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("push tracked main past the implementer commit: %v", err)
	}
	mainTip, err := scg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse pushed main tip: %v", err)
	}

	// The beehive gitlink is left at the PRE-merge recorded pointer (the
	// implementer's own commit) — exactly the interrupted-review shape.
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+implSHA+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed pre-merge gitlink: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}

	r := &Runner{Repo: rp, Git: g, Client: &refusingClient{t: t}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("an already-merged task should finalize at dispatch time, got %+v", res)
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("R1")
	if tk == nil {
		t.Fatal("task R1 vanished from PLAN.md")
	}
	if tk.Status != plan.Done {
		t.Fatalf("status = %s, want DONE", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("finalize must release the claim")
	}
	joined := strings.Join(tk.Body, "\n")
	if !contains(joined, "already merged into tracked main") || !contains(joined, mainTip) {
		t.Fatalf("plan body missing finalize note naming the merged tip %s:\n%s", mainTip, joined)
	}
	out, err := g.Run(ctx, "ls-files", "-s", "submodules/sm/repo")
	if err != nil {
		t.Fatalf("ls-files gitlink: %v", err)
	}
	if !contains(out, mainTip) {
		t.Fatalf("gitlink = %q, want bumped to the tracked-main tip %s (not left at the implementer commit %s)", out, mainTip, implSHA)
	}
}

// TestRecordReviewedCommitRecordsBranchTipNotAmbientPointer is the regression
// for record-review-commit-ambient-pointer: a completed Work pass on a BUSY
// submodule must record its OWN bee-<taskid> tip as ReviewCommit, never the
// ambient beehive-layer gitlink `HEAD:submodules/<sm>/repo`. Pre-fix,
// recordReviewedCommit stamped HEAD:rel, which on a busy submodule is a SIBLING
// task's already-merged pointer (an ancestor of tracked main) — so
// finalizeIfMergedByRecord later trivially "proved" that unrelated sha merged
// and flipped the task DONE while its real work never merged (observed live:
// flux phantom-library-bluegreen-deploy recorded the gitea-OOM sibling commit;
// gostream state-pvc recorded the zuul-ci merge). Here the ambient gitlink is
// deliberately a sibling commit (mainTip), while bee-R1 sits at the real,
// unmerged work (workSHA); the recorded review= MUST be workSHA.
func TestRecordReviewedCommitRecordsBranchTipNotAmbientPointer(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)

	// A scratch clone drives origin. First: the REAL work for R1 on bee-R1
	// (branched off the base, deliberately NEVER merged into main), pushed to
	// origin exactly as a completed Work pass leaves it.
	sc := filepath.Join(t.TempDir(), "push")
	if _, err := g.Run(ctx, "clone", "-q", origin, sc); err != nil {
		t.Fatalf("scratch clone: %v", err)
	}
	scg := gitConfig(t, sc)
	os.WriteFile(filepath.Join(sc, "r1-work.txt"), []byte("the real R1 work\n"), 0o644)
	if err := scg.Commit(ctx, "R1: real implementer work"); err != nil {
		t.Fatalf("commit R1 work: %v", err)
	}
	workSHA, err := scg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse R1 work: %v", err)
	}
	if _, err := scg.Run(ctx, "push", "origin", "HEAD:refs/heads/bee-R1"); err != nil {
		t.Fatalf("push bee-R1: %v", err)
	}

	// Now a SIBLING task's work lands on tracked main (the busy-submodule shape):
	// main advances to mainTip, an already-merged commit unrelated to R1. Reset
	// the scratch back to the seeded base first so this is NOT built on bee-R1.
	if _, err := scg.Run(ctx, "reset", "--hard", "origin/main"); err != nil {
		t.Fatalf("reset scratch to main: %v", err)
	}
	os.WriteFile(filepath.Join(sc, "sibling.txt"), []byte("unrelated sibling task\n"), 0o644)
	if err := scg.Commit(ctx, "sibling: unrelated merged work"); err != nil {
		t.Fatalf("commit sibling: %v", err)
	}
	if err := scg.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("push sibling to main: %v", err)
	}
	mainTip, err := scg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse main tip: %v", err)
	}
	if workSHA == mainTip {
		t.Fatal("test bug: work and sibling commits must differ")
	}

	// The ambient beehive-layer gitlink names the SIBLING's already-merged
	// pointer (mainTip) — precisely what HEAD:submodules/sm/repo resolves to when
	// a sibling pass most recently bumped the single shared gitlink.
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+mainTip+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed ambient sibling gitlink: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nwork\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}
	r := &Runner{Repo: rp, Git: g, TTL: time.Hour}
	cl := &claim.Claimer{Repo: rp, Sub: subs[0], Git: g, TTL: time.Hour, Session: "bee-R1"}

	if w := r.recordReviewedCommit(ctx, sel, "bee-R1", root, cl); w != "" {
		t.Fatalf("recordReviewedCommit returned warning: %s", w)
	}

	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("R1")
	if tk == nil {
		t.Fatal("task R1 vanished from PLAN.md")
	}
	if tk.ReviewCommit != workSHA {
		t.Fatalf("ReviewCommit = %q, want the bee-R1 tip %q (NOT the ambient sibling pointer %q)", tk.ReviewCommit, workSHA, mainTip)
	}
	if tk.ReviewCommit == mainTip {
		t.Fatal("ReviewCommit recorded the ambient sibling gitlink pointer — the exact record-review-commit-ambient-pointer bug")
	}
}

// TestReviewDispatchFinalizesAlreadyMergedLocalSharing mirrors
// TestReviewDispatchFinalizesAlreadyMergedRemote in local-sharing mode (a
// submodule checkout with NO remote at all): the recorded pointer is already an
// ancestor of the LOCAL tracked main branch, resolved directly with no fetch —
// the same shared-module-store contract CommitReachable's local fast path
// relies on (no mode flag; derived from remotes). A real local bee-R1 ref
// (merged-guard-branch-gate's own gate: sourceBranchExists) sits at that same
// commit, exactly what a genuine fast-forward merge leaves behind. Must
// finalize without ever dispatching a session.
// TestReviewDispatchFinalizesByRecordWhenBranchGone is the durable-record
// regression (lost-work-durable-fix): an interrupted review merged bee-R1 into
// tracked main but its DONE bookkeeping never landed, and the bee-R1 branch has
// since been reclaimed (gone from origin) — the exact phantom-library
// m14-per-user-rig shape. Neither finalizeIfAlreadyMerged nor bounceIfUnreachable
// can recognize the merge without the branch, and recoverIfLost would misread the
// vanished branch as lost work and reset/loop. The task's DURABLE review=<sha>
// record lets finalizeIfMergedByRecord finalize it to DONE at dispatch, no branch
// required.
func TestReviewDispatchFinalizesByRecordWhenBranchGone(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)

	// The implementer commit was merged into tracked main by a prior interrupted
	// review; a later unrelated commit also landed, so the recorded commit is an
	// ancestor of main without being its tip. Crucially, bee-R1 is NOT pushed —
	// it was reclaimed, so it resolves nowhere on origin.
	sc := filepath.Join(t.TempDir(), "push")
	if _, err := g.Run(ctx, "clone", "-q", origin, sc); err != nil {
		t.Fatalf("scratch clone: %v", err)
	}
	scg := gitConfig(t, sc)
	os.WriteFile(filepath.Join(sc, "feature.txt"), []byte("work\n"), 0o644)
	if err := scg.Commit(ctx, "feat: work"); err != nil {
		t.Fatalf("commit implementer work: %v", err)
	}
	implSHA, err := scg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse implementer commit: %v", err)
	}
	os.WriteFile(filepath.Join(sc, "unrelated.txt"), []byte("other\n"), 0o644)
	if err := scg.Commit(ctx, "unrelated follow-up merge"); err != nil {
		t.Fatalf("commit follow-up: %v", err)
	}
	if err := scg.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("push tracked main past the implementer commit: %v", err)
	}
	mainTip, err := scg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse pushed main tip: %v", err)
	}

	// gitlink left at the pre-merge recorded pointer; PLAN carries the DURABLE
	// review=<implSHA> record (what recordReviewedCommit stamps at NEEDS-REVIEW).
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+implSHA+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed pre-merge gitlink: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=1 deps= review="+implSHA+" -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// Precondition: bee-R1 must NOT exist on origin (reclaimed).
	if originHasBranch(t, origin, "bee-R1") {
		t.Fatal("precondition: bee-R1 must be absent on origin for the branch-gone case")
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview, Attempts: 1, ReviewCommit: implSHA}}

	r := &Runner{Repo: rp, Git: g, Client: &refusingClient{t: t}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a durably-recorded already-merged task should finalize at dispatch even with the branch gone, got %+v", res)
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("R1")
	if tk == nil {
		t.Fatal("task R1 vanished from PLAN.md")
	}
	if tk.Status != plan.Done {
		t.Fatalf("status = %s, want DONE (finalized from the durable record)", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("finalize must release the claim")
	}
	joined := strings.Join(tk.Body, "\n")
	if !contains(joined, "recorded reviewed commit") || !contains(joined, mainTip) {
		t.Fatalf("plan body missing durable-record finalize note naming the merged tip %s:\n%s", mainTip, joined)
	}
	out, err := g.Run(ctx, "ls-files", "-s", "submodules/sm/repo")
	if err != nil {
		t.Fatalf("ls-files gitlink: %v", err)
	}
	if !contains(out, mainTip) {
		t.Fatalf("gitlink = %q, want bumped to tracked-main tip %s", out, mainTip)
	}
}

// TestReviewDispatchDoesNotFinalizeByRecordWhenNotMerged proves the durable-record
// guard is conservative: a review=<sha> whose commit is NOT an ancestor of tracked
// main (genuinely pending/rejected work) must NOT be finalized — it falls through
// to the normal guards.
func TestReviewDispatchDoesNotFinalizeByRecordWhenNotMerged(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)

	// A commit that exists but was NEVER merged into tracked main (a diverging
	// branch tip), pushed as bee-R1 so it is resolvable but not an ancestor.
	sc := filepath.Join(t.TempDir(), "push")
	if _, err := g.Run(ctx, "clone", "-q", origin, sc); err != nil {
		t.Fatalf("scratch clone: %v", err)
	}
	scg := gitConfig(t, sc)
	os.WriteFile(filepath.Join(sc, "feature.txt"), []byte("work\n"), 0o644)
	if err := scg.Commit(ctx, "feat: pending work"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	pendingSHA, err := scg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if _, err := scg.Run(ctx, "push", "origin", "HEAD:refs/heads/bee-R1"); err != nil {
		t.Fatalf("push bee-R1: %v", err)
	}

	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+pendingSHA+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed gitlink: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= review="+pendingSHA+" -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview, ReviewCommit: pendingSHA}}

	// A genuinely-pending reachable commit must dispatch a NORMAL review, not be
	// auto-finalized. The session approves it the normal way (flips DONE on turn 1).
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte("## R1 [DONE] <!-- attempts=0 deps= review="+pendingSHA+" -->\nreview\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	if _, err := r.Run(ctx, sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if cl.sess.prompts == 0 {
		t.Fatal("a not-yet-merged recorded commit must dispatch a REAL review, not a zero-turn record-finalize")
	}
	b, _ := os.ReadFile(planPath)
	p, _ := plan.Parse(string(b))
	tk := p.Find("R1")
	if tk == nil {
		t.Fatal("task R1 vanished")
	}
	if joined := strings.Join(tk.Body, "\n"); contains(joined, "recorded reviewed commit") {
		t.Fatalf("task was wrongly record-finalized instead of reviewed:\n%s", joined)
	}
}

func TestReviewDispatchFinalizesAlreadyMergedLocalSharing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	sg := gitInit(t, repoDir) // a real checkout, deliberately NO remote (local-sharing)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("base\n"), 0o644)
	if err := sg.Commit(ctx, "base"); err != nil {
		t.Fatalf("submodule base commit: %v", err)
	}
	// The prior (interrupted) review already merged bee-R1 directly into the
	// local main branch (no push needed: this IS the tracked main, in place) —
	// a fast-forward, so the implementer commit and the tracked tip coincide.
	os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("work\n"), 0o644)
	if err := sg.Commit(ctx, "feat: work"); err != nil {
		t.Fatalf("commit implementer work: %v", err)
	}
	implSHA, err := sg.RevParse(ctx, "main")
	if err != nil {
		t.Fatalf("rev-parse main: %v", err)
	}
	// The implementer's source branch is a real local ref too — a genuine
	// interrupted review always has one behind it (a Work pass's own worktree
	// branch, per WorktreeAdd), left in place because reclaimSourceBranch only
	// deletes it once the task actually reaches DONE, which never happened
	// here. Sitting at the same commit as tracked main's tip is exactly what
	// a fast-forward merge leaves behind — the merged-guard-branch-gate fix
	// must finalize on this shape (a real, if now-redundant, ref), never
	// mistake it for the branch having never existed.
	if _, err := sg.Run(ctx, "branch", "bee-R1", implSHA); err != nil {
		t.Fatalf("create bee-R1 local ref: %v", err)
	}

	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+implSHA+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed gitlink: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}

	r := &Runner{Repo: rp, Git: g, Client: &refusingClient{t: t}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("an already-merged task should finalize at dispatch time even with no remote, got %+v", res)
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("R1")
	if tk == nil || tk.Status != plan.Done {
		t.Fatalf("status = %+v, want DONE", tk)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("finalize must release the claim")
	}
	joined := strings.Join(tk.Body, "\n")
	if !contains(joined, "already merged into tracked main") {
		t.Fatalf("plan body missing finalize note:\n%s", joined)
	}
	out, err := g.Run(ctx, "ls-files", "-s", "submodules/sm/repo")
	if err != nil {
		t.Fatalf("ls-files gitlink: %v", err)
	}
	if !contains(out, implSHA) {
		t.Fatalf("gitlink = %q, want bumped to (fast-forward) tip %s", out, implSHA)
	}
}

// TestReviewDispatchDoesNotFinalizeWithoutSourceBranchRemote is the
// merged-guard-branch-gate regression (session-audit-007 Finding #1): a
// ZERO-code-diff work pass (every session-audit-NNN task, by design — a
// diagnose-only pass never bumps the gitlink) leaves the recorded submodule
// pointer IDENTICAL to the submodule's tracked origin/main tip — trivially an
// "ancestor" of it, the exact triviality `git merge-base --is-ancestor A B`
// reports true for whenever A==B — yet bee-R1 (this task's own would-be
// implementer branch) was never created anywhere: no local ref, no remote
// ref, since there was no code change to commit or push. Before
// sourceBranchExists gated finalizeIfAlreadyMerged on that branch actually
// being real, this trivial self-ancestry alone was read as proof of a prior
// interrupted review and auto-DONE'd with ZERO agent turns — the live
// session-audit-006 defect this task fixes (confirmed firing in production:
// claimed WORK, flipped NEEDS-REVIEW, then finalized DONE 2 seconds into its
// REVIEW claim with no session transcript ever recorded). Must now fall
// through to a completely normal review dispatch instead (a session opens and
// completes the task exactly as mockClient drives it).
func TestReviewDispatchDoesNotFinalizeWithoutSourceBranchRemote(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)
	rg := git.New(repoDir)
	mainTip, err := rg.RevParse(ctx, "main")
	if err != nil {
		t.Fatalf("rev-parse submodule main: %v", err)
	}

	// The gitlink records the CURRENT tracked tip verbatim — a diagnose-only
	// work pass's gitlink is never bumped — and bee-R1 (this task's would-be
	// implementer branch) is never created anywhere: no local ref, no remote
	// ref, matching the zero-diff shape exactly.
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+mainTip+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed unmoved gitlink: %v", err)
	}
	// A zero-diff work pass still writes its change doc per protocol — this is
	// what distinguishes it from needs-review-auto-recover-lost-work's "truly
	// lost" shape (no branch, no doc): a doc's presence is one of that guard's
	// four required-absent signals, so recoverIfLost must fall through here.
	os.WriteFile(filepath.Join(sm, "docs", "bee-R1-R1.md"), []byte("# R1\nzero-diff\n"), 0o644)
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md", "submodules/sm/docs"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte("## R1 [DONE] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a normal review should complete, got %+v", res)
	}
	if cl.sess.prompts == 0 {
		t.Fatal("a task whose source branch never existed must dispatch a REAL review session, not a zero-turn auto-finalize")
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("R1")
	if tk == nil || tk.Status != plan.Done {
		t.Fatalf("status = %+v, want DONE (via normal review, not auto-finalize)", tk)
	}
	joined := strings.Join(tk.Body, "\n")
	if contains(joined, "already merged into tracked") {
		t.Fatalf("task was wrongly runner-finalized instead of reviewed: body:\n%s", joined)
	}
}

// TestReviewDispatchDoesNotFinalizeWithoutSourceBranchLocalSharing mirrors
// TestReviewDispatchDoesNotFinalizeWithoutSourceBranchRemote in local-sharing
// mode (a submodule checkout with NO remote at all): the recorded pointer is
// identical to the LOCAL tracked main tip (the zero-diff shape) and bee-R1 was
// never created as a local ref either — unlike
// TestReviewDispatchFinalizesAlreadyMergedLocalSharing's genuine (if now
// redundant) bee-R1 ref sitting at that same commit. Must dispatch a
// completely normal review, never a zero-turn auto-finalize.
func TestReviewDispatchDoesNotFinalizeWithoutSourceBranchLocalSharing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	sg := gitInit(t, repoDir) // a real checkout, deliberately NO remote (local-sharing)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("base\n"), 0o644)
	if err := sg.Commit(ctx, "base"); err != nil {
		t.Fatalf("submodule base commit: %v", err)
	}
	mainTip, err := sg.RevParse(ctx, "main")
	if err != nil {
		t.Fatalf("rev-parse main: %v", err)
	}
	// No bee-R1 ref anywhere — the zero-diff shape: a diagnose-only work pass
	// never created (let alone bumped) an implementer branch at all.

	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+mainTip+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed unmoved gitlink: %v", err)
	}
	// A zero-diff work pass still writes its change doc per protocol — this is
	// what distinguishes it from needs-review-auto-recover-lost-work's "truly
	// lost" shape (no branch, no doc): a doc's presence is one of that guard's
	// four required-absent signals, so recoverIfLost must fall through here.
	os.WriteFile(filepath.Join(sm, "docs", "bee-R1-R1.md"), []byte("# R1\nzero-diff\n"), 0o644)
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md", "submodules/sm/docs"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte("## R1 [DONE] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a normal review should complete, got %+v", res)
	}
	if cl.sess.prompts == 0 {
		t.Fatal("a task whose source branch never existed must dispatch a REAL review session, not a zero-turn auto-finalize")
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("R1")
	if tk == nil || tk.Status != plan.Done {
		t.Fatalf("status = %+v, want DONE (via normal review, not auto-finalize)", tk)
	}
	joined := strings.Join(tk.Body, "\n")
	if contains(joined, "already merged into tracked") {
		t.Fatalf("task was wrongly runner-finalized instead of reviewed: body:\n%s", joined)
	}
}

// TestReviewDispatchDoesNotFinalizeOnAmbientPointerAncestryRemote is the
// review-finalize-branch-ancestor-gap regression (ui-audit-008): unlike
// TestReviewDispatchDoesNotFinalizeWithoutSourceBranchRemote (where bee-R1
// never existed at all), here bee-R1 is a REAL, resolvable pushed branch —
// sourceBranchExists correctly reports it exists — but its OWN tip was NEVER
// folded into tracked main (a genuinely unmerged, in-flight implementer
// commit). Meanwhile the beehive-layer's ambient recorded submodule pointer
// (whatever some OTHER, already-finalized task's Work pass most recently
// bumped the single shared gitlink path to) is deliberately seeded at
// tracked main's OWN current tip — trivially "an ancestor" of itself, the
// exact triviality that must never be mistaken for "bee-R1 was merged". Pre-
// fix, finalizeIfAlreadyMerged tested IsAncestor against this ambient pointer
// instead of bee-R1's own tip and would wrongly auto-finalize DONE with zero
// agent turns; the fix must test bee-R1's own (fetched) tip, find it NOT an
// ancestor, and dispatch a completely normal review instead.
func TestReviewDispatchDoesNotFinalizeOnAmbientPointerAncestryRemote(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)
	rg := git.New(repoDir)

	// bee-R1: a REAL branch, pushed to origin, whose own tip is a genuinely
	// UNMERGED commit — diverged from tracked main, never folded in.
	if _, err := rg.Run(ctx, "checkout", "-q", "-b", "bee-R1"); err != nil {
		t.Fatalf("checkout bee-R1: %v", err)
	}
	os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("unmerged work\n"), 0o644)
	if err := rg.Commit(ctx, "feat: unmerged work"); err != nil {
		t.Fatalf("commit bee-R1 work: %v", err)
	}
	implSHA, err := rg.RevParse(ctx, "bee-R1")
	if err != nil {
		t.Fatalf("rev-parse bee-R1: %v", err)
	}
	if _, err := rg.Run(ctx, "push", "origin", "HEAD:refs/heads/bee-R1"); err != nil {
		t.Fatalf("push bee-R1: %v", err)
	}
	if _, err := rg.Run(ctx, "checkout", "-q", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	// Tracked main advances past bee-R1 entirely (an unrelated, already-
	// finalized task's follow-up commit) — bee-R1's commit is NOT its ancestor.
	sc := filepath.Join(t.TempDir(), "push")
	if _, err := g.Run(ctx, "clone", "-q", origin, sc); err != nil {
		t.Fatalf("scratch clone: %v", err)
	}
	scg := gitConfig(t, sc)
	os.WriteFile(filepath.Join(sc, "unrelated.txt"), []byte("other\n"), 0o644)
	if err := scg.Commit(ctx, "unrelated already-finalized task"); err != nil {
		t.Fatalf("commit unrelated follow-up: %v", err)
	}
	if err := scg.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("push tracked main past bee-R1: %v", err)
	}
	mainTip, err := scg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse pushed main tip: %v", err)
	}

	// The ambient beehive-layer gitlink is left at tracked main's OWN current
	// tip — e.g. some OTHER task's Work-pass completion most recently bumped
	// this shared gitlink path — trivially an ancestor of itself, but utterly
	// unrelated to whether bee-R1 (THIS task's implementer branch) was ever
	// merged.
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+mainTip+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed ambient gitlink at tracked tip: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte("## R1 [DONE] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a normal review should complete, got %+v", res)
	}
	if cl.sess.prompts == 0 {
		t.Fatal("bee-R1's own tip is not an ancestor of tracked main (only the unrelated ambient pointer trivially is): a real review must dispatch, not auto-finalize")
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("R1")
	if tk == nil || tk.Status != plan.Done {
		t.Fatalf("status = %+v, want DONE (via normal review, not auto-finalize)", tk)
	}
	joined := strings.Join(tk.Body, "\n")
	if contains(joined, "already merged into tracked") {
		t.Fatalf("task was wrongly runner-finalized on the ambient pointer's trivial self-ancestry instead of reviewed: body:\n%s", joined)
	}
	if contains(joined, implSHA) && contains(joined, "already merged") {
		t.Fatalf("plan body wrongly claims bee-R1's commit %s was already merged: body:\n%s", implSHA, joined)
	}
}

// TestReviewDispatchDoesNotFinalizeOnAmbientPointerAncestryLocalSharing mirrors
// TestReviewDispatchDoesNotFinalizeOnAmbientPointerAncestryRemote in
// local-sharing mode (a submodule checkout with NO remote at all): bee-R1 is a
// real LOCAL ref whose own tip is genuinely unmerged, while the ambient
// beehive-layer gitlink is left at local tracked main's own (later-advanced)
// tip — trivially an ancestor of itself. Must dispatch a completely normal
// review, never a zero-turn auto-finalize.
func TestReviewDispatchDoesNotFinalizeOnAmbientPointerAncestryLocalSharing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	sg := gitInit(t, repoDir) // a real checkout, deliberately NO remote (local-sharing)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("base\n"), 0o644)
	if err := sg.Commit(ctx, "base"); err != nil {
		t.Fatalf("submodule base commit: %v", err)
	}

	// bee-R1: a real local ref whose own tip diverges from main and is never
	// merged into it.
	if _, err := sg.Run(ctx, "checkout", "-q", "-b", "bee-R1"); err != nil {
		t.Fatalf("checkout bee-R1: %v", err)
	}
	os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("unmerged work\n"), 0o644)
	if err := sg.Commit(ctx, "feat: unmerged work"); err != nil {
		t.Fatalf("commit bee-R1 work: %v", err)
	}
	implSHA, err := sg.RevParse(ctx, "bee-R1")
	if err != nil {
		t.Fatalf("rev-parse bee-R1: %v", err)
	}
	if _, err := sg.Run(ctx, "checkout", "-q", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	// Local main advances past bee-R1 with an unrelated (already-finalized)
	// commit of its own — bee-R1's commit is NOT its ancestor.
	os.WriteFile(filepath.Join(repoDir, "unrelated.txt"), []byte("other\n"), 0o644)
	if err := sg.Commit(ctx, "unrelated already-finalized task"); err != nil {
		t.Fatalf("commit unrelated follow-up: %v", err)
	}
	mainTip, err := sg.RevParse(ctx, "main")
	if err != nil {
		t.Fatalf("rev-parse main: %v", err)
	}

	// The ambient beehive-layer gitlink is left at local main's OWN current
	// tip — trivially an ancestor of itself, unrelated to bee-R1.
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+mainTip+",submodules/sm/repo"); err != nil {
		t.Fatalf("seed ambient gitlink at tracked tip: %v", err)
	}
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	if _, err := g.Run(ctx, "add", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("add plan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte("## R1 [DONE] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a normal review should complete, got %+v", res)
	}
	if cl.sess.prompts == 0 {
		t.Fatal("bee-R1's own tip is not an ancestor of local main (only the unrelated ambient pointer trivially is): a real review must dispatch, not auto-finalize")
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("R1")
	if tk == nil || tk.Status != plan.Done {
		t.Fatalf("status = %+v, want DONE (via normal review, not auto-finalize)", tk)
	}
	joined := strings.Join(tk.Body, "\n")
	if contains(joined, "already merged into tracked") {
		t.Fatalf("task was wrongly runner-finalized on the ambient pointer's trivial self-ancestry instead of reviewed: body:\n%s", joined)
	}
	if contains(joined, implSHA) && contains(joined, "already merged") {
		t.Fatalf("plan body wrongly claims bee-R1's commit %s was already merged: body:\n%s", implSHA, joined)
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

// idleClient's Prompt returns ErrTurnIdle, modeling the progress watchdog firing
// on a turn that made no headway (the opencode client's IdleTimeout path).
type idleClient struct{}

func (idleClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	return idleSession{}, nil
}

type idleSession struct{}

func (idleSession) Prompt(ctx context.Context, text string) (string, error) {
	return "", ErrTurnIdle
}
func (idleSession) Messages(ctx context.Context) ([]Message, error) { return nil, nil }
func (idleSession) Close() error                                    { return nil }

// TestIdleTurnAbandonsForGC proves the runner treats a watchdog ErrTurnIdle like a
// stalled agent: it abandons the task for GC (non-fatal) with a warning naming the
// idle timeout, distinct from the absolute per-turn ceiling.
func TestIdleTurnAbandonsForGC(t *testing.T) {
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
		Repo: rp, Git: g, Client: idleClient{}, MaxTurns: 5,
		WallCap: time.Hour, TTL: time.Hour,
		TurnTimeout: time.Hour, TurnIdleTimeout: 15 * time.Minute,
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("an idle-watchdog abandon must not be fatal, got: %v", err)
	}
	if !res.GCMarked {
		t.Fatalf("want GCMarked on an idle-abandoned turn, got %+v", res)
	}
	if !contains(res.Warning, "idle timeout") {
		t.Fatalf("warning should name the idle timeout, got %q", res.Warning)
	}
}

// fixedClient hands out a caller-supplied Session so a test can drive exact
// Prompt/Abort behavior across turns.
type fixedClient struct{ sess Session }

func (c fixedClient) Open(ctx context.Context, cwd, system string) (Session, error) {
	return c.sess, nil
}

// retryIdleSession always idles and counts Prompt/Abort calls; it implements the
// optional aborter capability so the runner's in-place idle recovery exercises the
// server-side abort before each retry.
type retryIdleSession struct {
	prompts int
	aborts  int
}

func (s *retryIdleSession) Prompt(ctx context.Context, text string) (string, error) {
	s.prompts++
	return "", ErrTurnIdle
}
func (s *retryIdleSession) Messages(ctx context.Context) ([]Message, error) { return nil, nil }
func (s *retryIdleSession) Close() error                                    { return nil }
func (s *retryIdleSession) Abort(ctx context.Context) error                 { s.aborts++; return nil }

// TestIdleRetryBoundThenAbandons proves that with a retry budget the runner
// recovers in-place from an idle stall — aborting the wedged turn and re-driving
// the SAME session — and only abandons for GC once the budget is exhausted. With
// TurnIdleRetries=2 an always-idle session is prompted 3 times (1 + 2 retries),
// aborted twice, and finally abandoned with the idle-timeout warning.
func TestIdleRetryBoundThenAbandons(t *testing.T) {
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

	sess := &retryIdleSession{}
	r := &Runner{
		Repo: rp, Git: g, Client: fixedClient{sess: sess}, MaxTurns: 5,
		WallCap: time.Hour, TTL: time.Hour,
		TurnTimeout: time.Hour, TurnIdleTimeout: 15 * time.Minute, TurnIdleRetries: 2,
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("idle-retry abandon must not be fatal, got: %v", err)
	}
	if sess.prompts != 3 {
		t.Fatalf("want 3 prompts (1 + 2 retries), got %d", sess.prompts)
	}
	if sess.aborts != 2 {
		t.Fatalf("want 2 server-side aborts (one per retry), got %d", sess.aborts)
	}
	if !res.GCMarked {
		t.Fatalf("want GCMarked once the retry budget is exhausted, got %+v", res)
	}
	if !contains(res.Warning, "idle timeout") {
		t.Fatalf("warning should name the idle timeout, got %q", res.Warning)
	}
}

// recoverIdleSession idles on its first turn, then on the second writes the
// bootstrap PLAN.md and succeeds — modeling a transient upstream hang that clears
// on retry.
type recoverIdleSession struct {
	prompts  int
	aborts   int
	planPath string
}

func (s *recoverIdleSession) Prompt(ctx context.Context, text string) (string, error) {
	s.prompts++
	if s.prompts == 1 {
		return "", ErrTurnIdle
	}
	os.WriteFile(s.planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	return "", nil
}
func (s *recoverIdleSession) Messages(ctx context.Context) ([]Message, error) { return nil, nil }
func (s *recoverIdleSession) Close() error                                    { return nil }
func (s *recoverIdleSession) Abort(ctx context.Context) error                 { s.aborts++; return nil }

// TestIdleRetryRecoversInPlace proves a single transient idle stall no longer
// throws the pass away: after one abort+retry the re-driven session completes the
// task, so the pass reports Completed (not GCMarked) and aborted exactly once.
func TestIdleRetryRecoversInPlace(t *testing.T) {
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

	sess := &recoverIdleSession{planPath: filepath.Join(sm, "PLAN.md")}
	r := &Runner{
		Repo: rp, Git: g, Client: fixedClient{sess: sess}, MaxTurns: 5,
		WallCap: time.Hour, TTL: time.Hour,
		TurnTimeout: time.Hour, TurnIdleTimeout: 15 * time.Minute, TurnIdleRetries: 2,
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.GCMarked {
		t.Fatalf("a recovered idle stall must not GC-abandon, got %+v", res)
	}
	if sess.aborts != 1 {
		t.Fatalf("want exactly one abort (single stall), got %d", sess.aborts)
	}
	if !res.Completed {
		t.Fatalf("want Completed after in-place recovery, got %+v", res)
	}
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
	if !contains(res.Warning, "per-turn ceiling") {
		t.Fatalf("warning should name the per-turn ceiling, got %q", res.Warning)
	}
}

// TestRunnerConciseActivityAlwaysOn proves the runner streams concise per-turn
// activity to its always-on Concise sink with NO --debug (Debug nil): the pass
// kind at session open, each turn boundary, and the abandon/GC reason. This is the
// journal-activity-stream contract at the runner layer (the recorder's tool-call
// stream is covered by TestRecorderConciseStreamsWithoutDebug). It fails before the
// split, where these lines were gated behind Debug (the --debug flag) and a
// scheduled pass was silent.
func TestRunnerConciseActivityAlwaysOn(t *testing.T) {
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

	var concise strings.Builder
	r := &Runner{
		Repo: rp, Git: g, Client: idleClient{}, MaxTurns: 5,
		WallCap: time.Hour, TTL: time.Hour,
		TurnTimeout: time.Hour, TurnIdleTimeout: 15 * time.Minute,
		Concise: &concise, // Debug intentionally nil: a scheduled pass has no --debug
	}
	if _, err := r.Run(ctx, sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := concise.String()
	if !contains(got, "kind=bootstrap") || !contains(got, "opening session") {
		t.Fatalf("concise stream missing the always-on pass-kind line; got:\n%s", got)
	}
	if !contains(got, "turn 1/5") {
		t.Fatalf("concise stream missing an always-on turn boundary; got:\n%s", got)
	}
	if !contains(got, "idle timeout") {
		t.Fatalf("concise stream missing the always-on abandon/GC reason; got:\n%s", got)
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

// TestRunPublishFailureRecordsDurableTranscriptWarning is the publish-fail-
// durable-warning regression: finish() flushes+streams the "final" transcript
// BEFORE attempting r.publishWithResolution, so a failure discovered only there
// (the primary Work/Review/Arbitrate done path, ~swarm.go:1226) used to leave
// ZERO trace in the session transcript — the file was byte-for-byte
// indistinguishable from an honest first-try success, even though res.Warning
// carried the real reason (which reached only cmd/honeybee/main.go's stderr,
// never the repository). The fix must durably append a trailing "## ⚠️
// warning" block carrying that same text, so internal/audit.ParseFile — which
// keys Aborted/CompletionMiss exclusively off that block — reports the pass as
// aborted instead of silently indistinguishable from success.
func TestRunPublishFailureRecordsDurableTranscriptWarning(t *testing.T) {
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

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
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
	if res.Completed || !res.GCMarked {
		t.Fatalf("sanity: want GC-marked, not completed, got %+v", res)
	}
	transcriptPath := filepath.Join(sm, "sessions", res.SessionID+".md")
	body, rerr := os.ReadFile(transcriptPath)
	if rerr != nil {
		t.Fatalf("read transcript: %v", rerr)
	}
	if !strings.Contains(string(body), "## \u26a0\ufe0f warning") {
		t.Fatalf("regression: transcript carries no trailing warning block despite the publish failure; got:\n%s", body)
	}
	if !strings.Contains(string(body), "publishing to main failed") {
		t.Fatalf("warning block must carry the real failure text, got:\n%s", body)
	}
	s, aerr := audit.ParseFile(transcriptPath)
	if aerr != nil {
		t.Fatalf("audit.ParseFile: %v", aerr)
	}
	if !s.Heuristics.Aborted {
		t.Fatalf("audit must classify this transcript as Aborted, got %+v", s.Heuristics)
	}
	if !s.Heuristics.CompletionMiss {
		t.Fatalf("a Work-kind abort must set CompletionMiss, got %+v", s.Heuristics)
	}
	if !strings.Contains(s.Heuristics.AbortReason, "publishing to main failed") {
		t.Fatalf("AbortReason must carry the real failure text, got %q", s.Heuristics.AbortReason)
	}
}

// TestRunClaimResolvedPublishFailureRecordsDurableTranscriptWarning is the
// publish-fail-durable-warning regression for the claim-resolved-mid-turn call
// site (~swarm.go:1062): the task is ALREADY terminal (NEEDS-REVIEW, with its
// doc present) the very first time this pass heartbeats — e.g. a peer or a
// prior attempt resolved it between selection and this pass's first turn — so
// the ErrResolved branch's own finish()+publish attempt runs before any prompt.
// A publish failure discovered there must be just as durably recorded as the
// primary done path's.
func TestRunClaimResolvedPublishFailureRecordsDurableTranscriptWarning(t *testing.T) {
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
	// The task is ALREADY resolved to NEEDS-REVIEW (with its doc present) before
	// Run() ever starts, while the selection still carries the TODO status this
	// pass was dispatched with — so the very first heartbeat (turn 1, before any
	// prompt) sees the mismatch and returns claim.ErrResolved.
	os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
	g.Commit(context.Background(), "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	r := &Runner{
		Repo: rp, Git: g, Client: &mockClient{sess: &mockSession{}}, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		Publish: func(ctx context.Context) error { return errors.New("publish boom") },
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed || !res.GCMarked {
		t.Fatalf("sanity: want GC-marked, not completed, got %+v", res)
	}
	transcriptPath := filepath.Join(sm, "sessions", res.SessionID+".md")
	s, aerr := audit.ParseFile(transcriptPath)
	if aerr != nil {
		t.Fatalf("audit.ParseFile: %v", aerr)
	}
	if !s.Heuristics.Aborted || !s.Heuristics.CompletionMiss {
		t.Fatalf("want Aborted+CompletionMiss on the claim-resolved publish-failure path, got %+v", s.Heuristics)
	}
	if !strings.Contains(s.Heuristics.AbortReason, "publishing to main failed") {
		t.Fatalf("AbortReason must carry the real failure text, got %q", s.Heuristics.AbortReason)
	}
}

// TestRunNoOpPublishRecordsDurableTranscriptWarning is the publish-fail-
// durable-warning regression for the mainAdvanced re-verification call site
// (~swarm.go:1259, Bootstrap/Reconcile only): the agent leaves PLAN.md
// uncommitted in its own worktree, so the local completion check is green but
// the publish carries nothing and mainAdvanced reports main did NOT advance —
// discovered AFTER finish() already ran successfully, so this is the "finish
// itself never failed, only the post-finish re-verification did" shape.
func TestRunNoOpPublishRecordsDurableTranscriptWarning(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\nbuild it\n"), 0o644)
	g.Commit(context.Background(), "seed roi")

	ctx := context.Background()
	wtPath := filepath.Join(root, ".worktrees", "bee-noop")
	if err := g.WorktreeAdd(ctx, wtPath, "bee-noop", "main"); err != nil {
		t.Fatal(err)
	}
	wrp, _ := repo.Open(wtPath)
	wg := git.New(wtPath)
	subs, _ := wrp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Bootstrap, Submodule: subs[0]}

	cl := &scriptClient{}
	cl.onPrompt = func() {
		// Written but deliberately never committed: the local os.Stat check goes
		// green while the branch merge to main carries nothing.
		os.WriteFile(subs[0].PlanPath(), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}
	r := &Runner{
		Repo: wrp, Git: wg, Client: cl, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour,
		Publish: func(ctx context.Context) error { return wg.PublishToMain(ctx, "") },
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed || !res.GCMarked {
		t.Fatalf("sanity: want GC-marked, not completed, got %+v", res)
	}
	if !contains(res.Warning, "did not advance") {
		t.Fatalf("warning should name the no-op publish, got %q", res.Warning)
	}
	transcriptPath := filepath.Join(wtPath, "submodules", "sm", "sessions", res.SessionID+".md")
	s, aerr := audit.ParseFile(transcriptPath)
	if aerr != nil {
		t.Fatalf("audit.ParseFile: %v", aerr)
	}
	if !s.Heuristics.Aborted {
		t.Fatalf("want Aborted on the no-op-publish path, got %+v", s.Heuristics)
	}
	if !strings.Contains(s.Heuristics.AbortReason, "did not advance") {
		t.Fatalf("AbortReason must carry the real failure text, got %q", s.Heuristics.AbortReason)
	}
}

// TestRunSuccessfulPublishLeavesNoTranscriptWarning is the no-false-positives
// twin of the three durable-warning regressions above: an honest, uneventful
// completion must NOT grow a warning block just because the machinery that
// could append one now exists on the completion path.
func TestRunSuccessfulPublishLeavesNoTranscriptWarning(t *testing.T) {
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

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed || res.GCMarked {
		t.Fatalf("sanity: want a clean completion, got %+v", res)
	}
	transcriptPath := filepath.Join(sm, "sessions", res.SessionID+".md")
	body, rerr := os.ReadFile(transcriptPath)
	if rerr != nil {
		t.Fatalf("read transcript: %v", rerr)
	}
	if strings.Contains(string(body), "## \u26a0\ufe0f warning") {
		t.Fatalf("false positive: a clean completion must not carry a warning block, got:\n%s", body)
	}
	s, aerr := audit.ParseFile(transcriptPath)
	if aerr != nil {
		t.Fatalf("audit.ParseFile: %v", aerr)
	}
	if s.Heuristics.Aborted || s.Heuristics.CompletionMiss {
		t.Fatalf("false positive: audit must not classify a clean completion as aborted, got %+v", s.Heuristics)
	}
}

// TestRunDetectsLostClaimRaceBeforePublish is the claim-ttl-wallcap-race-guard
// regression (session-audit-013 F1, the "clean self-reported delivery, then a
// fresh redispatch of the identical task" pattern confirmed three times in one
// window). It reproduces the exact gap: this pass's own turn reaches LOCAL
// completion (status flipped NEEDS-REVIEW + doc written, committed to its own
// not-yet-published beehive worktree branch) at the precise moment a PEER
// session has ALREADY claimed the identical task on PUBLISHED main — the shape
// a heartbeat that only refreshes at the top of a turn lets happen when that
// turn's own duration (or, in production, finish()'s own conflict-retry
// sub-turns) runs long enough. Before the guard, the runner trusted the local
// "done" unconditionally and published straight over the peer. It must instead
// detect the loss (Lost=true), durably warn the transcript (mirroring the
// established publish-fail-durable-warning precedent), never report Completed,
// and never touch the peer's claim already sitting on main.
func TestRunDetectsLostClaimRaceBeforePublish(t *testing.T) {
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
	// A real .gitmodules entry (url = the local repoDir) so that the SEPARATE
	// beehive worktree created below (unlike every other Work-kind test in this
	// file, which runs directly at root and so never needs one) can successfully
	// `git submodule update --init` its own copy of submodules/sm/repo.
	gm := "[submodule \"sm\"]\n\tpath = submodules/sm/repo\n\turl = " + repoDir + "\n"
	os.WriteFile(filepath.Join(root, ".gitmodules"), []byte(gm), 0o644)
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	g.Commit(context.Background(), "seed")

	ctx := context.Background()
	// This honeybee's OWN isolated beehive-layer worktree, exactly as
	// cmd/honeybee creates it — physically separate from "main" (root/g), so the
	// test can commit a competing peer claim directly to main untouched by
	// anything this worktree does.
	wtPath := filepath.Join(root, ".worktrees", "bee-A")
	if err := g.WorktreeAdd(ctx, wtPath, "bee-A", "main"); err != nil {
		t.Fatal(err)
	}
	wrp, _ := repo.Open(wtPath)
	wg := git.New(wtPath)
	subs, _ := wrp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	cl := &scriptClient{}
	cl.onPrompt = func() {
		// A PEER session (bee-B) claims the SAME task and lands it directly on
		// published main — simulating the exact gap a stale, unrefreshed heartbeat
		// opens while this pass's turn is (conceptually) still in flight.
		peerPlan := filepath.Join(root, "submodules", "sm", "PLAN.md")
		if err := os.WriteFile(peerPlan, []byte(
			"## T1 [TODO] <!-- attempts=0 deps= session=bee-B heartbeat="+
				time.Now().UTC().Format(time.RFC3339)+" -->\ngo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := g.Commit(ctx, "peer bee-B claims T1"); err != nil {
			t.Fatal(err)
		}

		// This pass's own agent, unaware of the peer, finishes its turn: it flips
		// the task NEEDS-REVIEW and writes the doc, entirely within its own
		// not-yet-published worktree branch. git never tracks the empty seeded
		// docs/ dir, so this worktree checkout does not have it on disk yet.
		os.WriteFile(subs[0].PlanPath(), []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		os.MkdirAll(filepath.Join(subs[0].Path, "docs"), 0o755)
		os.WriteFile(filepath.Join(subs[0].Path, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		if err := wg.CommitPaths(ctx, "plan: needs-review T1",
			"submodules/sm/PLAN.md", "submodules/sm/docs/bee-T1-T1.md"); err != nil {
			t.Fatal(err)
		}
	}
	r := &Runner{
		Repo: wrp, Git: wg, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour,
		Session: "bee-A",
		Publish: func(ctx context.Context) error { return wg.PublishToMain(ctx, "") },
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed {
		t.Fatalf("must NOT report Completed: a peer already owns this task, got %+v", res)
	}
	if !res.Lost {
		t.Fatalf("want Lost=true (the race must be DETECTED, not silently completed), got %+v", res)
	}
	if !contains(res.Warning, "another session already claimed it") {
		t.Fatalf("warning must name the lost claim race, got %q", res.Warning)
	}
	// Main must still show the PEER's claim untouched, and must NOT carry our
	// lost pass's status flip — the whole point of the guard.
	body, err := g.Show(ctx, "main", "submodules/sm/PLAN.md")
	if err != nil {
		t.Fatalf("show main PLAN.md: %v", err)
	}
	if !strings.Contains(body, "session=bee-B") {
		t.Fatalf("peer's claim must survive untouched on main, got:\n%s", body)
	}
	if strings.Contains(body, "NEEDS-REVIEW") {
		t.Fatalf("our lost pass's status flip must NOT have reached main, got:\n%s", body)
	}
	// The loss must be durably recorded in the transcript, exactly like the
	// established publish-fail-durable-warning precedent (recordPublishFailureWarning).
	transcriptPath := filepath.Join(wtPath, "submodules", "sm", "sessions", res.SessionID+".md")
	s, aerr := audit.ParseFile(transcriptPath)
	if aerr != nil {
		t.Fatalf("audit.ParseFile: %v", aerr)
	}
	if !s.Heuristics.Aborted {
		t.Fatalf("want the transcript classified Aborted, got %+v", s.Heuristics)
	}
}

// TestRacePeerOwnsTaskFailsOpen proves the guard never falsely blocks a clean,
// uncontested completion: no Publish wired (no shared main to race against, the
// same convention mainAdvanced uses), a task absent from main, and a foreign
// but STALE (TTL-expired) claim must all read as "not lost".
func TestRacePeerOwnsTaskFailsOpen(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(sm, 0o755)
	ctx := context.Background()

	t.Run("no-publish-wired", func(t *testing.T) {
		os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [TODO] <!-- attempts=0 deps= session=bee-B heartbeat="+time.Now().UTC().Format(time.RFC3339)+" -->\ngo\n"), 0o644)
		g.Commit(ctx, "seed")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1"}}
		r := &Runner{Repo: rp, Git: g, Session: "bee-A", TTL: time.Hour}
		if r.racePeerOwnsTask(ctx, sel) {
			t.Fatal("must fail open with no Publish wired")
		}
	})

	t.Run("task-absent-from-main", func(t *testing.T) {
		os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## OTHER [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		g.Commit(ctx, "no T1")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1"}}
		r := &Runner{
			Repo: rp, Git: g, Session: "bee-A", TTL: time.Hour,
			Publish: func(context.Context) error { return nil },
		}
		if r.racePeerOwnsTask(ctx, sel) {
			t.Fatal("must fail open when the task is absent from published main")
		}
	})

	t.Run("stale-foreign-claim-not-lost", func(t *testing.T) {
		old := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
		os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [TODO] <!-- attempts=0 deps= session=bee-B heartbeat="+old+" -->\ngo\n"), 0o644)
		g.Commit(ctx, "stale peer claim")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1"}}
		r := &Runner{
			Repo: rp, Git: g, Session: "bee-A", TTL: time.Hour,
			Publish: func(context.Context) error { return nil },
		}
		if r.racePeerOwnsTask(ctx, sel) {
			t.Fatal("a STALE foreign claim (past TTL) must not report lost — it is GC-reclaimable, not a live peer")
		}
	})

	t.Run("own-session-not-lost", func(t *testing.T) {
		os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [TODO] <!-- attempts=0 deps= session=bee-A heartbeat="+time.Now().UTC().Format(time.RFC3339)+" -->\ngo\n"), 0o644)
		g.Commit(ctx, "our own claim")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1"}}
		r := &Runner{
			Repo: rp, Git: g, Session: "bee-A", TTL: time.Hour,
			Publish: func(context.Context) error { return nil },
		}
		if r.racePeerOwnsTask(ctx, sel) {
			t.Fatal("our OWN session's claim must never read as a foreign race")
		}
	})

	t.Run("live-foreign-claim-lost", func(t *testing.T) {
		os.WriteFile(filepath.Join(sm, "PLAN.md"), []byte("## T1 [TODO] <!-- attempts=0 deps= session=bee-B heartbeat="+time.Now().UTC().Format(time.RFC3339)+" -->\ngo\n"), 0o644)
		g.Commit(ctx, "live peer claim")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1"}}
		r := &Runner{
			Repo: rp, Git: g, Session: "bee-A", TTL: time.Hour,
			Publish: func(context.Context) error { return nil },
		}
		if !r.racePeerOwnsTask(ctx, sel) {
			t.Fatal("a LIVE foreign claim (fresh heartbeat, different session) must report lost")
		}
	})
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

// TestWorkPinsPointerToTrackedTipDespiteAgentBeeBump is the enforcement half of
// the submodule-pointer invariant (docs/submodule-pointer-invariant.md): the
// gitlink MUST always equal the tracked-branch tip, NEVER a bee-<taskid> tip.
// Even a misbehaving agent that commits the gitlink at its bee-branch tip (the
// exact defect the old "bump the submodule pointer" instruction produced, which
// dangled the pointer once the bee branch was reclaimed) must not survive: the
// runner re-pins the published pointer to origin/<branch> tip at completion.
func TestWorkPinsPointerToTrackedTipDespiteAgentBeeBump(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	ctx := context.Background()

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
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	wtDir := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
	var beeTip string
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		// Misbehaving agent: make a bee commit in the code worktree, then WRONGLY
		// bump the shared submodule pointer to that bee-tip and commit it in the
		// hive repo — exactly what the retired "bump the submodule pointer"
		// instruction produced.
		wt := git.New(wtDir)
		os.WriteFile(filepath.Join(wtDir, "beework"), []byte("bee"), 0o644)
		if err := wt.Commit(ctx, "bee work"); err != nil {
			t.Errorf("bee commit: %v", err)
			return
		}
		beeTip, _ = wt.RevParse(ctx, "HEAD")
		if _, err := git.New(repoDir).Run(ctx, "checkout", beeTip); err != nil {
			t.Errorf("checkout bee-tip in shared checkout: %v", err)
			return
		}
		if err := g.CommitPaths(ctx, "agent WRONGLY bumped pointer to bee-tip", "submodules/sm/repo"); err != nil {
			t.Errorf("agent bad pointer bump: %v", err)
			return
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
	if beeTip == "" || beeTip == originTip {
		t.Fatalf("bee-tip not distinct from tracked tip (bee=%q tip=%q); test is vacuous", beeTip, originTip)
	}
	// The PUBLISHED gitlink must be the tracked tip, NOT the agent's bee-tip.
	ptr, err := g.Run(ctx, "rev-parse", "HEAD:submodules/sm/repo")
	if err != nil {
		t.Fatalf("read committed gitlink: %v", err)
	}
	if ptr == beeTip {
		t.Fatalf("INVARIANT VIOLATED: committed gitlink is the bee-tip %s; the runner failed to re-pin to the tracked tip", beeTip)
	}
	if ptr != originTip {
		t.Fatalf("committed gitlink %s != tracked tip %s: pinPointerToTrackedTip did not enforce the invariant", ptr, originTip)
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

// worktreeGitlinksInIndex returns every tracked gitlink (mode 160000) under a
// submodules/<sm>/worktrees/ path in g's index. It parses raw `git ls-files -s`
// independently of the production classifier so the assertions below are a true
// check, not a tautology against the code under test.
func worktreeGitlinksInIndex(t *testing.T, g *git.Repo, ctx context.Context) []string {
	t.Helper()
	out, err := g.Run(ctx, "ls-files", "-s")
	if err != nil {
		t.Fatalf("ls-files: %v", err)
	}
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "160000 ") {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		p := line[tab+1:]
		parts := strings.Split(p, "/")
		if len(parts) >= 4 && parts[0] == "submodules" && parts[2] == "worktrees" {
			found = append(found, p)
		}
	}
	return found
}

// TestWorkCommitPathNeverStagesWorktreeGitlink is the producer-side guard: a Work
// run creates its code worktree at submodules/<sm>/worktrees/<branch> (itself a
// checkout) INSIDE the beehive worktree, and the per-turn claim/heartbeat commits
// run while it exists. Those commits must scope to PLAN.md (never `git add -A`),
// so the beehive index must hold NO gitlink under submodules/*/worktrees/* after
// the run. With the old add-all commit this test fails (the worktree leaks in as
// an orphan gitlink); with scoped commits the index stays clean.
func TestWorkCommitPathNeverStagesWorktreeGitlink(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	git.New(repoDir).Commit(ctx, "base")
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	wtDir := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
	var wtExisted bool
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		// The code worktree must physically exist while the heartbeat commits run,
		// so a clean index afterward is a real "never staged", not "never created".
		if fi, err := os.Stat(wtDir); err == nil && fi.IsDir() {
			wtExisted = true
		}
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, Session: "bee-A"}
	if _, err := r.Run(ctx, sel, "sys", "first"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !wtExisted {
		t.Fatal("code worktree was never created during the run; the test cannot prove the commit path avoided staging it")
	}
	if leaked := worktreeGitlinksInIndex(t, g, ctx); len(leaked) != 0 {
		t.Fatalf("Work commit path leaked worktree gitlink(s) into the beehive index: %v", leaked)
	}
}

// TestSweepRemovesOrphanWorktreeGitlink is the GC-sweep acceptance: a real
// registered submodule plus a leaked orphan gitlink under worktrees/. The orphan
// wedges `git submodule update` (fatal: no .gitmodules URL). After the sweep the
// orphan is gone, the registered submodule gitlink is untouched, and
// `git submodule update` succeeds.
func TestSweepRemovesOrphanWorktreeGitlink(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file") // permit the local-path submodule clone offline
	ctx := context.Background()

	// A source repo to register as a REAL (declared) submodule.
	src := t.TempDir()
	sg := gitInit(t, src)
	os.WriteFile(filepath.Join(src, "s.txt"), []byte("sub\n"), 0o644)
	if err := sg.Commit(ctx, "sub init"); err != nil {
		t.Fatalf("seed src: %v", err)
	}

	root := t.TempDir()
	g := gitInit(t, root)
	os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644)
	if err := g.Commit(ctx, "init"); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	if _, err := g.Run(ctx, "-c", "protocol.file.allow=always", "submodule", "add", src, "submodules/sm/repo"); err != nil {
		t.Fatalf("submodule add: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "add submodule"); err != nil {
		t.Fatalf("commit submodule: %v", err)
	}
	// Seed an ORPHAN gitlink: a 160000 index entry under worktrees/ with NO
	// .gitmodules URL — exactly what a leaked honeybee code-worktree looks like.
	orphanPath := "submodules/sm/worktrees/bee-x"
	if _, err := g.Run(ctx, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("3", 40)+","+orphanPath); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "leak orphan"); err != nil {
		t.Fatalf("commit orphan: %v", err)
	}

	// Precondition: the orphan wedges `git submodule update`.
	if _, err := g.Run(ctx, "submodule", "update", "--init"); err == nil {
		t.Fatal("precondition failed: expected `git submodule update` to fatal on the orphan gitlink")
	}

	r := &Runner{Git: g, TTL: time.Hour}
	if err := r.sweepOrphanWorktreeGitlinks(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// 1) The orphan is gone from the index.
	if got := worktreeGitlinksInIndex(t, g, ctx); len(got) != 0 {
		t.Fatalf("sweep left orphan worktree gitlink(s) in the index: %v", got)
	}
	// 2) The registered submodule gitlink is untouched.
	ls, err := g.Run(ctx, "ls-files", "-s", "--", "submodules/sm/repo")
	if err != nil || !strings.HasPrefix(ls, "160000 ") || !strings.Contains(ls, "submodules/sm/repo") {
		t.Fatalf("sweep disturbed the registered submodule gitlink (ls=%q err=%v)", ls, err)
	}
	// 3) `git submodule update` now succeeds — the wedge is cleared.
	if out, err := g.Run(ctx, "submodule", "update", "--init"); err != nil {
		t.Fatalf("submodule update still fails after sweep: %v\n%s", err, out)
	}
}

// TestSweepLeavesLiveWorktreeCheckoutOnDisk proves the sweep only drops the stray
// INDEX entry: when the leaked path is a live nested checkout (a code worktree a
// peer may still be using), its on-disk files are left intact and the gitlink
// does not reappear (the removal is committed via the staged index, never a
// pathspec add that would re-stage the live checkout).
func TestSweepLeavesLiveWorktreeCheckoutOnDisk(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644)
	if err := g.Commit(ctx, "init"); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	// A live nested checkout at the worktree path, staged (leaked) as a gitlink.
	wtRel := filepath.Join("submodules", "sm", "worktrees", "bee-live")
	wtAbs := filepath.Join(root, wtRel)
	os.MkdirAll(wtAbs, 0o755)
	gitInit(t, wtAbs)
	codeFile := filepath.Join(wtAbs, "code")
	os.WriteFile(codeFile, []byte("live work\n"), 0o644)
	git.New(wtAbs).Commit(ctx, "code")
	if _, err := g.Run(ctx, "add", wtRel); err != nil {
		t.Fatalf("stage worktree: %v", err)
	}
	if _, err := g.Run(ctx, "commit", "-m", "leak live worktree"); err != nil {
		t.Fatalf("commit leak: %v", err)
	}

	r := &Runner{Git: g, TTL: time.Hour}
	if err := r.sweepOrphanWorktreeGitlinks(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := worktreeGitlinksInIndex(t, g, ctx); len(got) != 0 {
		t.Fatalf("sweep did not remove the live-checkout gitlink from the index: %v", got)
	}
	// The peer's on-disk worktree files must be untouched.
	if b, err := os.ReadFile(codeFile); err != nil || string(b) != "live work\n" {
		t.Fatalf("sweep destroyed the live worktree checkout on disk (err=%v content=%q)", err, string(b))
	}
}

// TestMainAdvancedComparesPublishedStamp is the unit proof of the second
// publish-tree guard (Runner.mainAdvanced): it evaluates the bootstrap/reconcile
// completion predicate against the PUBLISHED main (origin/main when a remote is
// set, else local main) rather than the working tree. The decisive contrast is
// case (2): a matching ROI stamp written ONLY to the working tree — exactly what
// reconciled() reads and clears on — must read as NOT advanced here, because it
// was never committed/published. The remote section proves it reads origin/main
// and not a locally-regressed main. Every git failure is surfaced, never a false
// "advanced".
func TestMainAdvancedComparesPublishedStamp(t *testing.T) {
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
	planPath := filepath.Join(sm, "PLAN.md")
	recSel := &selectt.Selection{Kind: selectt.Reconcile, Submodule: subs[0]}
	bootSel := &selectt.Selection{Kind: selectt.Bootstrap, Submodule: subs[0]}

	// Non-nil Publish so the guard actually evaluates (nil short-circuits to true
	// for the local, no-convergence mode). Remote "" => the published ref is local
	// main, matching the harness the other publish tests use.
	r := &Runner{Repo: rp, Git: g, Publish: func(context.Context) error { return nil }}

	// 1) Nothing on main yet: neither kind has advanced.
	if adv, err := r.mainAdvanced(ctx, recSel); err != nil || adv {
		t.Fatalf("reconcile: no PLAN on main must be not-advanced, got adv=%v err=%v", adv, err)
	}
	if adv, err := r.mainAdvanced(ctx, bootSel); err != nil || adv {
		t.Fatalf("bootstrap: no PLAN on main must be not-advanced, got adv=%v err=%v", adv, err)
	}

	// 2) A matching stamp written ONLY to the working tree (never committed) must
	// NOT count as advanced — the guard reads the PUBLISHED main. Prove the
	// contrast: reconciled() (the local trigger) DOES clear on that same stamp.
	wtBody := "<!-- Beehive-ROI: " + head[:12] + " -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"
	if err := os.WriteFile(planPath, []byte(wtBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if adv, err := r.mainAdvanced(ctx, recSel); err != nil || adv {
		t.Fatalf("reconcile: an uncommitted working-tree stamp must be not-advanced, got adv=%v err=%v", adv, err)
	}
	if wtSees, err := r.reconciled(recSel); err != nil || !wtSees {
		t.Fatalf("sanity: reconciled() must clear on the working-tree stamp (got %v err=%v)", wtSees, err)
	}

	// 3) Commit that PLAN.md to main: now bootstrap (PLAN exists) and reconcile
	// (stamp prefixes the ROI head) both see main as advanced.
	if err := g.CommitPaths(ctx, "plan: reconcile", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("commit plan: %v", err)
	}
	if adv, err := r.mainAdvanced(ctx, recSel); err != nil || !adv {
		t.Fatalf("reconcile: a committed matching stamp on main must be advanced, got adv=%v err=%v", adv, err)
	}
	if adv, err := r.mainAdvanced(ctx, bootSel); err != nil || !adv {
		t.Fatalf("bootstrap: a committed PLAN.md on main must be advanced, got adv=%v err=%v", adv, err)
	}

	// 4) A committed PLAN whose stamp does NOT prefix the ROI head fails the
	// reconcile predicate, even though the file exists (bootstrap needs only
	// existence, so it stays advanced).
	bad := "<!-- Beehive-ROI: deadbeefcafe -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"
	if err := os.WriteFile(planPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.CommitPaths(ctx, "plan: wrong stamp", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("commit bad plan: %v", err)
	}
	if adv, err := r.mainAdvanced(ctx, recSel); err != nil || adv {
		t.Fatalf("reconcile: a non-prefix stamp on main must be not-advanced, got adv=%v err=%v", adv, err)
	}
	if adv, err := r.mainAdvanced(ctx, bootSel); err != nil || !adv {
		t.Fatalf("bootstrap: existence-only must stay advanced, got adv=%v err=%v", adv, err)
	}

	// 5) Publish == nil (local no-convergence mode) short-circuits to advanced,
	// even though main currently holds the non-matching stamp from case (4).
	rNil := &Runner{Repo: rp, Git: g}
	if adv, err := rNil.mainAdvanced(ctx, recSel); err != nil || !adv {
		t.Fatalf("nil Publish must short-circuit to advanced, got adv=%v err=%v", adv, err)
	}

	// 6) Remote mode reads origin/main, not local main. Push a good matching stamp
	// to a bare origin, then REGRESS local main (without pushing): the remote guard
	// must still see origin/main as advanced, proving it reads the published ref;
	// the local guard sees the regressed local main as not advanced.
	origin := t.TempDir()
	ob := git.New(origin)
	if _, err := ob.Run(ctx, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatalf("bare origin init: %v", err)
	}
	if _, err := g.Run(ctx, "remote", "add", "origin", origin); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	good := "<!-- Beehive-ROI: " + head[:12] + " -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"
	if err := os.WriteFile(planPath, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.CommitPaths(ctx, "plan: good, pushed", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("commit good plan: %v", err)
	}
	if err := g.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("push origin main: %v", err)
	}
	// Regress local main only (not pushed): origin/main keeps the good stamp.
	if err := os.WriteFile(planPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.CommitPaths(ctx, "plan: local regress, unpushed", "submodules/sm/PLAN.md"); err != nil {
		t.Fatalf("commit regress: %v", err)
	}
	rRemote := &Runner{Repo: rp, Git: g, Remote: "origin", Publish: func(context.Context) error { return nil }}
	if adv, err := rRemote.mainAdvanced(ctx, recSel); err != nil || !adv {
		t.Fatalf("remote: origin/main holds the good stamp, must be advanced (reads origin, not regressed local), got adv=%v err=%v", adv, err)
	}
	if adv, err := r.mainAdvanced(ctx, recSel); err != nil || adv {
		t.Fatalf("local: regressed local main must be not-advanced, got adv=%v err=%v", adv, err)
	}
}

// TestRunBootstrapGatesOnUnpublishedPlan is the second publish-tree guard's
// end-to-end regression: a bootstrap agent writes PLAN.md into its worktree but
// never COMMITS it, so the local completion check passes yet the publish is a
// no-op (the branch merge to main carries nothing). The run must NOT report
// Completed — main never gained PLAN.md — and must mark the task for GC so the
// bootstrap is re-driven. It is the negative twin of TestRunPublishesSessionToMain
// (which DOES commit and stays Completed), differing ONLY in the missing commit.
func TestRunBootstrapGatesOnUnpublishedPlan(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("# ROI\nbuild it\n"), 0o644)
	g.Commit(context.Background(), "seed roi")

	ctx := context.Background()
	wtPath := filepath.Join(root, ".worktrees", "bee-x")
	if err := g.WorktreeAdd(ctx, wtPath, "bee-x", "main"); err != nil {
		t.Fatal(err)
	}
	sessPath := filepath.Join(root, ".worktrees", "bee-x-session")
	if err := g.WorktreeAdd(ctx, sessPath, "bee-x-session", "main"); err != nil {
		t.Fatal(err)
	}
	wrp, _ := repo.Open(wtPath)
	wg := git.New(wtPath)
	sessGit := git.New(sessPath)
	subs, _ := wrp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Bootstrap, Submodule: subs[0]}

	cl := &scriptClient{}
	cl.onPrompt = func() {
		// Write PLAN.md into the agent worktree but DELIBERATELY never commit it:
		// the on-disk existence check passes, but the branch has no new commit, so
		// the publish merges nothing to main (the exact phantom-DONE gap).
		os.WriteFile(subs[0].PlanPath(), []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}
	r := &Runner{
		Repo: wrp, Git: wg, Client: cl, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour,
		Publish:        func(ctx context.Context) error { return wg.PublishToMain(ctx, "") },
		SessionGit:     sessGit,
		SessionRoot:    sessPath,
		SessionBranch:  "bee-x-session",
		SessionPublish: func(ctx context.Context) error { return sessGit.PublishToMain(ctx, "") },
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run must not error (the work is merely re-driven): %v", err)
	}
	if res.Completed {
		t.Fatalf("uncommitted PLAN never reached main; must NOT report Completed (phantom DONE): %+v", res)
	}
	if !res.GCMarked {
		t.Fatalf("must mark GC so the bootstrap is re-driven: %+v", res)
	}
	if !contains(res.Warning, "did not advance") {
		t.Fatalf("warning should name the no-op publish, got %q", res.Warning)
	}
	// main must NOT carry PLAN.md — the guard's whole point.
	if _, err := g.Show(ctx, "main", "submodules/sm/PLAN.md"); err == nil {
		t.Fatal("PLAN.md unexpectedly reached main despite never being committed")
	}
}

// setupPublishConflict builds a hive repo where the work branch and main both
// edited the same file, so the next publish conflicts. Returns the primary repo,
// the work-worktree repo, and the conflicted path.
func setupPublishConflict(t *testing.T) (g, wg *git.Repo, wtPath string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	g = gitInit(t, root)
	os.WriteFile(filepath.Join(root, "shared"), []byte("base\n"), 0o644)
	g.Commit(ctx, "base")
	wtPath = filepath.Join(root, ".worktrees", "bee-x")
	if err := g.WorktreeAdd(ctx, wtPath, "bee-x", "main"); err != nil {
		t.Fatal(err)
	}
	wg = git.New(wtPath)
	os.WriteFile(filepath.Join(wtPath, "shared"), []byte("branch change\n"), 0o644)
	if err := wg.CommitPaths(ctx, "branch edit", "shared"); err != nil {
		t.Fatal(err)
	}
	// main advances with a conflicting edit.
	os.WriteFile(filepath.Join(root, "shared"), []byte("main change\n"), 0o644)
	g.Commit(ctx, "main edit")
	return g, wg, wtPath
}

// TestPublishWithResolutionResolvesConflict: a publish conflict is handed to the
// agent, which rewrites the file to a clean merge; the runner commits it and the
// retry publish lands the work on main.
func TestPublishWithResolutionResolvesConflict(t *testing.T) {
	ctx := context.Background()
	g, wg, wtPath := setupPublishConflict(t)
	sess := &mockSession{onTurn: func(int) {
		os.WriteFile(filepath.Join(wtPath, "shared"), []byte("merged both changes\n"), 0o644)
	}}
	r := &Runner{Git: wg, MergeRetries: 8, Publish: func(ctx context.Context) error { return wg.PublishToMain(ctx, "") }}
	if err := r.publishWithResolution(ctx, sess); err != nil {
		t.Fatalf("publishWithResolution: %v", err)
	}
	if sess.prompts != 1 {
		t.Fatalf("agent prompts = %d, want exactly 1 resolution turn", sess.prompts)
	}
	body, err := g.Show(ctx, "main", "shared")
	if err != nil || !strings.Contains(body, "merged both changes") {
		t.Fatalf("main should carry the resolved merge, got %q (err %v)", body, err)
	}
}

// TestPublishWithResolutionDefersWhenUnresolved: if the agent does NOT clear the
// conflict, the runner must abort the merge (clean worktree), NOT land the work,
// and return a deferred ErrConflict for a later honeybee — never a wedge.
func TestPublishWithResolutionDefersWhenUnresolved(t *testing.T) {
	ctx := context.Background()
	g, wg, _ := setupPublishConflict(t)
	sess := &mockSession{onTurn: func(int) { /* leave the conflict markers in place */ }}
	r := &Runner{Git: wg, MergeRetries: 3, Publish: func(ctx context.Context) error { return wg.PublishToMain(ctx, "") }}
	err := r.publishWithResolution(ctx, sess)
	if err == nil || !errors.Is(err, git.ErrConflict) {
		t.Fatalf("want a deferred ErrConflict, got %v", err)
	}
	if st, _ := wg.Status(ctx); st != "" {
		t.Fatalf("work worktree must be clean after a deferred conflict, got %q", st)
	}
	body, _ := g.Show(ctx, "main", "shared")
	if strings.Contains(body, "branch change") {
		t.Fatalf("work must NOT have landed on main after a deferred conflict, got %q", body)
	}
}

func TestRunReconcileAlreadyAppliedSkipsSession(t *testing.T) {
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
	// PLAN already stamped to the live ROI head: the reconcile delta is applied.
	os.WriteFile(filepath.Join(sm, "PLAN.md"),
		[]byte("<!-- Beehive-ROI: "+head+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	g.Commit(ctx, "stamp")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Reconcile, Submodule: subs[0]}

	var gotSys string
	mc := &mockClient{gotSystem: &gotSys} // sess nil; Open would set gotSys + sess
	// No Remote => refreshMain is a no-op; the guard judges the local (stamped) tree.
	r := &Runner{Repo: rp, Git: g, Client: mc, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("already-applied reconcile must report Completed, got %+v", res)
	}
	if res.Turns != 0 {
		t.Fatalf("already-applied reconcile must spend zero turns, got %d", res.Turns)
	}
	if mc.sess != nil || gotSys != "" {
		t.Fatalf("already-applied reconcile must not open a session (sess=%v system=%q)", mc.sess, gotSys)
	}
}

// TestRunReconcileDriftedRunsSession is the converse: a genuinely drifted Reconcile
// (PLAN stamped to a dead sha) must NOT be short-circuited — it opens a session and
// runs. The mock folds the delta on turn 1 (stamps PLAN to the live head) so the
// per-turn completion check clears and the run finishes normally.
func TestRunReconcileDriftedRunsSession(t *testing.T) {
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
	planPath := filepath.Join(sm, "PLAN.md")
	// Stamped to a dead sha: genuine drift, the reconcile must run.
	os.WriteFile(planPath, []byte("<!-- Beehive-ROI: deadbeef0000 -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed plan")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Reconcile, Submodule: subs[0]}

	var gotSys string
	mc := &mockClient{gotSystem: &gotSys, sess: &mockSession{onTurn: func(turn int) {
		// The agent folds the ROI delta: stamp PLAN to the live head so reconciled() clears.
		os.WriteFile(planPath, []byte("<!-- Beehive-ROI: "+head+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: mc, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("drifted reconcile should complete after folding, got %+v", res)
	}
	if res.Turns < 1 {
		t.Fatalf("drifted reconcile must run at least one turn, got %d", res.Turns)
	}
	if gotSys != "sys" || mc.sess.prompts < 1 {
		t.Fatalf("drifted reconcile must open a session and prompt (system=%q prompts=%d)", gotSys, mc.sess.prompts)
	}
}

func gitConfig(t *testing.T, dir string) *git.Repo {
	t.Helper()
	g := git.New(dir)
	ctx := context.Background()
	for _, a := range [][]string{{"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if _, err := g.Run(ctx, a...); err != nil {
			t.Fatalf("config %v: %v", a, err)
		}
	}
	return g
}

// bareOriginSeeded creates a bare origin with a single base commit on main
// (seeded via a scratch clone driven by g) and returns the origin path.
func bareOriginSeeded(t *testing.T, g *git.Repo) string {
	t.Helper()
	ctx := context.Background()
	origin := t.TempDir()
	if _, err := git.New(origin).Run(ctx, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatalf("bare init: %v", err)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if _, err := g.Run(ctx, "clone", "-q", origin, seed); err != nil {
		t.Fatalf("seed clone: %v", err)
	}
	sg := gitConfig(t, seed)
	os.WriteFile(filepath.Join(seed, "f"), []byte("base\n"), 0o644)
	if err := sg.Commit(ctx, "base"); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	if err := sg.Push(ctx, "origin", "main"); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	return origin
}

// originHasBranch reports whether the bare origin still advertises a local branch.
func originHasBranch(t *testing.T, origin, branch string) bool {
	t.Helper()
	out, err := git.New(origin).Run(context.Background(), "for-each-ref", "--format=%(refname:short)", "refs/heads/"+branch)
	if err != nil {
		t.Fatalf("for-each-ref %s: %v", branch, err)
	}
	return strings.TrimSpace(out) == branch
}

// TestReclaimSourceBranchGuard drives reclaimSourceBranch directly across its four
// outcomes: a merged branch (tip already on the tracked main) is deleted on origin;
// an unmerged in-flight branch is left intact (deleting it would lose the commit
// and dangle the bumped pointer); a missing branch is a silent no-op; and a
// checkout with no remote is a silent no-op. None may surface a warning.
func TestReclaimSourceBranchGuard(t *testing.T) {
	ctx := context.Background()

	// build makes a beehive Runner whose submodule "sm" is a fresh clone of a bare
	// origin seeded with main, returning the pieces the assertions need. Each case
	// gets its own isolated origin + checkout.
	build := func(t *testing.T) (*Runner, repo.Submodule, string, string) {
		t.Helper()
		root := t.TempDir()
		g := gitInit(t, root)
		repo.Init(root)
		sm := filepath.Join(root, "submodules", "sm")
		os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
		origin := bareOriginSeeded(t, g)
		repoDir := filepath.Join(sm, "repo")
		if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
			t.Fatalf("clone submodule: %v", err)
		}
		gitConfig(t, repoDir)
		g.Commit(ctx, "seed")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		absRoot, _ := filepath.Abs(root)
		return &Runner{Repo: rp, Git: g}, subs[0], origin, absRoot
	}

	// pushBee creates branch on origin via a scratch clone: extra==true adds a
	// commit (so the branch diverges from main = unmerged); extra==false points it
	// at main (already merged).
	pushBee := func(t *testing.T, g *git.Repo, origin, branch string, extra bool) {
		t.Helper()
		sc := filepath.Join(t.TempDir(), "push-"+branch)
		if _, err := g.Run(ctx, "clone", "-q", origin, sc); err != nil {
			t.Fatalf("scratch clone: %v", err)
		}
		scg := gitConfig(t, sc)
		if extra {
			os.WriteFile(filepath.Join(sc, branch+".txt"), []byte("work\n"), 0o644)
			if err := scg.Commit(ctx, "work on "+branch); err != nil {
				t.Fatalf("commit on %s: %v", branch, err)
			}
		}
		if _, err := scg.Run(ctx, "push", "origin", "HEAD:refs/heads/"+branch); err != nil {
			t.Fatalf("push %s: %v", branch, err)
		}
	}

	t.Run("merged-deletes", func(t *testing.T) {
		r, sub, origin, absRoot := build(t)
		pushBee(t, r.Git, origin, "bee-T1", false) // == main: merged
		if !originHasBranch(t, origin, "bee-T1") {
			t.Fatal("precondition: bee-T1 should be on origin")
		}
		if w := r.reclaimSourceBranch(ctx, sub, "bee-T1", absRoot); w != "" {
			t.Fatalf("merged branch reclaim warned: %q", w)
		}
		if originHasBranch(t, origin, "bee-T1") {
			t.Fatal("a merged source branch must be deleted on origin")
		}
	})

	t.Run("unmerged-kept", func(t *testing.T) {
		r, sub, origin, absRoot := build(t)
		pushBee(t, r.Git, origin, "bee-T1", true) // main + 1: unmerged
		if w := r.reclaimSourceBranch(ctx, sub, "bee-T1", absRoot); w != "" {
			t.Fatalf("unmerged branch reclaim warned: %q", w)
		}
		if !originHasBranch(t, origin, "bee-T1") {
			t.Fatal("an unmerged in-flight source branch must be left intact on origin")
		}
	})

	t.Run("merged-but-task-not-done-kept", func(t *testing.T) {
		// A branch merged into tracked main whose hive task is still NEEDS-REVIEW
		// (an interrupted review) must be KEPT — it is the evidence
		// finalizeIfAlreadyMerged needs; deleting it strands the task.
		r, sub, origin, absRoot := build(t)
		os.WriteFile(sub.PlanPath(), []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\nreview\n"), 0o644)
		pushBee(t, r.Git, origin, "bee-T1", false) // == main: merged
		w := r.reclaimSourceBranch(ctx, sub, "bee-T1", absRoot)
		if w == "" {
			t.Fatal("reclaim of a merged branch whose task is not DONE must warn, not silently delete")
		}
		if !originHasBranch(t, origin, "bee-T1") {
			t.Fatal("a merged branch whose task is still NEEDS-REVIEW must be kept as finalize evidence")
		}
	})

	t.Run("merged-and-task-done-deletes", func(t *testing.T) {
		r, sub, origin, absRoot := build(t)
		os.WriteFile(sub.PlanPath(), []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ndone\n"), 0o644)
		pushBee(t, r.Git, origin, "bee-T1", false) // == main: merged
		if w := r.reclaimSourceBranch(ctx, sub, "bee-T1", absRoot); w != "" {
			t.Fatalf("merged+DONE branch reclaim warned: %q", w)
		}
		if originHasBranch(t, origin, "bee-T1") {
			t.Fatal("a merged branch whose task is DONE must be deleted on origin")
		}
	})

	t.Run("missing-noop", func(t *testing.T) {
		r, sub, origin, absRoot := build(t)
		if w := r.reclaimSourceBranch(ctx, sub, "bee-T1", absRoot); w != "" {
			t.Fatalf("missing branch reclaim must be a silent no-op, warned: %q", w)
		}
		if originHasBranch(t, origin, "bee-T1") {
			t.Fatal("missing case must not create bee-T1")
		}
	})

	t.Run("no-remote-noop", func(t *testing.T) {
		root := t.TempDir()
		g := gitInit(t, root)
		repo.Init(root)
		sm := filepath.Join(root, "submodules", "sm")
		os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
		repoDir := filepath.Join(sm, "repo")
		os.MkdirAll(repoDir, 0o755)
		gitInit(t, repoDir) // a real checkout but NO remote
		os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
		git.New(repoDir).Commit(ctx, "base")
		g.Commit(ctx, "seed")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		absRoot, _ := filepath.Abs(root)
		r := &Runner{Repo: rp, Git: g}
		if w := r.reclaimSourceBranch(ctx, subs[0], "bee-T1", absRoot); w != "" {
			t.Fatalf("no-remote reclaim must be a silent no-op, warned: %q", w)
		}
	})
}

// TestDeleteSourceBranch drives deleteSourceBranch directly: it deletes an
// UNMERGED (divergent) branch on origin — the arbitration-reject case reclaim
// deliberately refuses — is a silent no-op on an already-gone branch, and a
// silent no-op on a checkout with no remote. None may surface a warning.
func TestDeleteSourceBranch(t *testing.T) {
	ctx := context.Background()

	build := func(t *testing.T) (*Runner, repo.Submodule, string) {
		t.Helper()
		root := t.TempDir()
		g := gitInit(t, root)
		repo.Init(root)
		sm := filepath.Join(root, "submodules", "sm")
		os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
		origin := bareOriginSeeded(t, g)
		repoDir := filepath.Join(sm, "repo")
		if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
			t.Fatalf("clone submodule: %v", err)
		}
		gitConfig(t, repoDir)
		g.Commit(ctx, "seed")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		return &Runner{Repo: rp, Git: g}, subs[0], origin
	}

	// pushDivergent pushes branch as main + 1 extra commit (unmerged / divergent).
	pushDivergent := func(t *testing.T, g *git.Repo, origin, branch string) {
		t.Helper()
		sc := filepath.Join(t.TempDir(), "push-"+branch)
		if _, err := g.Run(ctx, "clone", "-q", origin, sc); err != nil {
			t.Fatalf("scratch clone: %v", err)
		}
		scg := gitConfig(t, sc)
		os.WriteFile(filepath.Join(sc, branch+".txt"), []byte("work\n"), 0o644)
		if err := scg.Commit(ctx, "work on "+branch); err != nil {
			t.Fatalf("commit on %s: %v", branch, err)
		}
		if _, err := scg.Run(ctx, "push", "origin", "HEAD:refs/heads/"+branch); err != nil {
			t.Fatalf("push %s: %v", branch, err)
		}
	}

	t.Run("unmerged-deletes", func(t *testing.T) {
		r, sub, origin := build(t)
		pushDivergent(t, r.Git, origin, "bee-T1") // divergent: reclaim would KEEP it
		if !originHasBranch(t, origin, "bee-T1") {
			t.Fatal("precondition: bee-T1 should be on origin")
		}
		if w := r.deleteSourceBranch(ctx, sub, "bee-T1"); w != "" {
			t.Fatalf("delete warned: %q", w)
		}
		if originHasBranch(t, origin, "bee-T1") {
			t.Fatal("a superseded (unmerged) source branch must be deleted on origin")
		}
	})

	t.Run("missing-noop", func(t *testing.T) {
		r, sub, origin := build(t)
		if w := r.deleteSourceBranch(ctx, sub, "bee-T1"); w != "" {
			t.Fatalf("missing branch delete must be a silent no-op, warned: %q", w)
		}
		if originHasBranch(t, origin, "bee-T1") {
			t.Fatal("missing case must not create bee-T1")
		}
	})

	t.Run("no-remote-noop", func(t *testing.T) {
		root := t.TempDir()
		g := gitInit(t, root)
		repo.Init(root)
		sm := filepath.Join(root, "submodules", "sm")
		os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
		repoDir := filepath.Join(sm, "repo")
		os.MkdirAll(repoDir, 0o755)
		gitInit(t, repoDir) // a real checkout but NO remote
		os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
		git.New(repoDir).Commit(ctx, "base")
		g.Commit(ctx, "seed")
		rp, _ := repo.Open(root)
		subs, _ := rp.Submodules()
		r := &Runner{Repo: rp, Git: g}
		if w := r.deleteSourceBranch(ctx, subs[0], "bee-T1"); w != "" {
			t.Fatalf("no-remote delete must be a silent no-op, warned: %q", w)
		}
	})
}

// TestPushSourceBranchReconcilesDeadOrphan is the F-LIVE publish-side guard:
// when the bee branch name on origin is occupied by a SUPERSEDED prior attempt
// (a divergent dead orphan), a plain push of the new attempt is rejected
// non-fast-forward. pushSourceBranch must land the new commit WITHOUT ever
// force-pushing or deleting the orphan's ref: it reconciles (fetch + merge -s
// ours) so the retried push is a genuine fast-forward, keeping the orphan
// reachable as an ancestor instead of discarding it.
func TestPushSourceBranchReconcilesDeadOrphan(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	origin := bareOriginSeeded(t, g)

	// Old attempt: a divergent orphan on origin/bee-T1 (main + its own commit).
	old := filepath.Join(t.TempDir(), "old")
	if _, err := g.Run(ctx, "clone", "-q", origin, old); err != nil {
		t.Fatalf("clone old: %v", err)
	}
	oldg := gitConfig(t, old)
	os.WriteFile(filepath.Join(old, "old.txt"), []byte("old attempt\n"), 0o644)
	if err := oldg.Commit(ctx, "old attempt"); err != nil {
		t.Fatalf("old commit: %v", err)
	}
	orphanTip, err := oldg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse orphan HEAD: %v", err)
	}
	if _, err := oldg.Run(ctx, "push", "origin", "HEAD:refs/heads/bee-T1"); err != nil {
		t.Fatalf("push old bee-T1: %v", err)
	}

	// New attempt: a fresh checkout on branch bee-T1 built off main with a
	// DIFFERENT commit — not a descendant of the orphan, so a plain push is non-FF.
	wt := filepath.Join(t.TempDir(), "new")
	if _, err := g.Run(ctx, "clone", "-q", origin, wt); err != nil {
		t.Fatalf("clone new: %v", err)
	}
	wg := gitConfig(t, wt)
	if _, err := wg.Run(ctx, "checkout", "-q", "-b", "bee-T1", "origin/main"); err != nil {
		t.Fatalf("checkout bee-T1: %v", err)
	}
	os.WriteFile(filepath.Join(wt, "new.txt"), []byte("new attempt\n"), 0o644)
	if err := wg.Commit(ctx, "new attempt"); err != nil {
		t.Fatalf("new commit: %v", err)
	}
	wantTip, err := wg.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	r := &Runner{}
	if w := r.pushSourceBranch(ctx, wg, "bee-T1"); w != "" {
		t.Fatalf("pushSourceBranch over a dead orphan warned: %q", w)
	}
	gotTip, err := git.New(origin).Run(ctx, "rev-parse", "refs/heads/bee-T1")
	if err != nil {
		t.Fatalf("origin rev-parse bee-T1: %v", err)
	}
	gotTip = strings.TrimSpace(gotTip)
	// Never force-pushed away: BOTH the new work and the orphan remain reachable
	// ancestors of the reconciled tip landed on origin.
	if ok, err := wg.IsAncestor(ctx, wantTip, gotTip); err != nil || !ok {
		t.Fatalf("new work %s must be reachable from reconciled origin tip %s: ok=%v err=%v", wantTip, gotTip, ok, err)
	}
	if ok, err := wg.IsAncestor(ctx, orphanTip, gotTip); err != nil || !ok {
		t.Fatalf("dead orphan %s must remain reachable from reconciled origin tip %s: ok=%v err=%v", orphanTip, gotTip, ok, err)
	}
}

// TestLandSourceBranchNoOpsOutsideNeedsReview locks a narrow but important
// correctness edge: if a push fails while the task's ACTUAL status is not (or
// no longer) NEEDS-REVIEW — outside landSourceBranch/demoteUnpushed's owned
// shape — the caller must still SEE the push-failure warning, but must NEVER be
// told a demotion happened when none did (demoted must be false, and PLAN.md
// must be untouched). Conflating "demoteUnpushed no-op'd" with "successfully
// demoted" would wrongly strand a task's claim/Completed reporting on a status
// this guard was never meant to touch.
func TestLandSourceBranchNoOpsOutsideNeedsReview(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	wg := gitConfig(t, repoDir)
	if _, err := wg.Run(ctx, "checkout", "-q", "-b", "bee-T1"); err != nil {
		t.Fatalf("checkout bee-T1: %v", err)
	}
	os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("work\n"), 0o644)
	if err := wg.Commit(ctx, "work"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	blockAllPushes(t, origin) // every push, including the reconciliation retry, fails

	const planBody = "## T1 [DONE] <!-- attempts=0 deps= -->\ndo it\n"
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte(planBody), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.Done}}

	r := &Runner{Repo: rp, Git: g, RejectLimit: 3}
	w, demoted, err := r.landSourceBranch(ctx, sel, wg, "bee-T1")
	if err != nil {
		t.Fatalf("landSourceBranch: %v", err)
	}
	if w == "" {
		t.Fatal("a genuine push failure must still surface a warning")
	}
	if demoted {
		t.Fatal("demoted=true but the task was DONE, not NEEDS-REVIEW: demoteUnpushed must have no-op'd")
	}
	got, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if string(got) != planBody {
		t.Fatalf("PLAN.md was touched despite the no-op path:\n%s", got)
	}
}

// TestWorkCompletionKeepsUnmergedSourceBranch is the end-to-end guard: a Work task
// that completes to NEEDS-REVIEW pushes its bee-<taskid> source branch to the
// submodule origin, but because that branch carries a commit not yet on the tracked
// main, the DONE/cap reclaim must LEAVE IT INTACT — deleting it would lose the
// in-flight commit and dangle the just-bumped pointer for reviewers/peers.
func TestWorkCompletionKeepsUnmergedSourceBranch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)

	// The submodule checkout is a clone of a bare origin seeded with main.
	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	wtDir := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		// Author a real commit on the source branch so bee-T1 diverges from the
		// tracked main (a genuine in-flight, not-yet-merged change)...
		os.WriteFile(filepath.Join(wtDir, "feature.txt"), []byte("work\n"), 0o644)
		_ = git.New(wtDir).Commit(ctx, "feat: in-flight work")
		// ...then complete the Work handoff: change doc + NEEDS-REVIEW.
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("want completed handoff, got %+v", res)
	}
	// The source branch was pushed (so the pointer resolves) AND kept (unmerged).
	if !originHasBranch(t, origin, "bee-T1") {
		t.Fatal("completed Work pushed an unmerged bee-T1 that reclaim wrongly deleted")
	}
	if res.Warning != "" {
		t.Fatalf("keeping an unmerged branch must not warn, got %q", res.Warning)
	}
}

// blockAllPushes installs a pre-receive hook on the bare origin that rejects
// EVERY push (fetch/clone are unaffected — hooks only run receive-side), so a
// completion push can be made to fail deterministically without depending on
// filesystem permissions or a real network outage.
func blockAllPushes(t *testing.T, origin string) {
	t.Helper()
	hooksDir := filepath.Join(origin, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hooksDir, "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'push blocked for test' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestWorkCompletionDemotesWhenSourceBranchCannotLand is the F-LIVE publish-side
// guard's failure path (session-audit-003): when a Work task's implementer
// commit cannot be pushed to the submodule's origin at all — here every push is
// rejected by a pre-receive hook — Run() must NOT report the task complete
// pointing at NEEDS-REVIEW. It demotes the task back to a workable status
// (TODO) and publishes that correction itself, so the runner never leaves a
// task NEEDS-REVIEW at a commit no reviewer can reach.
func TestWorkCompletionDemotesWhenSourceBranchCannotLand(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)

	origin := bareOriginSeeded(t, g)
	repoDir := filepath.Join(sm, "repo")
	if _, err := g.Run(ctx, "clone", "-q", origin, repoDir); err != nil {
		t.Fatalf("clone submodule: %v", err)
	}
	gitConfig(t, repoDir)
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")

	// Every push (the completion push AND any reconciliation retry) is rejected
	// from this point on; fetch/clone (syncWorktreeBase, above) already ran and
	// is unaffected by a receive-side hook.
	blockAllPushes(t, origin)

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	wtDir := filepath.Join(subs[0].WorktreesDir(), "bee-T1")
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(wtDir, "feature.txt"), []byte("work\n"), 0o644)
		_ = git.New(wtDir).Commit(ctx, "feat: work")
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, RejectLimit: 3}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed {
		t.Fatalf("a task whose source branch cannot land must NOT report Completed, got %+v", res)
	}
	if res.Warning == "" || !contains(res.Warning, "could not be landed") {
		t.Fatalf("warning missing/wrong: %q", res.Warning)
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	tk := p.Find("T1")
	if tk == nil {
		t.Fatal("task T1 vanished from PLAN.md")
	}
	if tk.Status != plan.TODO {
		t.Fatalf("status = %s, want TODO (demoted back to workable instead of stranded NEEDS-REVIEW)", tk.Status)
	}
	if tk.Session != "" || !tk.Heartbeat.IsZero() {
		t.Fatal("demotion must release the claim")
	}
	if tk.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", tk.Attempts)
	}
}

// --- handoff verify-gate -----------------------------------------------------

// gateFixture builds a Work-task fixture wired for the handoff verify-gate: a
// superproject with one submodule whose repo has a committed HEAD (so Run can add
// the code worktree) and, when goMod is set, a go.mod at its root (so the gate is
// applicable). PLAN.md carries a single TODO task T1. Returns the pieces a Run
// needs plus the code-worktree path the gate must execute in.
func gateFixture(t *testing.T, goMod bool) (g *git.Repo, rp *repo.Repo, sm, planPath, wtDir string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	g = gitInit(t, root)
	repo.Init(root)
	sm = filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	repoDir := filepath.Join(sm, "repo")
	os.MkdirAll(repoDir, 0o755)
	gitInit(t, repoDir)
	os.WriteFile(filepath.Join(repoDir, "f"), []byte("x"), 0o644)
	if goMod {
		os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o644)
	}
	if err := git.New(repoDir).Commit(ctx, "base"); err != nil {
		t.Fatalf("submodule base commit: %v", err)
	}
	planPath = filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= heartbeat=2026-06-29T10:00:00Z -->\ngo\n"), 0o644)
	g.Commit(ctx, "seed")
	rp, _ = repo.Open(root)
	wtDir = filepath.Join(sm, "worktrees", "bee-T1")
	return
}

// gateCall records one verify-gate invocation for assertions.
type gateCall struct {
	dir  string
	name string
	args []string
}

// gateRec is an injectable RunVerify that records every gate invocation and
// returns a programmed outcome (default: green). Single-threaded — the gate runs
// sequentially within the turn loop — so no locking is needed.
type gateRec struct {
	calls []gateCall
	resp  func(name string, args []string) (verifyOutcome, error)
}

func (gr *gateRec) run(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error) {
	gr.calls = append(gr.calls, gateCall{dir: dir, name: name, args: append([]string(nil), args...)})
	if gr.resp != nil {
		return gr.resp(name, args)
	}
	return verifyOutcome{}, nil
}

// TestVerifyGateGreenAllowsHandoffWithStaticInvocation: a clean worktree flips to
// NEEDS-REVIEW and the gate — which ran exactly gofmt -l . / go vet ./... / go test
// ./..., in the code worktree, and NEVER `go test -race` — lets the handoff stand.
func TestVerifyGateGreenAllowsHandoffWithStaticInvocation(t *testing.T) {
	ctx := context.Background()
	g, rp, sm, planPath, wtDir := gateFixture(t, true)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	gr := &gateRec{} // default: every check green
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, RunVerify: gr.run}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed || res.GCMarked {
		t.Fatalf("a green gate must complete the handoff: %+v", res)
	}
	want := []gateCall{
		{dir: wtDir, name: "git", args: []string{"status", "--porcelain"}},
		{dir: wtDir, name: "gofmt", args: []string{"-l", "."}},
		{dir: wtDir, name: "go", args: []string{"vet", "./..."}},
		{dir: wtDir, name: "go", args: []string{"test", "./..."}},
	}
	if !reflect.DeepEqual(gr.calls, want) {
		t.Fatalf("gate invocation mismatch:\n got %+v\nwant %+v", gr.calls, want)
	}
	for _, c := range gr.calls {
		for _, a := range c.args {
			if a == "-race" {
				t.Fatalf("gate must use the static invocation, never -race: %+v", c)
			}
		}
	}
}

// TestVerifyGateRedBlocksThenFixForwardCompletes: a red gate does NOT complete the
// handoff — it keeps the claim and feeds the failure back as the next prompt — and
// once the agent fixes it (the gate goes green) the same session completes.
func TestVerifyGateRedBlocksThenFixForwardCompletes(t *testing.T) {
	ctx := context.Background()
	g, rp, sm, planPath, _ := gateFixture(t, true)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	goTest := 0
	gr := &gateRec{resp: func(name string, args []string) (verifyOutcome, error) {
		if name == "go" && len(args) > 0 && args[0] == "test" {
			goTest++
			if goTest == 1 { // red on the first handoff, green after the agent fixes
				return verifyOutcome{out: "--- FAIL: TestX\nFAIL\tx\t0.1s", exitErr: true}, nil
			}
		}
		return verifyOutcome{}, nil
	}}
	var prompts []string
	cl := &mockClient{sess: &mockSession{all: &prompts, onTurn: func(turn int) {
		if turn == 1 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		}
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, RunVerify: gr.run}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed || res.GCMarked {
		t.Fatalf("fix-forward should complete once the gate goes green: %+v", res)
	}
	if len(prompts) < 2 {
		t.Fatalf("want a fix-forward turn after the red gate, got prompts %v", prompts)
	}
	if !strings.Contains(prompts[1], "Handoff verify-gate FAILED") || !strings.Contains(prompts[1], "FAIL: TestX") {
		t.Fatalf("the fix-forward prompt must carry the gate failure, got %q", prompts[1])
	}
}

// TestVerifyGateRedNeverCompletes: a persistently red gate NEVER reports the
// handoff complete — the run exhausts its turn cap, is GC-marked for retry (the
// claim intentionally left as the stale-GC signal), and each blocked turn re-feeds
// the failure so the agent keeps fixing forward.
func TestVerifyGateRedNeverCompletes(t *testing.T) {
	ctx := context.Background()
	g, rp, sm, planPath, _ := gateFixture(t, true)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	gr := &gateRec{resp: func(name string, args []string) (verifyOutcome, error) {
		if name == "go" && len(args) > 0 && args[0] == "test" {
			return verifyOutcome{out: "FAIL", exitErr: true}, nil // always red
		}
		return verifyOutcome{}, nil
	}}
	var prompts []string
	cl := &mockClient{sess: &mockSession{all: &prompts, onTurn: func(turn int) {
		if turn == 1 {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		}
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour, RunVerify: gr.run}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed {
		t.Fatalf("a persistently red gate must NOT report completion: %+v", res)
	}
	if !res.GCMarked {
		t.Fatalf("a blocked handoff at the turn cap must be GC-marked for retry: %+v", res)
	}
	if len(prompts) < 2 || !strings.Contains(prompts[len(prompts)-1], "Handoff verify-gate FAILED") {
		t.Fatalf("each blocked turn must re-feed the gate failure, got %v", prompts)
	}
}

// TestVerifyGateSkipsNonReviewFlip: the gate targets only the TODO->NEEDS-REVIEW
// review handoff; a Work pass that lands DONE directly is out of scope and must
// complete WITHOUT the gate ever running (a red stub would block it if it did).
func TestVerifyGateSkipsNonReviewFlip(t *testing.T) {
	ctx := context.Background()
	g, rp, sm, planPath, _ := gateFixture(t, true)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	gr := &gateRec{resp: func(name string, args []string) (verifyOutcome, error) {
		return verifyOutcome{out: "should never run", exitErr: true}, nil
	}}
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, RunVerify: gr.run}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a direct-to-DONE Work handoff is out of the gate's scope and must complete: %+v", res)
	}
	if len(gr.calls) != 0 {
		t.Fatalf("the gate must NOT run for a non-NEEDS-REVIEW flip, ran %+v", gr.calls)
	}
}

// TestVerifyGateSkipsGoChecksWithoutGoMod: the Go toolchain checks only run for a
// Go module, so a worktree with no go.mod skips gofmt/vet/test — but the
// uncommitted-work check still runs for it (non-Go targets like flux/gostream are
// exactly where the empty-branch bug bit). A CLEAN tree therefore completes with
// ONLY the `git status --porcelain` call and none of the Go checks.
func TestVerifyGateSkipsGoChecksWithoutGoMod(t *testing.T) {
	ctx := context.Background()
	g, rp, sm, planPath, wtDir := gateFixture(t, false) // no go.mod in the worktree
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	gr := &gateRec{resp: func(name string, args []string) (verifyOutcome, error) {
		if name == "git" {
			return verifyOutcome{}, nil // clean tree
		}
		return verifyOutcome{exitErr: true}, nil // any Go check would red — prove none runs
	}}
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, RunVerify: gr.run}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a clean non-Go-module worktree must complete: %+v", res)
	}
	want := []gateCall{{dir: wtDir, name: "git", args: []string{"status", "--porcelain"}}}
	if !reflect.DeepEqual(gr.calls, want) {
		t.Fatalf("only the uncommitted-work check must run without a go.mod:\n got %+v\nwant %+v", gr.calls, want)
	}
}

// TestVerifyGateDirtyTreeBlocksThenFixForwardCompletes: a code worktree that still
// has uncommitted changes at the NEEDS-REVIEW handoff does NOT complete (finish()
// would merge an empty branch and drop the edits); it keeps the claim and feeds
// back the fix-forward prompt, and once the agent commits (the tree goes clean)
// the same session completes. Uses a NON-Go module to prove the check guards
// exactly the targets the live bug hit.
func TestVerifyGateDirtyTreeBlocksThenFixForwardCompletes(t *testing.T) {
	ctx := context.Background()
	g, rp, sm, planPath, _ := gateFixture(t, false)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	gitStatus := 0
	gr := &gateRec{resp: func(name string, args []string) (verifyOutcome, error) {
		if name == "git" {
			gitStatus++
			if gitStatus == 1 { // dirty on the first handoff, clean after the agent commits
				return verifyOutcome{out: " M infrastructure/zuul/config-repo.yaml\n?? scripts/provision-zuul-registry-push-secret.sh"}, nil
			}
		}
		return verifyOutcome{}, nil
	}}
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 5, WallCap: time.Hour, TTL: time.Hour, RunVerify: gr.run}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed || res.GCMarked {
		t.Fatalf("a dirty tree must block once then complete after the fix: %+v", res)
	}
	if gitStatus < 2 {
		t.Fatalf("expected the uncommitted-work check to re-run after the fix, ran %d time(s)", gitStatus)
	}
}

// TestVerifyGateDirtyTreeNeverCompletes: while the code worktree stays dirty the
// handoff never completes (the task is left GC-marked for retry, never a phantom
// done that ships none of its code).
func TestVerifyGateDirtyTreeNeverCompletes(t *testing.T) {
	ctx := context.Background()
	g, rp, sm, planPath, _ := gateFixture(t, false)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	gr := &gateRec{resp: func(name string, args []string) (verifyOutcome, error) {
		if name == "git" {
			return verifyOutcome{out: " M infrastructure/zuul/config-repo.yaml"}, nil // always dirty
		}
		return verifyOutcome{}, nil
	}}
	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
		os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour, RunVerify: gr.run}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed {
		t.Fatalf("a persistently dirty tree must never complete the handoff: %+v", res)
	}
}

// TestWorkPassYieldsOnFiledBlockingDep: a work pass that discovers a missing
// prerequisite, files it, and blocks its OWN task on it (via `beehive task block`,
// modeled here by leaving the task TODO with a not-DONE dep) COMPLETES as a
// deliberate yield — the selector then holds it until the dep is DONE. Without
// this the pass would spin to the idle cap with nothing left to do.
func TestWorkPassYieldsOnFiledBlockingDep(t *testing.T) {
	ctx := context.Background()
	g, rp, sm, planPath, _ := gateFixture(t, false)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte(
			"## T1 [TODO] <!-- attempts=0 deps=T2 -->\n"+
				"## T2 [TODO] <!-- attempts=0 deps= -->\nnewly filed prerequisite\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 3, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a work pass that filed a blocking dep must complete (yield): %+v", res)
	}
	_ = sm
}

// TestWorkPassTODOWithoutBlockDoesNotComplete: the yield predicate is NARROW — a
// work pass that merely leaves its task TODO with NO unmet dep (did nothing) must
// NOT be misread as a completed yield; it keeps going.
func TestWorkPassTODOWithoutBlockDoesNotComplete(t *testing.T) {
	ctx := context.Background()
	g, rp, _, planPath, _ := gateFixture(t, false)
	subs, _ := rp.Submodules()
	sel := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}

	cl := &mockClient{sess: &mockSession{onTurn: func(turn int) {
		os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= -->\nunchanged\n"), 0o644)
	}}}
	r := &Runner{Repo: rp, Git: g, Client: cl, MaxTurns: 2, WallCap: time.Hour, TTL: time.Hour}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Completed {
		t.Fatalf("an unblocked, unchanged TODO must not complete as a yield: %+v", res)
	}
}

// TestRealRunVerifyClassifies pins the real exec path (used when RunVerify is nil):
// a clean exit is green, a command that RUNS but exits non-zero is a red (exitErr),
// and a binary that cannot be executed at all is an infra error.
func TestRealRunVerifyClassifies(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if o, err := realRunVerify(ctx, dir, "go", "version"); err != nil || o.exitErr {
		t.Fatalf("`go version` must be green: outcome=%+v err=%v", o, err)
	}
	if o, err := realRunVerify(ctx, dir, "go", "beehive-not-a-subcommand"); err != nil || !o.exitErr {
		t.Fatalf("an unknown go subcommand must be a red (ran, non-zero): outcome=%+v err=%v", o, err)
	}
	if _, err := realRunVerify(ctx, dir, "beehive-no-such-binary-xyz"); err == nil {
		t.Fatalf("a missing binary must surface an infra error, got nil")
	}
}

// predicateHardStopSession models a single turn that chains two tool calls: the
// first (deliver) commits the task's terminal status flip + change doc — i.e. the
// exact predicate r.complete checks — and the second (recorded via secondCallRan)
// is a further, unneeded tool call the agent should never have been solicited for
// once the runner observes the first has met the predicate. It waits on ctx
// between the two calls so the test can assert the mid-turn hard-stop watchdog
// cancels turnCtx (and so never lets the second call run) instead of only
// noticing the completion at the top of the NEXT turn.
type predicateHardStopSession struct {
	deliver       func()
	secondCallRan *atomic.Bool
}

func (s *predicateHardStopSession) Prompt(ctx context.Context, text string) (string, error) {
	s.deliver()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(2 * time.Second):
	}
	s.secondCallRan.Store(true)
	return "", nil
}
func (s *predicateHardStopSession) Messages(ctx context.Context) ([]Message, error) { return nil, nil }
func (s *predicateHardStopSession) Close() error                                    { return nil }

// TestPredicateHardStopCancelsTurnMidStream proves the primary fix: the runner
// watches for the task's completion predicate WHILE a turn is still in flight and
// hard-cancels the turn ctx the instant it is met, instead of waiting for the
// between-turn check — so a chained tool call the agent attempts AFTER delivering
// the terminal flip never executes, the pass still finalizes with the correct
// terminal status, and no error/warning is produced (the cancellation is treated
// as a normal settle, not a fault).
func TestPredicateHardStopCancelsTurnMidStream(t *testing.T) {
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

	secondCallRan := &atomic.Bool{}
	sess := &predicateHardStopSession{
		deliver: func() {
			os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
			os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
		},
		secondCallRan: secondCallRan,
	}
	r := &Runner{
		Repo: rp, Git: g, Client: fixedClient{sess: sess}, MaxTurns: 5,
		WallCap: time.Hour, TTL: time.Hour, PredicatePoll: 5 * time.Millisecond,
	}
	res, err := r.Run(context.Background(), sel, "sys", "first")
	if err != nil {
		t.Fatalf("a mid-turn predicate hard-stop must not be fatal, got: %v", err)
	}
	if !res.Completed || res.GCMarked || res.Lost {
		t.Fatalf("want a clean completion, got %+v", res)
	}
	if res.Warning != "" {
		t.Fatalf("a predicate hard-stop is a normal settle, want no warning, got %q", res.Warning)
	}
	if secondCallRan.Load() {
		t.Fatalf("the second tool call ran after the terminal flip — the mid-turn predicate hard-stop failed to cancel the turn in time")
	}
}

// TestPredicateHardStopNoSpuriousCancel is the negative control: a turn that
// NEVER reaches the completion predicate (the task never leaves its working
// status) must run to its normal per-turn ceiling unchanged — the watchdog must
// never fire on a task that genuinely has not delivered.
func TestPredicateHardStopNoSpuriousCancel(t *testing.T) {
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
		Repo: rp, Git: g, Client: fixedClient{sess: blockingSession{}}, MaxTurns: 5,
		WallCap: time.Hour, TTL: time.Hour,
		TurnTimeout: 50 * time.Millisecond, PredicatePoll: 5 * time.Millisecond,
	}
	res, err := r.Run(ctx, sel, "sys", "first")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.GCMarked {
		t.Fatalf("want the normal TurnTimeout abandon, unaffected by the predicate watchdog: %+v", res)
	}
	if !contains(res.Warning, "per-turn ceiling") {
		t.Fatalf("want the ordinary TurnTimeout warning, got %q", res.Warning)
	}
}

// reportLine returns the checklist line (from a continuationReport) whose text
// contains sub, or "" if none. Lines are the "  [x] label" entries.
func reportLine(report, sub string) string {
	for _, ln := range strings.Split(report, "\n") {
		if strings.Contains(ln, sub) && strings.Contains(ln, "[") {
			return ln
		}
	}
	return ""
}

// TestContinuationReportEnumeratesPredicates proves the between-turn prompt the
// runner sends (nextPrompt/continuationReport) is NOT the bare "continue" but a
// per-kind checklist of the completion predicates each marked met/unmet, computed
// from the SAME source as the deterministic completion check (completionChecklist,
// which complete() reduces over). It also exercises the accept criterion that a
// line flips unmet->met when the underlying artifact is produced, and the negative
// control: the bare-"continue" text does not satisfy the assertions.
func TestContinuationReportEnumeratesPredicates(t *testing.T) {
	root := t.TempDir()
	g := gitInit(t, root)
	repo.Init(root)
	sm := filepath.Join(root, "submodules", "sm")
	os.MkdirAll(filepath.Join(sm, "docs"), 0o755)
	os.WriteFile(filepath.Join(sm, "ROI.md"), []byte("intent\n"), 0o644)
	planPath := filepath.Join(sm, "PLAN.md")
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	ctx := context.Background()
	g.Commit(ctx, "seed")
	roiHead, _ := g.LastCommit(ctx, "submodules/sm/ROI.md")

	rp, _ := repo.Open(root)
	subs, _ := rp.Submodules()
	r := &Runner{Repo: rp, Git: g}

	// --- Work kind: two predicates, both initially unmet. ---
	work := &selectt.Selection{Kind: selectt.Work, Submodule: subs[0], Task: plan.Task{ID: "T1", Status: plan.TODO}}
	rep := r.nextPrompt(work, "bee-T1")
	// Negative control: a bare "continue" would fail every assertion below.
	if rep == "continue" {
		t.Fatal("work continuation prompt must be the checklist report, not the bare \"continue\"")
	}
	if !strings.HasPrefix(rep, "continue.") {
		t.Fatalf("report must still lead with a continue instruction, got:\n%s", rep)
	}
	statusLine := reportLine(rep, "terminal STATUS set")
	docLine := reportLine(rep, "change doc present at submodules/sm/docs/bee-T1-T1.md")
	if statusLine == "" || docLine == "" {
		t.Fatalf("work report missing the terminal-status and/or change-doc predicate:\n%s", rep)
	}
	if !strings.Contains(statusLine, "[ ]") || !strings.Contains(docLine, "[ ]") {
		t.Fatalf("both work predicates should read UNMET at the start:\n%s", rep)
	}
	// The report agrees with the deterministic completion check.
	if done, _ := r.complete(work, "bee-T1"); done {
		t.Fatal("complete() must be false while both work predicates are unmet")
	}

	// Produce the change doc: its predicate line flips unmet -> met, the status
	// line stays unmet, and completion is still not reached.
	os.WriteFile(filepath.Join(sm, "docs", "bee-T1-T1.md"), []byte("doc"), 0o644)
	rep = r.nextPrompt(work, "bee-T1")
	if l := reportLine(rep, "change doc present"); !strings.Contains(l, "[x]") {
		t.Fatalf("change-doc line must flip to MET once the doc exists, got: %q", l)
	}
	if l := reportLine(rep, "terminal STATUS set"); !strings.Contains(l, "[ ]") {
		t.Fatalf("status line must stay UNMET while PLAN is TODO, got: %q", l)
	}
	if done, _ := r.complete(work, "bee-T1"); done {
		t.Fatal("complete() must stay false while the status predicate is unmet")
	}

	// Flip the status terminal: now every line is met and complete() agrees.
	os.WriteFile(planPath, []byte("## T1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	rep = r.nextPrompt(work, "bee-T1")
	if strings.Contains(rep, "[ ]") {
		t.Fatalf("all work predicates should read MET now:\n%s", rep)
	}
	if done, err := r.complete(work, "bee-T1"); err != nil || !done {
		t.Fatalf("a met-all report must coincide with completion: done=%v err=%v", done, err)
	}

	// --- Review kind: single predicate, flips when the task leaves NEEDS-REVIEW. ---
	os.WriteFile(planPath, []byte("## R1 [NEEDS-REVIEW] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	review := &selectt.Selection{Kind: selectt.Review, Submodule: subs[0], Task: plan.Task{ID: "R1", Status: plan.NeedsReview}}
	rep = r.nextPrompt(review, "bee-R1")
	if l := reportLine(rep, "NEEDS-REVIEW"); !strings.Contains(l, "[ ]") {
		t.Fatalf("review predicate must be UNMET while the task sits at NEEDS-REVIEW:\n%s", rep)
	}
	os.WriteFile(planPath, []byte("## R1 [DONE] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	rep = r.nextPrompt(review, "bee-R1")
	if l := reportLine(rep, "NEEDS-REVIEW"); !strings.Contains(l, "[x]") {
		t.Fatalf("review predicate must flip to MET once the task left NEEDS-REVIEW:\n%s", rep)
	}

	// --- Reconcile kind: single ROI-stamp predicate. ---
	recon := &selectt.Selection{Kind: selectt.Reconcile, Submodule: subs[0]}
	os.WriteFile(planPath, []byte("## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644) // no stamp
	rep = r.nextPrompt(recon, "")
	if l := reportLine(rep, "ROI stamp"); !strings.Contains(l, "[ ]") {
		t.Fatalf("reconcile predicate must be UNMET with no matching ROI stamp:\n%s", rep)
	}
	os.WriteFile(planPath, []byte("<!-- Beehive-ROI: "+roiHead[:12]+" -->\n## T1 [TODO] <!-- attempts=0 deps= -->\ngo\n"), 0o644)
	rep = r.nextPrompt(recon, "")
	if l := reportLine(rep, "ROI stamp"); !strings.Contains(l, "[x]") {
		t.Fatalf("reconcile predicate must flip to MET once the stamp matches ROI HEAD:\n%s", rep)
	}
	if done, err := r.complete(recon, ""); err != nil || !done {
		t.Fatalf("reconcile met report must coincide with completion: done=%v err=%v", done, err)
	}
}
