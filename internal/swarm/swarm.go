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

// modelSelector is an OPTIONAL Client capability: pick the model for the upcoming
// session at dispatch time. The runner uses it to route a pass to a cheap or
// strong model per task kind (Runner.ModelFor). A Client that does not implement
// it (the test fakes) keeps whatever model it was constructed with, so routing is
// purely additive.
type modelSelector interface {
	SetModel(model string)
}

// Runner drives a single honeybee turn loop.
type Runner struct {
	Repo     *repo.Repo
	Git      *git.Repo // beehive worktree (this honeybee's isolated checkout)
	Client   Client
	MaxTurns int
	// MergeRetries bounds publish conflict resolution: on a merge conflict the
	// runner has the agent resolve it, then retries the publish, up to this many
	// attempts before deferring the task to a later honeybee. 0 -> default 8.
	MergeRetries int
	WallCap      time.Duration
	TTL          time.Duration
	Now          func() time.Time
	Debug        io.Writer // non-nil: stream session activity live
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

	// LeanInject trims the per-pass injected system prompt to only what this pass's
	// kind acts on (trimProtocol) and defers the Work completion rule from the
	// up-front preamble to an at-decision-point "continue" hint (nextPrompt). Off
	// by default so the injected set is byte-identical to the historical path; the
	// runner flips it on from an env flag (see cmd/honeybee). The protocol is
	// re-sent on every turn, so trimming compounds across a session.
	LeanInject bool

	// LeanContext bounds the per-turn context the runner hands a Work agent to
	// (changed-file diffs + a rolling transcript summary) instead of the bare
	// "continue" that leaves the agent to re-read the same files and re-scan the
	// whole prior transcript every turn (ROI Priority 1 — "feed diffs, not full
	// re-reads"; "rolling summary of prior turns"). Off by default: the per-turn
	// prompt is the byte-identical "continue"/lean hint (nextPrompt) and no extra
	// session poll runs. The runner flips it on from an env flag (see cmd/honeybee).
	// A compression of the working context only — the authoritative transcript still
	// streams verbatim to the session branch.
	LeanContext bool

	// LeanBrief injects a runner-precomputed task brief on a Work dispatch (ROI
	// Priority 1 — "Precompute in the runner: resolve the worktree, branch, and
	// pointers; hand the agent the file excerpts it needs; make the deterministic
	// choices for it"). The brief carries the resolved worktree/branch, the
	// submodule pointer + tracked tip, the task's own PLAN card, the mandated
	// change-doc path and commit stamp, and head excerpts of the task's own files,
	// so the agent does not re-run discovery git plumbing or scan the whole
	// submodule tree to orient. Off by default so the injected preamble stays
	// byte-identical to the historical path; the runner flips it on from an env
	// flag (see cmd/honeybee). Additive — the floor, not a cage.
	LeanBrief bool

	// ModelFor selects the agent model for a pass by its task kind (the layered
	// config's per-kind override, falling through to the single Model). When it is
	// set AND the Client implements modelSelector, the runner routes each dispatch
	// to the returned model — so near-deterministic kinds (reconcile/review/
	// arbitrate) run on a cheap model while code Work runs on the strong one. Nil,
	// or a "" return, is inert: the client's preconfigured model stands, byte-
	// identical to the single-model path.
	ModelFor func(kind string) string

	// StallTurns bounds idle churn: if a Work pass produces an identical code-
	// worktree fingerprint (HEAD + porcelain status) for this many CONSECUTIVE
	// turns after the first — an agent talking without changing a file — the runner
	// abandons the pass for GC instead of burning the rest of the turn/wall budget.
	// 0 = off (the default), so a host that has not opted in is unaffected.
	StallTurns int

	// Progress overrides the per-turn progress fingerprint the stall detector
	// observes (tests inject a deterministic sequence). Nil = the real signal: the
	// agent's code worktree HEAD + porcelain status. Only consulted when
	// StallTurns > 0.
	Progress func(context.Context) string

	// BuildEnv is the resolved host build/test environment (e.g. CGO_ENABLED=0 +
	// root-fs GOTMPDIR/TMPDIR/GOCACHE) the runner OWNS so no honeybee re-derives it
	// (audit session-audit-001 F1: ~150-190 turns/window of pure env rediscovery).
	// It is (a) EXPORTED into the honeybee process env at agent spawn so build/test
	// subprocesses the honeybee itself spawns inherit it, and (b) STATED once in the
	// injected preamble as the mandated Go invocation for task-bearing kinds. Both
	// levers read this one map (see buildenv.go) so the export and the stated line
	// never drift. Sourced from config.Config.BuildEnv (see cmd/honeybee). Empty
	// (the default) = inert: no export, byte-identical preamble.
	BuildEnv map[string]string
	// ExportEnv applies BuildEnv to the process environment at agent spawn. The
	// injectable seam: nil runs the real os.Setenv loop; tests set it to capture the
	// exported map without mutating the real process env.
	ExportEnv func(map[string]string)
}

// streamSession commits the current transcript to the isolated session branch
// and, when distributed, pushes that branch to the remote — so a beehived
// (possibly on another host) reading the branch sees the session in near real
// time. No merge to main here; that happens once at session end. Periodic
// recorder calls ignore errors; finalization surfaces them so branch deletion is
// gated on a real transcript copy.
func (r *Runner) streamSession(ctx context.Context, rel string) error {
	if r.SessionGit == nil {
		return nil
	}
	if err := r.SessionGit.CommitPaths(ctx, "session: stream", rel); err != nil {
		if err != git.ErrNothing {
			return err
		}
		// A previous stream commit may have succeeded locally while its push failed.
		// Push even with no new commit so finalization failure never strands the only
		// transcript copy on an unadvertised local branch.
		if r.SessionPush != nil {
			return r.SessionPush(ctx)
		}
		return nil
	}
	if r.SessionPush != nil {
		return r.SessionPush(ctx)
	}
	return nil
}

