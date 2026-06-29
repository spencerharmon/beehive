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

// Client opens opencode sessions. NewSession seeds system+first prompt at cwd
// and returns the assistant's first reply.
type Client interface {
	NewSession(ctx context.Context, cwd, system, first string) (Session, string, error)
}

// Runner drives a single honeybee turn loop.
type Runner struct {
	Repo     *repo.Repo
	Git      *git.Repo // beehive repo root
	Client   Client
	MaxTurns int
	WallCap  time.Duration
	TTL      time.Duration
	Now      func() time.Time
	Debug    io.Writer // non-nil: stream session activity live
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
	sess, _, err := r.Client.NewSession(ctx, absRoot, system, first)
	if err != nil {
		return res, fmt.Errorf("open session: %w", err)
	}
	defer sess.Close()

	// Always-on recorder: one poller streams the session transcript to the repo
	// (submodules/<sm>/sessions/<branch>.md) and, when Debug is set, tees live
	// activity (reasoning, tool commands + output) to stderr. beehived reads the
	// repo file, so opencode is polled exactly once regardless of UI viewers.
	recCtx, recStop := context.WithCancel(ctx)
	rec := &recorder{
		sess:    sess,
		path:    filepath.Join(sel.Submodule.SessionsDir(), res.Branch+".md"),
		header:  fmt.Sprintf("# session %s\n\nsubmodule: %s · kind: %s\n", res.Branch, sel.Submodule.Name, sel.Kind),
		debug:   r.Debug,
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	recDone := make(chan struct{})
	go func() { rec.loop(recCtx); close(recDone) }()

	cl := &claim.Claimer{Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now}
	deadline := r.now().Add(r.WallCap)
	prompt := first
	finish := func() {
		recStop()
		<-recDone
		rec.snapshot(ctx) // final flush after the last turn settles
		r.commitSession(ctx, res.Branch)
	}
	for res.Turns = 1; res.Turns <= r.MaxTurns; res.Turns++ {
		if sel.Kind == selectt.Work {
			if err := cl.Heartbeat(ctx, sel.Task.ID, r.now()); err != nil {
				finish()
				return res, fmt.Errorf("turn %d heartbeat: %w", res.Turns, err)
			}
		}
		if res.Turns > 1 {
			if _, err := sess.Prompt(ctx, prompt); err != nil {
				finish()
				return res, fmt.Errorf("turn %d prompt: %w", res.Turns, err)
			}
		}
		done, err := r.complete(sel, res.Branch)
		if err != nil {
			finish()
			return res, err
		}
		if done {
			res.Completed = true
			finish()
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
	finish()
	return res, nil
}

// commitSession commits the recorded transcript file to main. Best-effort: a
// nothing-to-commit is not an error worth failing the run over.
func (r *Runner) commitSession(ctx context.Context, branch string) {
	_ = r.Git.Commit(ctx, "session: "+branch+"\n\nBeehive: session "+branch)
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
