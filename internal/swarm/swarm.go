// Package swarm runs one honeybee: create a per-branch worktree, open one
// opencode session (AGENTS.md system prompt + first prompt, cwd=worktree),
// deterministically check completion each turn, send "continue" until met or a
// turn/wall-clock cap, then either delete the worktree on terminal or mark the
// task for GC. No controller; the session carries context across turns.
package swarm

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spencerharmon/beehive/internal/claim"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// Session is one opencode conversation; context persists across Prompt calls.
// Prompt blocks until the assistant turn finishes and returns its reply text.
// Messages returns the full structured history (user/assistant/reasoning/tools)
// so the recorder can render a live transcript without re-driving the model.
type Session interface {
	Prompt(ctx context.Context, text string) (string, error)
	Messages(ctx context.Context) ([]Message, error)
	Close() error
}

// Client opens opencode sessions. Open creates a session at cwd with the given
// system prompt but sends no message yet, so callers can start recording before
// the (often long) first turn runs.
type Client interface {
	Open(ctx context.Context, cwd, system string) (Session, error)
}

// Runner drives a single honeybee turn loop.
type Runner struct {
	Repo     *repo.Repo
	Git      *git.Repo // beehive worktree (this honeybee's isolated checkout)
	Client   Client
	MaxTurns int
	WallCap  time.Duration
	TTL      time.Duration
	Now      func() time.Time
	Debug    io.Writer // non-nil: stream session activity live
	// Publish, when set, advances main to this honeybee's worktree branch and
	// pushes (conflict-free merge of distinct files). Called after each heartbeat
	// and at session end so peers converge "as we go". Nil in tests = local only.
	Publish func(context.Context) error
	// Remote is the beehive repo's push remote ("" = local only). BaseMain is the
	// main-branch SHA this honeybee branched from at startup. Together they let
	// each turn detect that an operator deleted the plan or removed this task on
	// main after the honeybee started, and exit instead of working a dead task.
	Remote   string
	BaseMain string

	// Session transcripts are recorded in a SEPARATE beehive worktree/branch so
	// they never share an index with the agent's PLAN.md/docs edits (an
	// out-of-band session commit would otherwise be clobbered by the agent's next
	// index commit, and vice versa). SessionGit/SessionRoot are that worktree;
	// SessionPublish advances main with it. Distinct file paths (sessions/ vs
	// plan/docs) merge conflict-free. Nil/"" in tests = no session worktree (the
	// transcript is still written to disk, just not committed/published).
	SessionGit     *git.Repo
	SessionRoot    string
	SessionPublish func(context.Context) error
}

// syncSession commits the live session transcript in the dedicated session
// worktree and publishes it to main, so the beehived UI shows the session while
// it runs. The session worktree has exactly one writer (this recorder), so a
// plain path-scoped commit is safe — no index contention with the agent.
func (r *Runner) syncSession(ctx context.Context, sid, rel string) {
	if r.SessionGit == nil {
		return // tests without a session worktree: file is on disk, not committed
	}
	_ = r.SessionGit.CommitPaths(ctx, "session: "+sid+"\n\nBeehive: session "+sid, rel)
	if r.SessionPublish != nil {
		_ = r.SessionPublish(ctx)
	}
}