// startSession plants the stub on main (the session branch's first commit, so
// the end merge can't conflict on the file) and returns the stub commit to
// squash onto. No-op (returns "", nil) when there is no session worktree (tests).
func (r *Runner) startSession(ctx context.Context, file, rel string) (string, error) {
	if r.SessionGit == nil || r.SessionRoot == "" || r.SessionBranch == "" {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", fmt.Errorf("create session directory: %w", err)
	}
	if err := os.WriteFile(file, []byte(repo.SessionStub(r.SessionBranch)), 0o644); err != nil {
		return "", fmt.Errorf("write session stub: %w", err)
	}
	if err := r.SessionGit.CommitPaths(ctx, "session: start "+r.SessionBranch, rel); err != nil {
		return "", fmt.Errorf("commit session stub: %w", err)
	}
	stub, err := r.SessionGit.RevParse(ctx, "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve session stub commit: %w", err)
	}
	if r.SessionPublish != nil {
		if err := r.SessionPublish(ctx); err != nil {
			return "", fmt.Errorf("publish session stub: %w", err)
		}
	}
	if r.SessionPush != nil {
		if err := r.SessionPush(ctx); err != nil {
			return "", fmt.Errorf("push session branch: %w", err)
		}
	}
	return stub, nil
}

// finalizeSession squashes the rapid streaming commits down to a single commit
// on top of the stub (keeping main history clean) and merges the durable final
// transcript to main once. stub is the commit startSession returned; "" (tests /
// no session worktree) makes this a no-op.
func (r *Runner) finalizeSession(ctx context.Context, sid, rel, stub string) error {
	if r.SessionGit == nil || stub == "" {
		return nil
	}
	// Collapse stub..HEAD into one commit: --soft keeps the final file staged.
	if _, err := r.SessionGit.Run(ctx, "reset", "--soft", stub); err != nil {
		return fmt.Errorf("reset session branch to stub: %w", err)
	}
	if err := r.SessionGit.CommitPaths(ctx, "session: "+sid+"\n\nBeehive: session "+sid, rel); err != nil && err != git.ErrNothing {
		return fmt.Errorf("commit final session transcript: %w", err)
	}
	if r.SessionPublish != nil {
		if err := r.SessionPublish(ctx); err != nil {
			return fmt.Errorf("publish final session transcript: %w", err)
		}
	}
	return nil
}

type sessionTranscriptError struct{ err error }

func (e sessionTranscriptError) Error() string { return e.err.Error() }
func (e sessionTranscriptError) Unwrap() error { return e.err }

func isSessionTranscriptError(err error) bool {
	var st sessionTranscriptError
	return errors.As(err, &st)
}

func (r *Runner) publish(ctx context.Context) error {
	if r.Publish == nil {
		return nil
	}
	return r.Publish(ctx)
}

// publishWithResolution lands the work on main, using the agent to resolve merge
// conflicts git cannot (concurrent honeybee work, or an external/non-honeybee
// commit to the submodule or hive that no lock can prevent). Deterministic first:
// r.publish merges + pushes and auto-resolves non-conflicting races itself. On a
// real conflict it reproduces the merge in the work worktree, hands the conflicted
// paths to the agent, commits the resolution, and retries — bounded by
// MergeRetries (default 8). If new changes keep interfering past the bound it gives
// up CLEANLY: the task is left unfinished (not marked done; the stale claim GCs) so
// a LATER honeybee resumes where this one stopped, e.g. once a human or non-honeybee
// agent has finished editing. Never a wedge, never silent, no tokens spent on an
// unrecoverable state. A nil session (tests/no agent) or a non-conflict failure
// falls through to the deterministic result unchanged.
func (r *Runner) publishWithResolution(ctx context.Context, sess Session) error {
	n := r.MergeRetries
	if n <= 0 {
		n = 8
	}
	for attempt := 0; attempt < n; attempt++ {
		err := r.publish(ctx)
		if err == nil {
			return nil
		}
		if sess == nil || !errors.Is(err, git.ErrConflict) {
			return err // no agent, or a non-conflict failure: defer as before
		}
		if rerr := r.resolveConflict(ctx, sess); rerr != nil {
			// Not resolvable this pass (agent failed, left markers, or a gitlink
			// conflict needing a submodule merge): defer cleanly for a later pass.
			return errors.Join(err, rerr)
		}
		// Resolved and committed on the branch; loop and retry the publish.
	}
	return fmt.Errorf("publish did not converge after %d attempts (concurrent/external changes still interfering); left for a later honeybee to finish: %w", n, git.ErrConflict)
}

