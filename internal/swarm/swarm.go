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
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/claim"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
)

// Session is one opencode conversation; context persists across Prompt calls.
// Prompt blocks until the assistant turn finishes and returns its reply text.
type Session interface {
	Prompt(ctx context.Context, text string) (string, error)
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
	sess, reply, err := r.Client.NewSession(ctx, absRoot, system, first)
	if err != nil {
		return res, fmt.Errorf("open session: %w", err)
	}
	defer sess.Close()

	var log strings.Builder
	r.record(&log, sel, 1, first, reply)

	cl := &claim.Claimer{Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now}
	deadline := r.now().Add(r.WallCap)
	prompt := first
	for res.Turns = 1; res.Turns <= r.MaxTurns; res.Turns++ {
		if sel.Kind == selectt.Work {
			if err := cl.Heartbeat(ctx, sel.Task.ID, r.now()); err != nil {
				return res, fmt.Errorf("turn %d heartbeat: %w", res.Turns, err)
			}
		}
		if res.Turns > 1 {
			rep, err := sess.Prompt(ctx, prompt)
			if err != nil {
				return res, fmt.Errorf("turn %d prompt: %w", res.Turns, err)
			}
			r.record(&log, sel, res.Turns, prompt, rep)
		}
		done, err := r.complete(sel, res.Branch)
		if err != nil {
			return res, err
		}
		if done {
			res.Completed = true
			r.persist(ctx, sel, res.Branch, log.String())
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
	r.persist(ctx, sel, res.Branch, log.String())
	return res, nil
}

// record appends a turn to the transcript and streams it live when Debug is set.
func (r *Runner) record(log *strings.Builder, sel *selectt.Selection, turn int, prompt, reply string) {
	fmt.Fprintf(log, "\n## turn %d\n\n> %s\n\n%s\n", turn, prompt, reply)
	if r.Debug != nil {
		// Live assistant output (text + tool calls) streams via the opencode
		// SSE event tap, so only echo the turn header + prompt here.
		fmt.Fprintf(r.Debug, "\n=== turn %d ===\n> %s\n", turn, prompt)
	}
}

// persist records the session transcript in the beehive repo, committed to main.
func (r *Runner) persist(ctx context.Context, sel *selectt.Selection, branch, body string) {
	dir := filepath.Join(sel.Submodule.Path, "docs", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f := filepath.Join(dir, branch+".md")
	if err := os.WriteFile(f, []byte("# session "+branch+"\n"+body), 0o644); err != nil {
		return
	}
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