func (r *Runner) publish(ctx context.Context) error {
	if r.Publish == nil {
		return nil
	}
	return r.Publish(ctx)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Result reports how a honeybee ended.
type Result struct {
	Completed bool
	Turns     int
	GCMarked  bool
	Branch    string
	SessionID string
	Warning   string // non-empty when the run aborted (e.g. task removed on main)
}

// branchFor names the worktree branch and doc stem for a task selection.
func branchFor(sel *selectt.Selection) string {
	switch sel.Kind {
	case selectt.Bootstrap:
		return "bee-bootstrap"
	case selectt.Reconcile:
		return "bee-reconcile"
	default:
		return "bee-" + sel.Task.ID
	}
}

// Run executes the loop for one selection. It claims work tasks, creates a
// worktree (Work only), runs turns until completion or caps, and tidies up.
func (r *Runner) Run(ctx context.Context, sel *selectt.Selection, system, first string) (Result, error) {
	res := Result{Branch: branchFor(sel)}
	absRoot, err := filepath.Abs(r.Repo.Root)
	if err != nil {
		return res, err
	}

	// Only a main Work task edits the submodule repo and needs a worktree.
	// Bootstrap/reconcile only touch beehive-layer files (PLAN.md, docs).
	var wg *git.Repo
	wtRel := filepath.Join("..", "worktrees", res.Branch)
	if sel.Kind == selectt.Work {
		wg = git.New(sel.Submodule.RepoDir())
		if err := wg.WorktreeAdd(ctx, wtRel, res.Branch, "HEAD"); err != nil {
			return res, fmt.Errorf("worktree add: %w", err)
		}
	}

	// Context preamble: the agent works from the beehive repo root and must use
	// the right paths. ROI/PLAN/docs live in the beehive layer, code in the worktree.
	preamble := fmt.Sprintf(
		"# Context\nYou are working from the beehive repo root (cwd). Submodule: %s.\n"+
			"Coordination files: submodules/%s/ROI.md (read-only), submodules/%s/PLAN.md, submodules/%s/docs/.\n"+
			"Code worktree (for Work tasks): submodules/%s/worktrees/%s.\n"+
			"Act autonomously: do not ask for confirmation; make the edits and commits the protocol requires.\n\n",
		sel.Submodule.Name, sel.Submodule.Name, sel.Submodule.Name, sel.Submodule.Name,
		sel.Submodule.Name, res.Branch)
	first = preamble + first

	if r.Debug != nil {
		fmt.Fprintf(r.Debug, "[honeybee] dir=%s submodule=%s kind=%s opening session...\n", absRoot, sel.Submodule.Name, sel.Kind)
	}
	sess, err := r.Client.Open(ctx, absRoot, system)
	if err != nil {
		return res, fmt.Errorf("open session: %w", err)
	}
	defer sess.Close()

	// Always-on recorder: one poller streams the session transcript to the repo
	// (submodules/<sm>/sessions/<branch>.md) and, when Debug is set, tees live
	// activity (reasoning, tool commands + output) to stderr. beehived reads the
	// repo file, so opencode is polled exactly once regardless of UI viewers.
	recCtx, recStop := context.WithCancel(ctx)
	sid := SessionID(res.Branch, r.now())
	res.SessionID = sid
	// The session transcript lives at submodules/<sm>/sessions/<sid>.md. It is
	// written and committed in the dedicated SESSION worktree (SessionRoot), never
	// the agent's beehive worktree, so session commits and PLAN.md/docs commits
	// stay on separate branches. Tests without a session worktree fall back to the
	// agent worktree path and skip committing.
	sessionRel := filepath.Join("submodules", sel.Submodule.Name, "sessions", sid+".md")
	sessionFile := filepath.Join(sel.Submodule.SessionsDir(), sid+".md")
	if r.SessionRoot != "" {
		sessionFile = filepath.Join(r.SessionRoot, sessionRel)
	}
	rec := &recorder{
		sess:     sess,
		path:     sessionFile,
		header:   fmt.Sprintf("# session %s\n\nsubmodule: %s · kind: %s · branch: %s\n", sid, sel.Submodule.Name, sel.Kind, res.Branch),
		debug:    r.Debug,
		flush:    func(c context.Context) { r.syncSession(c, sid, sessionRel) },
		flushIvl: 2 * time.Second,
		toolSt:   map[string]string{},
		partLen:  map[string]int{},
		started:  map[string]bool{},
	}
	recDone := make(chan struct{})
	go func() { rec.loop(recCtx); close(recDone) }()

	cl := &claim.Claimer{Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now}
	deadline := r.now().Add(r.WallCap)
	prompt := first
	// finish stops the recorder, optionally records an abort warning to the
	// session file (so it shows in the UI), then commits+publishes the session
	// (its own worktree) and publishes the agent's branch.
	finish := func(warning string) {
		recStop()
		<-recDone
		rec.snapshot(ctx) // final flush after the last turn settles
		if warning != "" {
			rec.appendWarning(warning)
			if r.Debug != nil {
				fmt.Fprintf(r.Debug, "\n⚠️  %s\n", warning)
			}
		}
		r.syncSession(ctx, sid, sessionRel)
		_ = r.publish(ctx)
	}
	for res.Turns = 1; res.Turns <= r.MaxTurns; res.Turns++ {
		// Guard: an operator may have deleted the plan or removed this task on main
		// after we started. Detect it and exit rather than work a task nobody wants.
		if removed, warning, err := r.taskRemoved(ctx, sel); err == nil && removed {
			res.Warning = warning
			finish(warning)
			if wg != nil {
				_ = wg.WorktreeRemove(ctx, wtRel)
			}
			return res, nil
		}
		if sel.Kind == selectt.Work {
			if err := cl.Heartbeat(ctx, sel.Task.ID, r.now()); err != nil {
				finish("")
				return res, fmt.Errorf("turn %d heartbeat: %w", res.Turns, err)
			}
			_ = r.publish(ctx) // surface the heartbeat to peers as we go
		}
		// Drive the turn. Turn 1 sends the seeded `first` prompt; the recorder is
		// already polling, so even the long bootstrap turn streams to the UI/debug.
		if _, err := sess.Prompt(ctx, prompt); err != nil {
			finish("")
			return res, fmt.Errorf("turn %d prompt: %w", res.Turns, err)
		}
		done, err := r.complete(sel, res.Branch)
		if err != nil {
			finish("")
			return res, err
		}
		if done {
			res.Completed = true
			finish("")
			if wg != nil {
				_ = wg.WorktreeRemove(ctx, wtRel)
			}
			return res, nil
		}
		if r.now().After(deadline) {
			break
		}
		prompt = "continue"
	}
	res.GCMarked = true // turn/wall cap hit, leave IN-PROGRESS heartbeat for GC
	finish("")
	return res, nil
}

// taskRemoved checks whether the operator deleted the plan or removed this
// honeybee's task on the beehive's main branch after the honeybee started. It
// pulls the remote (if any), and only if main advanced and PLAN.md actually
// changed does it re-read the plan: a missing PLAN.md or a missing task means
// this honeybee should stop. Transient git/network errors are non-fatal (the
// run continues) so a blip never kills a healthy honeybee. Work tasks only.
func (r *Runner) taskRemoved(ctx context.Context, sel *selectt.Selection) (bool, string, error) {
	if sel.Kind != selectt.Work || r.BaseMain == "" {
		return false, "", nil
	}
	ref := "main"
	if r.Remote != "" {
		if err := r.Git.Fetch(ctx, r.Remote, "main"); err != nil {
			return false, "", err
		}
		ref = r.Remote + "/main"
	}
	cur, err := r.Git.RevParse(ctx, ref)
	if err != nil {
		return false, "", err
	}
	if cur == r.BaseMain {
		return false, "", nil // nothing changed since we started
	}
	planRel := "submodules/" + sel.Submodule.Name + "/" + repo.PlanFile
	changed, err := r.Git.DiffPaths(ctx, r.BaseMain, cur, planRel)
	if err != nil {
		return false, "", err
	}
	if !changed {
		return false, "", nil
	}
	content, err := r.Git.Show(ctx, ref, planRel)
	if err != nil {
		return true, fmt.Sprintf(
			"PLAN.md for %s was deleted on %s after this honeybee started; task %s no longer exists. Exiting.",
			sel.Submodule.Name, ref, sel.Task.ID), nil
	}
	p, err := plan.Parse(content)
	if err != nil {
		return false, "", err
	}
	if p.Find(sel.Task.ID) == nil {
		return true, fmt.Sprintf(
			"task %s was removed from %s PLAN.md on %s after this honeybee started. Exiting.",
			sel.Task.ID, sel.Submodule.Name, ref), nil
	}
	return false, "", nil
}

// complete is the deterministic per-turn completion check.
func (r *Runner) complete(sel *selectt.Selection, branch string) (bool, error) {
	switch sel.Kind {
	case selectt.Bootstrap:
		_, err := os.Stat(sel.Submodule.PlanPath())
		return err == nil, nil
	case selectt.Reconcile:
		return r.reconciled(sel)
	default:
		return r.workDone(sel, branch)
	}
}

func (r *Runner) reconciled(sel *selectt.Selection) (bool, error) {
	roiPath := "submodules/" + sel.Submodule.Name + "/" + repo.ROIFile
	head, err := r.Git.LastCommit(context.Background(), roiPath)
	if err != nil {
		return false, err
	}
	stamp, err := sel.Submodule.ROIStamp()
	if err != nil {
		return false, err
	}
	return stamp != "" && stamp == head, nil
}

// workDone verifies PLAN.md status transitioned terminal, the heartbeat ts is
// cleared, and the branch+task doc exists under submodule docs/.
func (r *Runner) workDone(sel *selectt.Selection, branch string) (bool, error) {
	b, err := os.ReadFile(sel.Submodule.PlanPath())
	if err != nil {
		return false, err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return false, err
	}
	t := p.Find(sel.Task.ID)
	if t == nil {
		return false, nil
	}
	terminal := t.Status == plan.Done || t.Status == plan.NeedsReview ||
		t.Status == plan.TODO || t.Status == plan.NeedsArb
	if !terminal || !t.Heartbeat.IsZero() {
		return false, nil
	}
	return r.docPresent(sel, branch)
}

func (r *Runner) docPresent(sel *selectt.Selection, branch string) (bool, error) {
	dir := filepath.Join(sel.Submodule.Path, "docs")
	stem := branch + "-" + sel.Task.ID
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, e := range ents {
		if !e.IsDir() && pathHasPrefix(e.Name(), stem) {
			return true, nil
		}
	}
	return false, nil
}

func pathHasPrefix(name, stem string) bool {
	return len(name) >= len(stem) && name[:len(stem)] == stem
}