// resolveConflict reproduces the publish merge in the work worktree and drives the
// agent to resolve it, committing a clean merge. Returns nil once resolved (or the
// race cleared with no conflict); returns an error when it is not resolvable this
// pass. A submodule-gitlink conflict is a deferred submodule merge (see
// docs/conflict-resolution.md), not a text resolution, so it is not attempted here.
func (r *Runner) resolveConflict(ctx context.Context, sess Session) error {
	ref := "main"
	if r.Remote != "" {
		if err := r.Git.Fetch(ctx, r.Remote, "main"); err != nil {
			return err
		}
		ref = r.Remote + "/main"
	}
	switch err := r.Git.Merge(ctx, ref); {
	case err == nil:
		return nil // race cleared; caller retries the push
	case !errors.Is(err, git.ErrConflict):
		return err
	}
	// A submodule-gitlink conflict (index mode 160000) cannot be resolved by editing
	// text; it needs a merge inside the submodule. Out of scope here — defer.
	if raw, _ := r.Git.Run(ctx, "ls-files", "-u"); strings.Contains(raw, "160000 ") {
		_ = r.Git.AbortMerge(ctx)
		return fmt.Errorf("submodule-gitlink conflict needs a submodule merge (deferred; see docs/conflict-resolution.md)")
	}
	conflicts, err := r.Git.UnmergedPaths(ctx)
	if err != nil {
		_ = r.Git.AbortMerge(ctx)
		return err
	}
	turnCtx := ctx
	if r.TurnTimeout > 0 {
		var cancel context.CancelFunc
		turnCtx, cancel = context.WithTimeout(ctx, r.TurnTimeout)
		defer cancel()
	}
	if _, perr := sess.Prompt(turnCtx, conflictResolutionPrompt(conflicts)); perr != nil {
		_ = r.Git.AbortMerge(ctx)
		return fmt.Errorf("agent conflict-resolution turn failed: %w", perr)
	}
	// Stage ONLY the conflicted paths (scope the merge commit to the resolution),
	// then refuse to commit if anything is still unmerged or still marker-laden.
	if _, err := r.Git.Run(ctx, append([]string{"add", "--"}, conflicts...)...); err != nil {
		_ = r.Git.AbortMerge(ctx)
		return err
	}
	if rem, _ := r.Git.UnmergedPaths(ctx); len(rem) > 0 {
		_ = r.Git.AbortMerge(ctx)
		return fmt.Errorf("agent left %s unresolved", strings.Join(rem, ","))
	}
	if marked, _ := r.Git.HasConflictMarkers(ctx, conflicts); marked {
		_ = r.Git.AbortMerge(ctx)
		return fmt.Errorf("agent left conflict markers in %s", strings.Join(conflicts, ","))
	}
	if _, err := r.Git.Run(ctx, "commit", "--no-edit"); err != nil {
		_ = r.Git.AbortMerge(ctx)
		return err
	}
	return nil
}

// conflictResolutionPrompt tells the agent to resolve an in-progress merge on its
// branch. The runner — not the agent — commits and publishes, so the agent only
// rewrites the conflicted files to a correct combined merge.
func conflictResolutionPrompt(conflicts []string) string {
	return "STOP the current task. A concurrent change landed on main and conflicts with your branch. " +
		"A merge of main into your working branch is IN PROGRESS with git conflict markers in: " +
		strings.Join(conflicts, ", ") + ". " +
		"Resolve every conflict so each file is the correct combination of BOTH changes — keep the other " +
		"change's work, never delete it just to clear the conflict. Remove all conflict markers " +
		"(<<<<<<<, =======, >>>>>>>). Do NOT commit, push, or run git merge/reset/abort — the runner commits " +
		"and publishes. When every listed file is a clean, correct merge, end your turn."
}

