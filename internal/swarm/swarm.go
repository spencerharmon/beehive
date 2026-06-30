// Package swarm runs one honeybee: create a per-branch worktree, open one
// opencode session (AGENTS.md system prompt + first prompt, cwd=worktree),
// deterministically check completion each turn, send "continue" until met or a
// turn/wall-clock cap, then either delete the worktree on terminal or mark the
// task for GC. No controller; the session carries context across turns.
package swarm

import (
	"context"
	"errors"
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
	// Session is this honeybee's unique claim token, stamped on the task it works
	// so peers can tell a task is actively held (session + fresh heartbeat) versus
	// free. Must match the token main used for the initial Claim.
	Session string
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

	// Session transcripts stream as RAPID commits to an isolated per-session
	// branch (SessionGit is the worktree on SessionBranch); SessionPush, when set,
	// pushes that branch to the remote each commit so a beehived on another host
	// can read it. Nothing touches main mid-session. While running, main holds a
	// STUB at the session path naming the branch (the branch's first commit, so
	// the end merge can't conflict on the file); at session end the branch is
	// squashed and SessionPublish merges the durable final transcript to main
	// once. Distinct file paths (sessions/ vs plan/docs) merge conflict-free.
	// Nil/"" in tests = transcript on disk only, not committed/streamed.
	SessionGit     *git.Repo
	SessionRoot    string
	SessionBranch  string
	SessionPublish func(context.Context) error
	SessionPush    func(context.Context) error

	// RestoreConfig, when set, reverts any change to the beehive repo's git config
	// (remotes) that the agent introduced during a turn. git config is shared
	// across all worktrees, so a `git remote add` an agent runs in its worktree
	// leaks into the live repo and corrupts repo-rooted readers (the editor cuts
	// edit worktrees from origin/main). The runner calls it at the top of every
	// turn so drift never persists past the turn that caused it. Nil in tests.
	RestoreConfig func(context.Context)

	// TurnTimeout bounds a single agent turn (one opencode Prompt). A stalled
	// session is canceled at this cap and the task is abandoned for GC, instead of
	// the honeybee wedging on a dead HTTP call until the systemd RuntimeMaxSec
	// backstop. 0 = no per-turn cap (tests).
	TurnTimeout time.Duration
}

// streamSession commits the current transcript to the isolated session branch
// and, when distributed, pushes that branch to the remote — so a beehived
// (possibly on another host) reading the branch sees the session in near real
// time. No merge to main here; that happens once at session end. Best-effort:
// a failed push never fails the run.
func (r *Runner) streamSession(ctx context.Context, rel string) {
	if r.SessionGit == nil {
		return
	}
	if err := r.SessionGit.CommitPaths(ctx, "session: stream", rel); err != nil {
		return // ErrNothing (no change) or a transient error: skip this tick
	}
	if r.SessionPush != nil {
		_ = r.SessionPush(ctx)
	}
}

// startSession plants the stub on main (the session branch's first commit, so
// the end merge can't conflict on the file) and returns the stub commit to
// squash onto. No-op (returns "") when there is no session worktree (tests).
func (r *Runner) startSession(ctx context.Context, file, rel string) string {
	if r.SessionGit == nil || r.SessionRoot == "" || r.SessionBranch == "" {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return ""
	}
	if err := os.WriteFile(file, []byte(repo.SessionStub(r.SessionBranch)), 0o644); err != nil {
		return ""
	}
	if err := r.SessionGit.CommitPaths(ctx, "session: start "+r.SessionBranch, rel); err != nil {
		return ""
	}
	stub, err := r.SessionGit.Head(ctx)
	if err != nil {
		return ""
	}
	if r.SessionPublish != nil {
		_ = r.SessionPublish(ctx) // land the stub on main so the session is discoverable
	}
	if r.SessionPush != nil {
		_ = r.SessionPush(ctx)
	}
	return stub
}