// sweepOrphanWorktreeGitlinks removes any orphan gitlink that a prior pass leaked
// under submodules/<sm>/worktrees/ from this honeybee's beehive index and
// publishes the removal to main, so a committed code-worktree (a gitlink with no
// .gitmodules entry that wedges `git submodule update`) self-heals on the next
// pass instead of festering until an operator runs cleanup. It is a strict
// superset-safe GC: it only ever drops a stray *worktree* gitlink (never a
// declared submodule) and only from the index — the live worktree files on disk
// are untouched, so a peer actively using that worktree is unaffected. A no-op
// (no orphans) costs a single `git ls-files`. Failures are surfaced to the caller,
// which treats them as non-fatal (a transient publish race must not kill a
// healthy honeybee; the next pass re-sweeps).
func (r *Runner) sweepOrphanWorktreeGitlinks(ctx context.Context) error {
	orphans, err := r.Git.OrphanWorktreeGitlinks(ctx)
	if err != nil {
		return err
	}
	if len(orphans) == 0 {
		return nil
	}
	if err := r.Git.RemoveCached(ctx, orphans...); err != nil {
		return err
	}
	msg := "beehive: drop orphan worktree gitlink(s) " + strings.Join(orphans, ", ") +
		"\n\nBeehive: gc orphan-worktree-gitlink"
	if err := r.Git.CommitStaged(ctx, msg); err != nil {
		if errors.Is(err, git.ErrNothing) {
			return nil
		}
		return err
	}
	return r.publish(ctx)
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
	// SessionPublished is true once final session transcript replaced live stub on
	// main. Callers may delete stream branch only when this is true; otherwise that
	// branch is only remaining transcript source.
	SessionPublished bool
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

	// Self-heal before doing anything else: drop any orphan code-worktree gitlink a
	// prior pass leaked into the beehive index (a committed submodules/<sm>/
	// worktrees/<branch> gitlink with no .gitmodules entry wedges `git submodule
	// update`). This runs before this pass creates its OWN code worktree, so the
	// index is a clean projection of main and the removal commit records nothing
	// but the orphan drop. Non-fatal: a transient failure must not kill a healthy
	// honeybee — the next pass re-sweeps — so it is logged, not returned.
	if err := r.sweepOrphanWorktreeGitlinks(ctx); err != nil && r.Debug != nil {
		fmt.Fprintf(r.Debug, "[honeybee] orphan-worktree-gitlink sweep: %v\n", err)
	}

	// Reconcile dedup guard (defense-in-depth for the selection->dispatch window):
	// select.reconcileRange already pulls+prefix-checks before PICKING a reconcile,
	// but a concurrent pass can fold+stamp+push the same ROI delta between our
	// selection and now. Re-pull main so we judge against the freshest published
	// stamp, then short-circuit via the SAME prefix compare (Runner.reconciled): if
	// PLAN.md's Beehive-ROI stamp already prefixes the current ROI head, the delta is
	// already applied — report complete WITHOUT opening a session (zero agent turns,
	// zero tokens), instead of spawning one of the zero-progress reconcile passes the
	// session audit flagged. Genuine ROI drift (stamp does NOT prefix head) falls
	// through and runs the reconcile normally.
	if sel.Kind == selectt.Reconcile {
		if err := r.refreshMain(ctx); err != nil && r.Debug != nil {
			fmt.Fprintf(r.Debug, "[honeybee] reconcile pre-check pull failed; checking local PLAN stamp: %v\n", err)
		}
		done, err := r.reconciled(sel)
		if err != nil {
			return res, err
		}
		if done {
			res.Completed = true
			if r.Debug != nil {
				fmt.Fprintf(r.Debug, "[honeybee] reconcile already applied for %s (PLAN stamp prefixes ROI head); skipping session\n", sel.Submodule.Name)
			}
			return res, nil
		}
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
			"# Context (REVIEW — judge existing work, do NOT reimplement)\n"+
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
			"# Context (ARBITRATION — settle the dispute, do NOT reimplement)\n"+
				"cwd is the beehive repo root. Submodule: %[1]s. Task in arbitration: %[2]s.\n"+
				"Beehive layer (read/write on main): submodules/%[1]s/PLAN.md, submodules/%[1]s/docs/. ROI.md is read-only.\n"+
				"Implementer branch bee-%[2]s in submodules/%[1]s/repo; change doc submodules/%[1]s/docs/bee-%[2]s-%[2]s.md; "+
				"reviewer rejection doc submodules/%[1]s/docs/%[2]s-review-reject.md.\n"+
				"SIDE WITH IMPLEMENTER -> merge pointer bump + PLAN.md DONE + unlock dependents. "+
				"SIDE WITH REVIEWER -> PLAN.md TODO with the binding rationale; if a concrete operator blocker is exposed, "+
				"run beehive task human %[1]s %[2]s --reason \"<specific blocker>\".\n"+
				"The run completes when the task leaves NEEDS-ARBITRATION. Act autonomously.\n\n",
			smName, sel.Task.ID)
	case selectt.Work:
		// The completion rule (flip to NEEDS-REVIEW, commit+push with the Beehive
		// stamp, bump the pointer) is a static "what to do at the end" dump. In lean
		// mode it is dropped from the up-front preamble and fired instead as an
		// at-decision-point hint on the "continue" turn where the change doc is still
		// missing (nextPrompt); its authoritative copy still lives in the retained
		// Work task section. Default (off): the full sentence stays in place, keeping
		// the injected brief byte-identical to the historical path.
		onComplete := ""
		if !r.LeanInject {
			onComplete = fmt.Sprintf(
				"On completion of a Work task: PLAN.md -> NEEDS-REVIEW on main; commit the code on branch %[1]s "+
					"with a `Beehive: %[2]s <doc-path>` stamp and ensure that commit is pushed to the submodule's origin; "+
					"bump the submodule pointer.\n",
				res.Branch, taskID(sel))
		}
		preamble = fmt.Sprintf(
			"# Context\nYou are working from the beehive repo root (cwd). Submodule: %[1]s.\n"+
				"Coordination files (the beehive layer): submodules/%[1]s/ROI.md (read-only), "+
				"submodules/%[1]s/PLAN.md, submodules/%[1]s/docs/.\n"+
				"Code worktree (already created and checked out for you): submodules/%[1]s/worktrees/%[2]s/ "+
				"on branch %[2]s. Edit the submodule's CODE there; never write submodules/%[1]s/repo (the shared checkout).\n"+
				"%[4]s"+
				"REQUIRED change doc path: submodules/%[1]s/docs/%[2]s-%[3]s.md (the beehive layer — NOT inside the code "+
				"worktree). The runner's completion check looks for it exactly there; a doc elsewhere reads as 'not done'.\n"+
				"Act autonomously: do not ask for confirmation; make the edits and commits the protocol requires.\n\n",
			smName, res.Branch, taskID(sel), onComplete)
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
				"will reselect. Do not edit the session/heartbeat yourself. If you hit a concrete blocker requiring "+
				"operator input, run: beehive task human %[2]s %[3]s --reason \"<specific blocker and exact input needed>\". "+
				"Use exact status NEEDS-HUMAN; never write HUMAN-NEEDED.\n\n",
			r.Session, smName, sel.Task.ID)
	}
	// Told-once build/test environment: task-bearing kinds (Work/Review/Arbitrate)
	// all build/test the submodule's code, so state the host-mandated Go invocation
	// the runner owns (config BuildEnv) once, up front, instead of paying ~150-190
	// turns/window for every honeybee to re-derive it (audit F1). Bootstrap/Reconcile
	// touch only PLAN.md and never carry it. Inert (empty BuildEnv ⇒ "") so the
	// injected preamble is byte-identical to the historical path when unconfigured.
	if hasTask(sel) {
		preamble += buildEnvPreamble(r.BuildEnv)
	}
	// Precomputed task brief (Work only): hand the agent the worktree/branch/pointer
	// the setup already resolved, the deterministic doc-path/commit-stamp, its PLAN
	// card, and head excerpts of its own files — so it skips discovery plumbing and
	// a whole-tree scan. Inert by default (LeanBrief off) so the injected preamble
	// is byte-identical to the historical path.
	if r.LeanBrief && sel.Kind == selectt.Work {
		preamble += r.buildTaskBrief(ctx, sel, wg, wtAbs, absRoot, res.Branch).render()
	}
	first = preamble + first

	if r.Debug != nil {
		fmt.Fprintf(r.Debug, "[honeybee] dir=%s submodule=%s kind=%s opening session...\n", absRoot, sel.Submodule.Name, sel.Kind)
	}
	// Lean mode: trim the injected protocol to only what this pass's kind acts on
	// before opening the session. The system prompt is re-sent on EVERY turn, so
	// trimming it once here compounds across the whole session. Off by default =
	// the injected system is byte-identical to the historical full protocol.
	if r.LeanInject {
		system = trimProtocol(system, sel.Kind)
	}
	// Per-kind model routing (opt-in). When the operator configured a model for
	// this pass's kind (layered config -> ModelFor) and the Client can select one,
	// route the dispatch to it before the session opens: a near-deterministic kind
	// (reconcile/review/arbitrate) can run on a cheap model while real code Work
	// runs on the strong one. Inert when ModelFor is nil or returns "" (no routing
	// configured): the client keeps its preconfigured model, byte-identical to the
	// single-model path.
	if r.ModelFor != nil {
		if ms, ok := r.Client.(modelSelector); ok {
			if m := r.ModelFor(string(sel.Kind)); m != "" {
				ms.SetModel(m)
			}
		}
	}
	// Export the host build/test env into this honeybee's process at agent spawn,
	// so any build/test subprocess the honeybee itself spawns inherits it (the
	// stated preamble line, above, is the lever for opencode's sibling bash tool —
	// see buildenv.go). No-op when BuildEnv is empty.
	r.exportBuildEnv()
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
		rec.commit = func(c context.Context) { _ = r.streamSession(c, sessionRel) }
		rec.commitIvl = time.Second
	}
	// Plant the stub on main and capture the squash base BEFORE the recorder starts
	// overwriting the file with the transcript.
	stubCommit, err := r.startSession(ctx, sessionFile, sessionRel)
	if err != nil {
		return res, err
	}
	recDone := make(chan struct{})
	go func() { rec.loop(recCtx); close(recDone) }()

	cl := &claim.Claimer{
		Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now,
		Session: r.Session, Publish: r.Publish, Remote: r.Remote,
	}
	deadline := r.now().Add(r.WallCap)
	prompt := first
	// LeanContext bounds each post-first turn's prompt to (changed-file diffs +
	// rolling summary) via tc; nil (the default, and every non-Work kind) leaves
	// the loop on the byte-identical bare "continue"/lean-hint path.
	var tc *turnCompactor
	if r.LeanContext && sel.Kind == selectt.Work {
		tc = newTurnCompactor()
	}
	// No-forward-progress guard. stall trips when the code worktree fingerprint is
	// unchanged for StallTurns consecutive turns (see stallDetector); progressGit
	// is the agent's own worktree (wtAbs) whose HEAD+status is that fingerprint —
	// NOT the shared submodule checkout (r-side wg), which never moves as the agent
	// commits on its branch. Both are inert when StallTurns==0 (limit<=0 never
	// trips) so the default single-model host is unaffected.
	stall := &stallDetector{limit: r.StallTurns}
	var progressGit *git.Repo
	if sel.Kind == selectt.Work && wtAbs != "" {
		progressGit = git.New(wtAbs)
	}
	// finish stops the recorder, optionally records an abort warning to the
	// transcript (so it shows in the UI), commits the final transcript to the
	// session branch, then squashes+merges the durable final to main once and
	// publishes the agent's branch.
	finish := func(warning string) error {
		recStop()
		<-recDone
		if err := rec.snapshot(ctx); err != nil { // final flush after the last turn settles
			return sessionTranscriptError{err: fmt.Errorf("write final session transcript: %w", err)}
		}
		if warning != "" {
			if err := rec.appendWarning(warning); err != nil {
				return sessionTranscriptError{err: fmt.Errorf("append session warning: %w", err)}
			}
			if r.Debug != nil {
				fmt.Fprintf(r.Debug, "\n⚠️  %s\n", warning)
			}
		}
		if err := r.streamSession(ctx, sessionRel); err != nil { // durable final transcript on the session branch (beehived's live source)
			return sessionTranscriptError{err: fmt.Errorf("stream final session transcript: %w", err)}
		}
		// Publish the WORK first and let its result gate completion. The work is what
		// the task exists to produce; a convenience artifact must never block it.
		ferr := r.publishWithResolution(ctx, sess)
		if ferr != nil && r.Debug != nil {
			fmt.Fprintf(r.Debug, "\n⚠️  publish to main failed: %v\n", ferr)
		}
		// Promote the transcript to main best-effort. It already lives on the session
		// branch (beehived reads that for the live view), so failing to reach main is a
		// WARNING, never a task failure — decoupling it is what stops a cosmetic
		// transcript merge-conflict from blocking real work. Leave SessionPublished
		// false on failure so the stream branch is kept as the transcript source. The
		// warning goes to stderr (not r.Debug) so it reaches the journal even with
		// --debug off, carrying the conflicted paths for the log-review audit.
		if terr := r.finalizeSession(ctx, sid, sessionRel, stubCommit); terr != nil {
			fmt.Fprintf(os.Stderr, "honeybee: WARNING session transcript publish to main failed (non-fatal; transcript kept on branch %s): %v\n", r.SessionBranch, terr)
		} else {
			res.SessionPublished = true
		}
		return ferr
	}
	cleanup := func() {
		if wg != nil {
			_ = wg.WorktreeRemove(ctx, wtAbs)
		}
	}
	// reclaimBranch deletes this task's merged source branch from the submodule
	// origin and drops its local ref. Call it AFTER cleanup() removes the worktree
	// (a checked-out branch cannot be deleted locally). The hard merged-guard inside
	// means an unmerged/in-flight branch is left intact, so it is safe on DONE and
	// cap alike. A reclaim warning is recorded only when no warning is already set.
	reclaimBranch := func() {
		if w := r.reclaimSourceBranch(ctx, sel.Submodule, res.Branch, absRoot); w != "" && res.Warning == "" {
			res.Warning = w
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
		removed, warning, terr := r.taskRemoved(ctx, sel)
		if terr != nil {
			// A fetch/pull failure means we can no longer trust that main hasn't
			// moved out from under us. Per the remote-sharing contract, work done
			// blind (unable to catch up) is invalid, so end the run rather than
			// keep spending tokens on a task whose status we cannot verify. (In
			// local-sharing/no-remote mode taskRemoved does no fetch and cannot
			// return this error, so healthy single-host runs are unaffected.)
			if ferr := finish(""); ferr != nil {
				cleanup()
				return res, errors.Join(fmt.Errorf("turn %d: cannot reach main to verify task status: %w", res.Turns, terr), ferr)
			}
			cleanup()
			return res, fmt.Errorf("turn %d: cannot reach main to verify task status: %w", res.Turns, terr)
		}
		if removed {
			res.Warning = warning
			if ferr := finish(warning); ferr != nil {
				cleanup()
				return res, ferr
			}
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
					if ferr := finish(""); ferr != nil {
						return res, errors.Join(derr, ferr)
					}
					return res, derr
				}
				if done {
					// Publish first; only release + report complete if main advanced.
					if ferr := finish(""); ferr != nil {
						cleanup()
						if isSessionTranscriptError(ferr) {
							return res, ferr
						}
						res.GCMarked = true
						res.Warning = fmt.Sprintf(
							"task %s reached completion locally but publishing to main failed: %v; left unreleased for retry",
							sel.Task.ID, ferr)
						return res, nil
					}
					res.Completed = true
					if w := r.pushSourceBranch(ctx, wg, res.Branch); w != "" {
						res.Warning = w
					}
					_ = cl.Release(ctx, sel.Task.ID)
					cleanup()
					reclaimBranch()
					return res, nil
				}
				res.Warning = fmt.Sprintf(
					"task %s left %s but the completion check failed — left for review",
					sel.Task.ID, sel.Task.Status)
				if ferr := finish(res.Warning); ferr != nil {
					cleanup()
					return res, ferr
				}
				cleanup()
				return res, nil
			case errors.Is(err, claim.ErrLost):
				// Another session won the task. Stop now so the honeybee process
				// reselects the next most useful task instead of wasting turns on a
				// redundant pass. (Double-guarded: the agent is also instructed to stop
				// when it sees a foreign session, ending the turn early on its own.)
				res.Lost = true
				res.Warning = fmt.Sprintf("lost the claim race for %s to another session; reselecting", sel.Task.ID)
				if ferr := finish(res.Warning); ferr != nil {
					cleanup()
					return res, ferr
				}
				cleanup()
				return res, nil
			default:
				if ferr := finish(""); ferr != nil {
					return res, errors.Join(fmt.Errorf("turn %d heartbeat: %w", res.Turns, err), ferr)
				}
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
				if ferr := finish(res.Warning); ferr != nil {
					cleanup()
					return res, ferr
				}
				cleanup()
				return res, nil
			}
			if ferr := finish(""); ferr != nil {
				return res, errors.Join(fmt.Errorf("turn %d prompt: %w", res.Turns, perr), ferr)
			}
			return res, fmt.Errorf("turn %d prompt: %w", res.Turns, perr)
		}
		done, err := r.complete(sel, res.Branch)
		if err != nil {
			if ferr := finish(""); ferr != nil {
				return res, errors.Join(err, ferr)
			}
			return res, err
		}
		if done {
			// Completion is only real once the work lands on main. Publish first; if it
			// fails, do NOT release the claim or report Completed — leave the task
			// claimed (stale -> GC -> retry) so the work is re-driven, never silently
			// dropped as a phantom DONE.
			if ferr := finish(res.Warning); ferr != nil {
				cleanup()
				if isSessionTranscriptError(ferr) {
					return res, ferr
				}
				res.GCMarked = true
				res.Warning = fmt.Sprintf(
					"task %s reached completion locally but publishing to main failed: %v; left unreleased for retry",
					sel.Task.ID, ferr)
				return res, nil
			}
			// Second publish-tree guard (bootstrap/reconcile): finish() published,
			// but a publish can succeed as a NO-OP — classically the agent left the
			// new PLAN.md uncommitted in the worktree, so the branch merge to main
			// carried nothing — meaning the green LOCAL complete() above does NOT
			// prove the plan/stamp reached main. Re-verify the completion predicate
			// against the PUBLISHED main; if main did not actually advance, treat it
			// exactly like a failed publish: mark for GC and do NOT report Completed,
			// so the work is re-driven rather than recorded as a phantom DONE. A
			// verification error is fail-closed (also blocks completion). For all
			// other kinds and in no-publish local mode mainAdvanced returns true, so
			// this is a no-op there.
			if adv, aerr := r.mainAdvanced(ctx, sel); aerr != nil || !adv {
				cleanup()
				res.GCMarked = true
				if aerr != nil {
					res.Warning = fmt.Sprintf(
						"task %s reached completion locally but verifying main advanced failed: %v; left for retry",
						taskID(sel), aerr)
				} else {
					res.Warning = fmt.Sprintf(
						"task %s reached completion locally but main did not advance after publish (no-op publish); left for retry",
						taskID(sel))
				}
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
			reclaimBranch()
			return res, nil
		}
		// No-forward-progress guard: this turn did NOT complete the task, so
		// fingerprint the agent's code worktree. An identical fingerprint for
		// StallTurns turns running means the agent is churning — talking without
		// changing a file — so abandon it for GC now rather than burn the remaining
		// turn/wall budget on a provably stuck session. Fail-open: when no signal is
		// available (a git error, or a non-Work pass with no worktree and no injected
		// probe) progressSignal returns ok=false and the guard never fires, so a
		// transient fault can never cause a false kill.
		if sig, ok := r.progressSignal(ctx, progressGit); ok && stall.observe(sig) {
			res.GCMarked = true
			res.Warning = fmt.Sprintf(
				"task %s made no forward progress across %d consecutive turns (idle churn); abandoning for GC",
				taskID(sel), r.StallTurns)
			if ferr := finish(res.Warning); ferr != nil {
				cleanup()
				return res, ferr
			}
			cleanup()
			return res, nil
		}
		if r.now().After(deadline) {
			break
		}
		if tc != nil {
			prompt = r.leanContextPrompt(ctx, sess, tc, sel, res.Branch)
		} else {
			prompt = r.nextPrompt(sel, res.Branch)
		}
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
	if ferr := finish(""); ferr != nil {
		cleanup()
		return res, ferr
	}
	cleanup()
	reclaimBranch()
	return res, nil
}

// stallDetector trips when a pass produces no forward progress — an identical
// progress fingerprint — for `limit` CONSECUTIVE turns after the first. The first
// observation of any fingerprint sets the reference (repeats=0); each subsequent
// identical one increments the streak; a changed fingerprint resets it. An
// idle-from-start agent therefore trips on the turn that completes `limit`
// unchanged observations. limit<=0 disables it (never trips), which is the inert
// default that leaves a host without stall bounding unchanged.
type stallDetector struct {
	limit   int
	last    string
	repeats int
	seen    bool
}

// observe folds one turn's fingerprint in and reports whether the pass has now
// stalled (limit consecutive unchanged fingerprints since the reference).
func (s *stallDetector) observe(sig string) bool {
	if s.limit <= 0 {
		return false
	}
	if s.seen && sig == s.last {
		s.repeats++
	} else {
		s.last = sig
		s.repeats = 0
		s.seen = true
	}
	return s.repeats >= s.limit
}

// progressSignal returns the fingerprint the stall detector observes this turn,
// and whether one is available. Progress (when set) is the authoritative source
// (tests inject a deterministic sequence); otherwise the real signal is the
// agent's code worktree HEAD + porcelain status, so any commit or uncommitted
// edit the agent makes changes it. ok=false (no worktree, or a transient git
// error) means "no signal" — the caller skips the guard this turn, so a fault
// can never manufacture a false idle-churn kill.
func (r *Runner) progressSignal(ctx context.Context, wt *git.Repo) (string, bool) {
	if r.Progress != nil {
		return r.Progress(ctx), true
	}
	if wt == nil {
		return "", false
	}
	head, err := wt.RevParse(ctx, "HEAD")
	if err != nil {
		return "", false
	}
	status, err := wt.Status(ctx)
	if err != nil {
		return "", false
	}
	return head + "\x00" + status, true
}