// finalizeSession squashes the rapid streaming commits down to a single commit
// on top of the stub (keeping main history clean) and merges the durable final
// transcript to main once. stub is the commit startSession returned; "" (tests /
// no session worktree) makes this a no-op.
func (r *Runner) finalizeSession(ctx context.Context, sid, rel, stub string) {
	if r.SessionGit == nil || stub == "" {
		return
	}
	// Collapse stub..HEAD into one commit: --soft keeps the final file staged.
	if _, err := r.SessionGit.Run(ctx, "reset", "--soft", stub); err != nil {
		return
	}
	if err := r.SessionGit.CommitPaths(ctx, "session: "+sid+"\n\nBeehive: session "+sid, rel); err != nil && err != git.ErrNothing {
		return
	}
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
	Lost      bool // lost the claim race; the caller should reselect another task
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
	var wtAbs string
	if sel.Kind == selectt.Work {
		// Absolute paths rooted at THIS honeybee's worktree (absRoot). A fresh
		// `git worktree add` of the beehive repo does NOT populate submodules, so
		// submodules/<sm>/repo is an empty gitlink. If we ran `git worktree add`
		// against it directly, git would ascend to the superproject and silently
		// create a bogus SUPERPROJECT worktree at the source path (the exact
		// confusion that wastes an agent's whole run). Initialize the submodule
		// checkout first, then branch the source worktree off it.
		repoDir := sel.Submodule.RepoDir()
		wtAbs = filepath.Join(sel.Submodule.WorktreesDir(), res.Branch)
		if !isSourceCheckout(ctx, repoDir) {
			rel, relErr := filepath.Rel(absRoot, repoDir)
			if relErr != nil {
				return res, fmt.Errorf("resolve submodule path: %w", relErr)
			}
			if _, err := r.Git.Run(ctx, "submodule", "update", "--init", "--", rel); err != nil {
				return res, fmt.Errorf("init submodule %s checkout: %w", sel.Submodule.Name, err)
			}
		}
		if !isSourceCheckout(ctx, repoDir) {
			return res, fmt.Errorf("submodule %s not checked out at %s; refusing to create a worktree that would corrupt the superproject",
				sel.Submodule.Name, repoDir)
		}
		wg = git.New(repoDir)
		// Sync the submodule checkout to the tracked-branch tip BEFORE branching, so
		// the worktree starts from the live code rather than the (possibly stale)
		// recorded pointer. Advancing the beehive pointer to the synced tip is
		// automatic (no review). A no-remote install (and most tests) keeps the
		// recorded pointer and branches off HEAD as before.
		if err := r.syncWorktreeBase(ctx, wg, sel.Submodule, absRoot); err != nil {
			return res, err
		}
		if err := wg.WorktreeAdd(ctx, wtAbs, res.Branch, "HEAD"); err != nil {
			return res, fmt.Errorf("worktree add: %w", err)
		}
	}

	// Context preamble: shipped in the binary (NOT the on-disk AGENTS.md, which is
	// frozen at `beehive init` time), so it stays accurate as the tool evolves. It
	// is kind-specific: a Work agent gets the code-worktree handoff; a Review or
	// Arbitrate agent is told to JUDGE existing work (and is given the implementer
	// branch + docs) so it never re-implements a NEEDS-REVIEW task.
	smName := sel.Submodule.Name
	var preamble string
	switch sel.Kind {
	case selectt.Review:
		preamble = fmt.Sprintf(
			"# Context (REVIEW — judge existing work, do NOT reimplement, do NOT set IN-PROGRESS)\n"+
				"cwd is the beehive repo root. Submodule: %[1]s. Task under review: %[2]s.\n"+
				"Beehive layer (read/write on main): submodules/%[1]s/PLAN.md, submodules/%[1]s/docs/. ROI.md is read-only.\n"+
				"Implementer's work is on branch bee-%[2]s in submodules/%[1]s/repo — inspect read-only via git "+
				"(fetch from origin if the branch is absent locally). Change doc: submodules/%[1]s/docs/bee-%[2]s-%[2]s.md; "+
				"the PLAN.md task body has a `Review:` note.\n"+
				"APPROVE -> merge the submodule pointer bump + PLAN.md task DONE + unlock dependents. "+
				"REJECT -> PLAN.md task NEEDS-ARBITRATION + rejection doc submodules/%[1]s/docs/%[2]s-review-reject.md.\n"+
				"The run completes when the task leaves NEEDS-REVIEW. Act autonomously.\n\n",
			smName, sel.Task.ID)
	case selectt.Arbitrate:
		preamble = fmt.Sprintf(
			"# Context (ARBITRATION — settle the dispute, do NOT reimplement, do NOT set IN-PROGRESS)\n"+
				"cwd is the beehive repo root. Submodule: %[1]s. Task in arbitration: %[2]s.\n"+
				"Beehive layer (read/write on main): submodules/%[1]s/PLAN.md, submodules/%[1]s/docs/. ROI.md is read-only.\n"+
				"Implementer branch bee-%[2]s in submodules/%[1]s/repo; change doc submodules/%[1]s/docs/bee-%[2]s-%[2]s.md; "+
				"reviewer rejection doc submodules/%[1]s/docs/%[2]s-review-reject.md.\n"+
				"SIDE WITH IMPLEMENTER -> merge pointer bump + PLAN.md DONE + unlock dependents. "+
				"SIDE WITH REVIEWER -> PLAN.md TODO (or NEEDS-HUMAN past the limit) with the binding rationale.\n"+
				"The run completes when the task leaves NEEDS-ARBITRATION. Act autonomously.\n\n",
			smName, sel.Task.ID)
	case selectt.Work:
		preamble = fmt.Sprintf(
			"# Context\nYou are working from the beehive repo root (cwd). Submodule: %[1]s.\n"+
				"Coordination files (the beehive layer): submodules/%[1]s/ROI.md (read-only), "+
				"submodules/%[1]s/PLAN.md, submodules/%[1]s/docs/.\n"+
				"Code worktree (already created and checked out for you): submodules/%[1]s/worktrees/%[2]s/ "+
				"on branch %[2]s. Edit the submodule's CODE there; never write submodules/%[1]s/repo (the shared checkout).\n"+
				"On completion of a Work task: PLAN.md -> NEEDS-REVIEW on main; commit the code on branch %[2]s "+
				"with a `Beehive: %[3]s <doc-path>` stamp and ensure that commit is pushed to the submodule's origin; "+
				"bump the submodule pointer.\n"+
				"REQUIRED change doc path: submodules/%[1]s/docs/%[2]s-%[3]s.md (the beehive layer — NOT inside the code "+
				"worktree). The runner's completion check looks for it exactly there; a doc elsewhere reads as 'not done'.\n"+
				"Act autonomously: do not ask for confirmation; make the edits and commits the protocol requires.\n\n",
			smName, res.Branch, taskID(sel))
	default: // Bootstrap, Reconcile: beehive-layer only, no code worktree.
		preamble = fmt.Sprintf(
			"# Context\nYou are working from the beehive repo root (cwd). Submodule: %[1]s.\n"+
				"Beehive layer: submodules/%[1]s/ROI.md (read-only), submodules/%[1]s/PLAN.md, submodules/%[1]s/docs/.\n"+
				"Act autonomously: do not ask for confirmation; make the edits and commits the protocol requires.\n\n",
			smName)
	}
	if hasTask(sel) {
		preamble += fmt.Sprintf(
			"Claim: the runner stamped this task session=%[1]s and re-stamps it each turn. Before doing work "+
				"each turn, confirm submodules/%[2]s/PLAN.md still shows session=%[1]s on task %[3]s with a fresh "+
				"heartbeat. If a DIFFERENT session holds it, STOP immediately — you lost the race and the runner "+
				"will reselect. Do not edit the session/heartbeat yourself.\n\n",
			r.Session, smName, sel.Task.ID)
	}
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
	// The transcript streams as rapid commits to the isolated session branch (via
	// the session worktree), pushed to the remote when distributed; beehived reads
	// the branch. Fall back to the submodule's sessions dir on disk when there is
	// no session worktree (tests).
	sessionRel := filepath.Join("submodules", sel.Submodule.Name, "sessions", sid+".md")
	sessionFile := filepath.Join(sel.Submodule.SessionsDir(), sid+".md")
	if r.SessionRoot != "" {
		sessionFile = filepath.Join(r.SessionRoot, sessionRel)
	}
	rec := &recorder{
		sess:    sess,
		path:    sessionFile,
		header:  fmt.Sprintf("# session %s\n\nsubmodule: %s · kind: %s · branch: %s\n", sid, sel.Submodule.Name, sel.Kind, res.Branch),
		debug:   r.Debug,
		toolSt:  map[string]string{},
		partLen: map[string]int{},
		started: map[string]bool{},
	}
	if r.SessionGit != nil {
		rec.commit = func(c context.Context) { r.streamSession(c, sessionRel) }
		rec.commitIvl = time.Second
	}
	// Plant the stub on main and capture the squash base BEFORE the recorder starts
	// overwriting the file with the transcript.
	stubCommit := r.startSession(ctx, sessionFile, sessionRel)
	recDone := make(chan struct{})
	go func() { rec.loop(recCtx); close(recDone) }()

	cl := &claim.Claimer{
		Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now,
		Session: r.Session, Publish: r.Publish, Remote: r.Remote,
	}
	deadline := r.now().Add(r.WallCap)
	prompt := first
	// finish stops the recorder, optionally records an abort warning to the
	// transcript (so it shows in the UI), commits the final transcript to the
	// session branch, then squashes+merges the durable final to main once and
	// publishes the agent's branch.
	finish := func(warning string) error {
		recStop()
		<-recDone
		rec.snapshot(ctx) // final flush after the last turn settles
		if warning != "" {
			rec.appendWarning(warning)
			if r.Debug != nil {
				fmt.Fprintf(r.Debug, "\n⚠️  %s\n", warning)
			}
		}
		r.streamSession(ctx, sessionRel) // commit final transcript (incl. warning) to the branch
		r.finalizeSession(ctx, sid, sessionRel, stubCommit)
		// Publish the work to main and RETURN the result. A failure means the change
		// never landed on main; callers that treat the task as complete MUST gate on
		// this so a rejected publish can never read as DONE. Always surfaced to the log.
		ferr := r.publish(ctx)
		if ferr != nil && r.Debug != nil {
			fmt.Fprintf(r.Debug, "\n⚠️  publish to main failed: %v\n", ferr)
		}
		return ferr
	}
	cleanup := func() {
		if wg != nil {
			_ = wg.WorktreeRemove(ctx, wtAbs)
		}
	}
	for res.Turns = 1; res.Turns <= r.MaxTurns; res.Turns++ {
		// Revert any change the agent made to the repo's git config (remotes) during
		// the previous turn before doing more work. Honeybees publish via worktree
		// merges to main but must never touch the shared repo config; this keeps that
		// invariant turn-by-turn so a stray remote never outlives the turn.
		if r.RestoreConfig != nil {
			r.RestoreConfig(ctx)
		}
		// Guard: an operator may have deleted the plan or removed this task on main
		// after we started. Detect it and exit rather than work a task nobody wants.
		if removed, warning, err := r.taskRemoved(ctx, sel); err == nil && removed {
			res.Warning = warning
			finish(warning)
			cleanup()
			return res, nil
		}
		// Unified per-turn heartbeat for every task-bearing kind (Work/Review/
		// Arbitrate). It re-stamps our session, after first pulling main so we observe
		// a competitor or a resolution. Three outcomes beyond "ok":
		if hasTask(sel) {
			err := cl.Heartbeat(ctx, sel.Task.ID, sel.Task.Status, r.now())
			switch {
			case err == nil:
				// Ownership re-confirmed; the heartbeat already published to peers.
			case errors.Is(err, claim.ErrResolved):
				// The task left its working status during the previous turn (the agent
				// drove it terminal, or someone else resolved it). Re-check completion
				// and exit cleanly — never a fatal error.
				done, derr := r.complete(sel, res.Branch)
				if derr != nil {
					finish("")
					return res, derr
				}
				if done {
					// Publish first; only release + report complete if main advanced.
					if ferr := finish(""); ferr != nil {
						res.GCMarked = true
						res.Warning = fmt.Sprintf(
							"task %s reached completion locally but publishing to main failed: %v; left unreleased for retry",
							sel.Task.ID, ferr)
						cleanup()
						return res, nil
					}
					res.Completed = true
					if w := r.pushSourceBranch(ctx, wg, res.Branch); w != "" {
						res.Warning = w
					}
					_ = cl.Release(ctx, sel.Task.ID)
					cleanup()
					return res, nil
				}
				res.Warning = fmt.Sprintf(
					"task %s left %s but the completion check failed — left for review",
					sel.Task.ID, sel.Task.Status)
				finish(res.Warning)
				cleanup()
				return res, nil
			case errors.Is(err, claim.ErrLost):
				// Another session won the task. Stop now so the honeybee process
				// reselects the next most useful task instead of wasting turns on a
				// redundant pass. (Double-guarded: the agent is also instructed to stop
				// when it sees a foreign session, ending the turn early on its own.)
				res.Lost = true
				res.Warning = fmt.Sprintf("lost the claim race for %s to another session; reselecting", sel.Task.ID)
				finish(res.Warning)
				cleanup()
				return res, nil
			default:
				finish("")
				return res, fmt.Errorf("turn %d heartbeat: %w", res.Turns, err)
			}
		}
		// Drive the turn. Turn 1 sends the seeded `first` prompt; the recorder is
		// already polling, so even the long bootstrap turn streams to the UI/debug.
		// Bound the turn: a stalled opencode call is canceled at TurnTimeout and the
		// task abandoned for GC, never a multi-hour zombie blocked on a dead socket.
		turnCtx := ctx
		cancelTurn := func() {}
		if r.TurnTimeout > 0 {
			turnCtx, cancelTurn = context.WithTimeout(ctx, r.TurnTimeout)
		}
		_, perr := sess.Prompt(turnCtx, prompt)
		timedOut := r.TurnTimeout > 0 && turnCtx.Err() == context.DeadlineExceeded
		cancelTurn()
		if perr != nil {
			if timedOut {
				res.GCMarked = true
				res.Warning = fmt.Sprintf("turn %d exceeded the %s per-turn timeout (stalled agent); abandoning for GC", res.Turns, r.TurnTimeout)
				finish(res.Warning)
				cleanup()
				return res, nil
			}
			finish("")
			return res, fmt.Errorf("turn %d prompt: %w", res.Turns, perr)
		}
		done, err := r.complete(sel, res.Branch)
		if err != nil {
			finish("")
			return res, err
		}
		if done {
			// Completion is only real once the work lands on main. Publish first; if it
			// fails, do NOT release the claim or report Completed — leave the task
			// claimed (stale -> GC -> retry) so the work is re-driven, never silently
			// dropped as a phantom DONE.
			if ferr := finish(res.Warning); ferr != nil {
				res.GCMarked = true
				res.Warning = fmt.Sprintf(
					"task %s reached completion locally but publishing to main failed: %v; left unreleased for retry",
					sel.Task.ID, ferr)
				cleanup()
				return res, nil
			}
			res.Completed = true
			if w := r.pushSourceBranch(ctx, wg, res.Branch); w != "" {
				res.Warning = w
			}
			if hasTask(sel) {
				_ = cl.Release(ctx, sel.Task.ID)
			}
			cleanup()
			return res, nil
		}
		if r.now().After(deadline) {
			break
		}
		prompt = "continue"
	}
	// Turn/wall cap hit: the agent never reached completion. Mirror the DONE path
	// and reclaim the orphaned code worktree (cleanup -> wg.WorktreeRemove) so
	// stale trees don't accumulate and a future `git worktree add` for this
	// branch/dir doesn't collide. DELIBERATELY leave the task's status and its
	// (now going-stale) session+heartbeat claim untouched: there is no IN-PROGRESS
	// status under the unified claim model, so that lingering claim is exactly the
	// signal stale-claim GC uses to reclaim/re-TODO the task. We must NOT Release
	// here (that clears the claim and would hide the abandonment) and must NOT flip
	// status. cleanup() only removes the worktree dir; it never writes PLAN.md.
	res.GCMarked = true
	finish("")
	cleanup()
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
	case selectt.Review:
		// Done once the reviewer moved the task out of NEEDS-REVIEW (-> DONE on
		// approve, -> NEEDS-ARBITRATION on reject).
		return r.statusLeft(sel, plan.NeedsReview)
	case selectt.Arbitrate:
		// Done once the arbiter moved the task out of NEEDS-ARBITRATION (-> DONE,
		// -> TODO, or -> NEEDS-HUMAN).
		return r.statusLeft(sel, plan.NeedsArb)
	default:
		return r.workDone(sel, branch)
	}
}

// statusLeft reports whether the selected task has moved out of `from`. A task
// removed from the plan counts as not-our-completion (the removed-guard handles
// deletions). Used by review/arbitration, whose resolution is purely a PLAN.md
// state transition rather than a new change doc.
func (r *Runner) statusLeft(sel *selectt.Selection, from plan.Status) (bool, error) {
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
	return t.Status != from, nil
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
	// The PLAN.md ROI stamp is often abbreviated while head is the full %H sha,
	// so an exact compare ~never matches and reconcile reports "never done".
	// Match by prefix (mirrors select.reconcileRange's check) so a short stamp
	// that prefixes the full head clears the reconcile, firing exactly once.
	return stamp != "" && strings.HasPrefix(head, stamp), nil
}

// workDone verifies the PLAN.md status transitioned to a terminal/handoff state
// for a Work task and the branch+task change doc exists under submodule docs/.
// The runner's own session/heartbeat stamp is NOT a completion signal (it is
// released separately), so it is not checked here.
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
		t.Status == plan.NeedsArb
	if !terminal {
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

// taskID is the stable id used in the change-doc stem and Context preamble.
func taskID(sel *selectt.Selection) string {
	switch sel.Kind {
	case selectt.Bootstrap, selectt.Reconcile:
		return string(sel.Kind)
	default:
		return sel.Task.ID
	}
}

// hasTask reports whether the selection carries a concrete PLAN task that must be
// claimed and heartbeated (Work/Review/Arbitrate). Bootstrap/Reconcile operate on
// PLAN.md itself and hold no task claim.
func hasTask(sel *selectt.Selection) bool {
	switch sel.Kind {
	case selectt.Work, selectt.Review, selectt.Arbitrate:
		return true
	default:
		return false
	}
}

// syncWorktreeBase brings the submodule checkout to the tracked-branch tip
// before the code worktree branches off it, so a honeybee always starts from the
// live code instead of the (possibly stale) recorded pointer. It fetches the
// tracked branch (from .gitmodules submodule.<path>.branch, default "main") and
// hard-resets the checkout to origin/<branch>, then advances the beehive pointer
// to the synced tip by committing the bumped gitlink in the honeybee's beehive
// worktree (r.Git). That pointer move is reviewless: the tip already lives on the
// submodule's origin, so the bumped pointer never dangles. A no-remote checkout
// (single-host install, most tests) is a no-op: the recorded pointer stands and
// the worktree branches off HEAD as before.
func (r *Runner) syncWorktreeBase(ctx context.Context, wg *git.Repo, sub repo.Submodule, absRoot string) error {
	rem, err := wg.Remote(ctx)
	if err != nil {
		return fmt.Errorf("submodule %s remote: %w", sub.Name, err)
	}
	if rem == "" {
		return nil // no remote: nothing to sync, keep the recorded pointer
	}
	rel, err := filepath.Rel(absRoot, sub.RepoDir())
	if err != nil {
		return fmt.Errorf("resolve submodule %s path: %w", sub.Name, err)
	}
	branch := r.trackedBranch(ctx, rel)
	if err := wg.Fetch(ctx, rem, branch); err != nil {
		return fmt.Errorf("fetch submodule %s %s/%s: %w", sub.Name, rem, branch, err)
	}
	if err := wg.HardReset(ctx, rem+"/"+branch); err != nil {
		return fmt.Errorf("sync submodule %s to %s/%s: %w", sub.Name, rem, branch, err)
	}
	// Advance the beehive pointer to the synced tip (no review). Commit only the
	// gitlink path so unrelated working-tree state is untouched; ErrNothing means
	// the recorded pointer already matched the tip (no move to commit).
	tip, err := wg.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve synced %s tip: %w", sub.Name, err)
	}
	msg := fmt.Sprintf("beehive: sync %s worktree base to tracked tip %s", sub.Name, tip)
	if err := r.Git.CommitPaths(ctx, msg, rel); err != nil && !errors.Is(err, git.ErrNothing) {
		return fmt.Errorf("commit synced %s pointer: %w", sub.Name, err)
	}
	return nil
}