// nextPrompt is the "keep going" prompt sent on every turn after the first.
// Default: the bare "continue" (byte-identical to the historical loop). In lean
// mode, for a Work task whose change doc is still absent, it fires the completion
// rule as an at-decision-point hint — the turn where the agent is most likely
// wrapping up — instead of front-loading that static rule in the system preamble.
// Once the doc exists (the agent is effectively done) it reverts to "continue".
func (r *Runner) nextPrompt(sel *selectt.Selection, branch string) string {
	if !r.LeanInject || sel.Kind != selectt.Work {
		return "continue"
	}
	if present, err := r.docPresent(sel, branch); err == nil && present {
		return "continue"
	}
	return fmt.Sprintf(
		"continue. When the code change is made and tested, complete the task: write the change doc at "+
			"EXACTLY submodules/%[1]s/docs/%[2]s-%[3]s.md, commit the code on branch %[2]s with a "+
			"`Beehive: %[3]s <doc-path>` stamp and push it to the submodule's origin, bump the submodule "+
			"pointer, then flip the PLAN.md task to NEEDS-REVIEW on main.",
		sel.Submodule.Name, branch, sel.Task.ID)
}

// leanContextPrompt is the LeanContext next-turn prompt for a Work task: it wraps
// the same continue/completion instruction nextPrompt would send with a bounded
// re-orientation brief — the diffs of files that changed since last turn plus a
// rolling summary of prior turns (turnCompactor.assemble) — so the agent continues
// without re-reading unchanged files or re-scanning the whole transcript. It polls
// the live session to build that brief; if the poll fails it degrades to the plain
// nextPrompt instruction (byte-identical to the default path) rather than failing
// the turn — a digest is an optimization, never a correctness dependency. The
// authoritative transcript still streams verbatim to the session branch; this only
// bounds what the runner asks the agent to re-process each turn.
func (r *Runner) leanContextPrompt(ctx context.Context, sess Session, tc *turnCompactor, sel *selectt.Selection, branch string) string {
	instruction := r.nextPrompt(sel, branch)
	msgs, err := sess.Messages(ctx)
	if err != nil {
		// Can't read history this turn — advance no pins and send the plain
		// instruction so the turn still proceeds exactly as the default loop would.
		if r.Debug != nil {
			fmt.Fprintf(r.Debug, "\n  \u00b7 lean-context: session poll failed, sending plain continue: %v\n", err)
		}
		return instruction
	}
	return tc.assemble(instruction, transcriptText(msgs), latestFileContents(msgs))
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
	if t.Status == plan.NeedsHuman && t.HumanReason() == "" {
		return false, nil
	}
	return t.Status != from, nil
}