// trackedBranch returns the submodule's tracked branch from .gitmodules
// (submodule.<path>.branch), defaulting to "main" when the key is unset or
// .gitmodules is absent (e.g. a test's bare nested checkout).
func (r *Runner) trackedBranch(ctx context.Context, rel string) string {
	out, err := r.Git.Run(ctx, "config", "-f", ".gitmodules", "--get", "submodule."+rel+".branch")
	if err == nil {
		if b := strings.TrimSpace(out); b != "" {
			return b
		}
	}
	return "main"
}

// isSourceCheckout reports whether dir is the root of its OWN git work tree (a
// populated submodule checkout) rather than an empty gitlink. For an empty
// gitlink, `git rev-parse --show-toplevel` ascends to the parent superproject,
// so comparing the resolved top-level to dir correctly rejects it — preventing a
// `git worktree add` from corrupting the superproject.
func isSourceCheckout(ctx context.Context, dir string) bool {
	top, err := git.New(dir).Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return false
	}
	a, err1 := filepath.EvalSymlinks(top)
	b, err2 := filepath.EvalSymlinks(dir)
	if err1 != nil || err2 != nil {
		return top == dir
	}
	return a == b
}

// pushSourceBranch publishes the agent's source-branch commit to the submodule's
// origin so the bumped submodule pointer resolves for every other host/bee — a
// pointer naming a commit that lives only in this honeybee's local clone is
// worthless to peers. Best-effort: a missing remote is a no-op (local install),
// and a push failure is returned as a warning, never a hard run failure.
func (r *Runner) pushSourceBranch(ctx context.Context, wg *git.Repo, branch string) string {
	if wg == nil {
		return ""
	}
	rem, err := wg.Remote(ctx)
	if err != nil || rem == "" {
		return ""
	}
	if err := wg.Push(ctx, rem, branch); err != nil {
		return fmt.Sprintf("source branch %s was NOT pushed to %s (%v); the submodule pointer will dangle until pushed", branch, rem, err)
	}
	return ""
}