// refreshMain fast-forwards this honeybee's beehive worktree to origin/main via
// the git-remote-ops Pull (--ff-only) so a pre-dispatch check reads the freshest
// published PLAN.md/ROI.md. No remote (single-host install, tests) is a no-op; a
// divergent branch errors and the caller falls back to the local tree — the pull
// only ever makes the check FRESHER, never blocks it.
func (r *Runner) refreshMain(ctx context.Context) error {
	if r.Remote == "" {
		return nil
	}
	return r.Git.Pull(ctx, r.Remote, "main")
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

// mainAdvanced is the second publish-tree guard: it verifies that a bootstrap/
// reconcile task's output actually reached the PUBLISHED main, not merely this
// honeybee's working tree. complete() fires on the LOCAL artifact (an on-disk
// PLAN.md or its working-tree ROI stamp) and finish() then publishes, but a
// publish can succeed as a NO-OP — most commonly when the agent wrote the file
// to the worktree but never committed it, so the branch merge carries nothing —
// leaving the local check "done" while origin main never gained the stamp. This
// re-checks the completion predicate against the published ref (origin/main when
// distributed, else local main), so a local-only or unpushed change fails the
// check instead of reporting done. It runs AFTER finish() (the only place these
// kinds publish), which is why it is a distinct post-publish gate rather than a
// change to reconciled() — that stays the local per-turn trigger, pinned to the
// working-tree read by TestReconciledPrefixMatch and the reconcile-prefix-match
// overlap.
//
// Scope: bootstrap/reconcile only — the beehive-layer kinds whose entire output
// is a stamp/plan on main. Work/Review/Arbitrate are already gated by guard (1)
// (a rejected publish returns an error from finish and blocks completion) and
// reach completion via the task-bearing done path, so they return true here. The
// no-publish local mode (Publish == nil; tests / single-host with no convergence)
// has no separate "main" to advance beyond the local commit, so it also returns
// true. Every git failure is surfaced (fail-closed: the caller treats an error
// as "not advanced" and re-drives), never swallowed into a false "done".
func (r *Runner) mainAdvanced(ctx context.Context, sel *selectt.Selection) (bool, error) {
	if r.Publish == nil {
		return true, nil
	}
	switch sel.Kind {
	case selectt.Bootstrap, selectt.Reconcile:
	default:
		return true, nil
	}
	// Resolve the published ref exactly as taskRemoved does: origin/main when a
	// remote is configured (fetch it first so the tracking ref is current), else
	// the local main branch that a no-remote publish updates in place.
	ref := "main"
	if r.Remote != "" {
		if err := r.Git.Fetch(ctx, r.Remote, "main"); err != nil {
			return false, err
		}
		ref = r.Remote + "/main"
	}
	planRel := "submodules/" + sel.Submodule.Name + "/" + repo.PlanFile
	if sel.Kind == selectt.Bootstrap {
		// Bootstrap's local check is "PLAN.md exists on disk"; its published form
		// is "PLAN.md exists on main".
		return r.Git.Exists(ctx, ref, planRel), nil
	}
	// Reconcile: apply the exact reconciled() predicate — the PLAN.md ROI stamp
	// prefixes main's ROI head — but read the stamp from the PUBLISHED PLAN.md
	// rather than the working tree. ROI.md is not touched by a reconcile, so its
	// head is identical on the branch and on main; only the stamp must have
	// landed.
	roiPath := "submodules/" + sel.Submodule.Name + "/" + repo.ROIFile
	head, err := r.Git.LastCommit(ctx, roiPath)
	if err != nil {
		return false, err
	}
	body, err := r.Git.Show(ctx, ref, planRel)
	if err != nil {
		return false, nil // no PLAN.md published to main -> not advanced
	}
	p, err := plan.Parse(body)
	if err != nil {
		return false, err
	}
	stamp := p.ROIStamp()
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
	if t.Status == plan.NeedsHuman {
		return t.HumanReason() != "", nil
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

// reclaimSourceBranch deletes a finished task's pushed bee-<taskid> source branch
// from the submodule origin and drops its local ref — but ONLY once that branch's
// tip is already contained in the submodule's tracked main. That guard is
// load-bearing: an in-flight branch (e.g. just handed to NEEDS-REVIEW, or a
// capped/abandoned attempt) is never deleted, so a delete can never lose a commit
// nor dangle the bumped pointer for peers — once the commit lives on main it stays
// resolvable there after the branch is gone. It rides the GC/DONE path and is
// best-effort: it returns a warning string, never a hard error. A Review/Arbitrate
// pass or single-host install without the submodule checked out, a missing remote,
// or an already-deleted branch is a silent no-op; only a failed delete or a check
// that could not run is surfaced as a warning, leaving the branch intact.
func (r *Runner) reclaimSourceBranch(ctx context.Context, sub repo.Submodule, branch, absRoot string) string {
	repoDir := sub.RepoDir()
	// Without a real submodule checkout (Review/Arbitrate, or a host that never
	// initialized it) we cannot talk to its origin; leave reclamation to a host
	// that has the checkout.
	if !isSourceCheckout(ctx, repoDir) {
		return ""
	}
	sg := git.New(repoDir)
	rem, err := sg.Remote(ctx)
	if err != nil || rem == "" {
		return "" // no remote: nothing was ever pushed, nothing to reclaim
	}
	tip, err := sg.LsRemoteBranch(ctx, rem, branch)
	if err != nil {
		return fmt.Sprintf("could not query source branch %s on %s (%v); left intact", branch, rem, err)
	}
	if tip == "" {
		// The remote branch is already gone (a peer reclaimed it, or it was never
		// pushed). Drop any lingering local ref so it does not accumulate; no-op.
		_ = sg.DeleteBranch(ctx, branch)
		return ""
	}
	rel, err := filepath.Rel(absRoot, repoDir)
	if err != nil {
		return fmt.Sprintf("could not resolve %s submodule path (%v); source branch %s left intact", sub.Name, err, branch)
	}
	tracked := r.trackedBranch(ctx, rel)
	// Fetch the tracked main (so the ancestry check sees the latest, incl. a just-
	// merged approval) and the source branch (so its tip object is present locally).
	if err := sg.Fetch(ctx, rem, tracked); err != nil {
		return fmt.Sprintf("could not fetch %s/%s to verify source branch %s is merged (%v); left intact", rem, tracked, branch, err)
	}
	if err := sg.Fetch(ctx, rem, branch); err != nil {
		return fmt.Sprintf("could not fetch source branch %s from %s (%v); left intact", branch, rem, err)
	}
	merged, err := sg.IsAncestor(ctx, tip, rem+"/"+tracked)
	if err != nil {
		return fmt.Sprintf("could not determine whether source branch %s is merged into %s/%s (%v); left intact", branch, rem, tracked, err)
	}
	if !merged {
		// In-flight: deleting it would lose the commit and dangle the pointer. Keep.
		return ""
	}
	if err := sg.DeleteRemoteBranch(ctx, rem, branch); err != nil {
		return fmt.Sprintf("merged source branch %s was NOT deleted on %s (%v); it will accumulate until reclaimed", branch, rem, err)
	}
	// The remote branch is gone and the commit survives on main; drop the local ref.
	_ = sg.DeleteBranch(ctx, branch)
	return ""
}
