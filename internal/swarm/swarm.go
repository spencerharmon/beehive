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
	"sync/atomic"
	"time"

	"github.com/spencerharmon/beehive/internal/claim"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/plan"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spencerharmon/beehive/internal/version"
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

// aborter is an OPTIONAL Session capability: tear down the session's in-flight
// turn server-side (opencode POST /session/{id}/abort). The turn loop uses it to
// clear a wedged turn before re-driving the same session on an idle-stall retry.
// A Session that does not implement it (test mocks) is simply not aborted — the
// re-issued Prompt supersedes the dead turn anyway — so this stays additive.
type aborter interface {
	Abort(ctx context.Context) error
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
	// RejectLimit bounds how many times a Work task's implementer commit can fail
	// to land on the submodule's origin (landSourceBranch/demoteUnpushed) before
	// it is escalated to NEEDS-HUMAN instead of recycled to TODO yet again — the
	// same "rejections before NEEDS-HUMAN" knob Claimer.Reject uses for a review/
	// arbitration livelock. 0 -> default 3.
	RejectLimit int
	WallCap     time.Duration
	TTL         time.Duration
	Now         func() time.Time
	// Concise is the ALWAYS-ON activity sink (os.Stderr on a real pass): a terse
	// per-turn log — pass kind, turn boundaries, and abandon/GC warnings — so every
	// scheduled `honeybee` pass is observable live via `journalctl -t honeybee`
	// even with no --debug flag. Its lines are disjoint from Debug's, so under
	// --debug (both = stderr) the combined stream is a clean superset. Nil in tests
	// that don't assert on it.
	Concise io.Writer
	Debug   io.Writer // --debug only: verbose full-transcript tee (superset of Concise)
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

	// TurnIdleTimeout is the per-turn PROGRESS-watchdog window used only to word the
	// abandon warning; the watchdog itself runs in the opencode client
	// (Opencode.IdleTimeout), set from the SAME config key (TurnIdleTimeoutMinutes).
	// A turn that produces no new transcript activity for this long returns
	// ErrTurnIdle, which the turn loop treats as a stalled-agent GC — distinct from
	// the absolute TurnTimeout ceiling. 0 = watchdog disabled.
	TurnIdleTimeout time.Duration

	// TurnIdleRetries bounds how many times a single pass recovers in-place from an
	// idle stall before abandoning the task for GC. On an idle stall with retries
	// left (and wall budget remaining) the runner aborts the wedged turn server-side
	// (Session Abort capability, best-effort) and re-drives the SAME session with a
	// resume prompt, preserving the investigation instead of throwing the whole pass
	// away to a transient upstream hang. 0 = the historical behavior: the first idle
	// stall abandons immediately.
	TurnIdleRetries int

	// PredicatePoll sets the mid-turn completion-predicate watchdog cadence: while
	// a turn is in flight (sess.Prompt blocked) the runner polls the SAME
	// deterministic check r.complete uses (a committed status flip to a terminal
	// state, plus the change doc for a Work task) and, the instant it is observed,
	// cancels the turn ctx so no further tool call is solicited from the agent.
	// This closes the gap session-audit-014/015 found twice: opencode settles a
	// turn only when the model goes idle, and a single turn can chain many tool
	// calls, so an agent that delivers the terminal flip and then keeps calling
	// tools ran to completion before the runner's between-turn check ever saw it —
	// corrupting the audit's completion_miss signal on a session that had actually
	// delivered. 0 -> default 500ms. It is fail-open: absent a positive complete()
	// read the watchdog makes no decision and never touches the turn ctx, so a
	// turn that has not yet reached its predicate always runs to its normal
	// settle/TurnTimeout unchanged.
	PredicatePoll time.Duration

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

	// Model is the effective fallback agent model for this pass (the layered
	// config's single Model, i.e. the model the Opencode client is built with). It
	// is the value stamped into the session transcript header for per-model stats
	// when ModelFor does not route the pass to a per-kind override. Empty only in
	// tests that construct a Runner without a model; the read side then defaults it.
	Model string

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
	// RunVerify runs one handoff verify-gate command (gofmt/go vet/go test) in the
	// code worktree and reports whether it ran-and-failed (a red) versus could-not-
	// run (an infra error). The injectable seam: nil uses realRunVerify (real exec
	// inheriting the exported BuildEnv); tests set it to force red/green and to
	// assert the static invocation. See verify.go.
	RunVerify func(ctx context.Context, dir, name string, args ...string) (verifyOutcome, error)
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
	// Collapse stub..HEAD into a single transcript commit that leaves a CLEAN tree.
	//
	// startSession already published the stub to main, so main may have advanced
	// (peer sessions/work) and the earlier SessionPublish merged that advance back
	// onto this branch — HEAD can be a merge commit whose tree carries main's other
	// files. A `reset --soft stub` rewinds PAST that merge while leaving its content
	// staged; a pathspec commit of only `rel` then strands the rest as uncommitted
	// residue, and the subsequent publish's `merge main` is REFUSED ("local changes
	// would be overwritten") — reported as a bare/none conflict. That is the
	// regression that left 96% of sessions as stubs on main. Rebuild the tree
	// explicitly instead: capture the streamed tip, hard-reset to the stub (a
	// pristine projection of main-at-session-start), then restore ONLY the transcript
	// from the tip. The result is stub-tree + transcript and nothing else, so the
	// publish cleanly re-merges the advanced main with no working-tree clash.
	tip, err := r.SessionGit.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve session branch tip: %w", err)
	}
	if _, err := r.SessionGit.Run(ctx, "reset", "--hard", stub); err != nil {
		return fmt.Errorf("reset session branch to stub: %w", err)
	}
	if _, err := r.SessionGit.Run(ctx, "checkout", tip, "--", rel); err != nil {
		return fmt.Errorf("restore final transcript onto stub: %w", err)
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

// recordPublishFailureWarning durably records msg — an already-constructed
// GC-for-retry warning — into the session transcript AFTER finish() has
// returned. It exists because finish() flushes+streams the "final" transcript
// (rec.snapshot then r.streamSession) BEFORE attempting r.publishWithResolution
// (line ~941): a failure discovered only there, or in the mainAdvanced
// re-verification after finish() has already succeeded, can never land in the
// file finish already sealed. Without this, such a pass's transcript is
// byte-for-byte indistinguishable from an honest first-try success and
// internal/audit's Aborted/CompletionMiss detection — which keys exclusively
// off a trailing "## ⚠️ warning" block (parse.go:scanBody) — never fires.
//
// Uses the EXACT primitives finish already uses internally, in the same order,
// so the resulting block is identical in shape to any other abort warning:
// rec.appendWarning(msg) (safe to call again here — it is documented as safe
// whenever the recorder goroutine has stopped, which every call site of this
// helper guarantees since finish() already did recStop+<-recDone before
// returning) followed by r.streamSession to push the amended file. Best-effort:
// a failure to durably record the warning is reported to stderr, never
// escalated — a cosmetic transcript-write problem must never turn an
// already-decided GC-for-retry outcome into something worse.
func (r *Runner) recordPublishFailureWarning(ctx context.Context, rec *recorder, sessionRel, msg string) {
	if err := rec.appendWarning(msg); err != nil {
		fmt.Fprintf(os.Stderr, "honeybee: WARNING failed to durably record publish-failure warning to session transcript: %v\n", err)
		return
	}
	if err := r.streamSession(ctx, sessionRel); err != nil {
		fmt.Fprintf(os.Stderr, "honeybee: WARNING failed to stream amended publish-failure transcript: %v\n", err)
	}
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

// logConcise writes one always-on per-turn activity line to the journal (kind,
// turn boundary, abandon/GC reason). No-op when Concise is unset (tests). Debug
// is deliberately NOT teed here: its lines are disjoint, so under --debug the
// recorder's verbose tee plus these concise lines form the superset without any
// line being emitted twice.
func (r *Runner) logConcise(format string, args ...any) {
	if r.Concise != nil {
		fmt.Fprintf(r.Concise, format, args...)
	}
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

	// Lost-work auto-recovery guard (needs-review-auto-recover-lost-work): the
	// EARLIEST-stage review/arbitration dispatch check — runs before the
	// already-merged / reachability split below is even reached, because those
	// guards assume there is SOMETHING reachable to judge (an ancestor-of-main
	// commit, or a commit present somewhere). A work pass can flip a task
	// NEEDS-REVIEW/NEEDS-ARBITRATION after authoring in its worktree but before
	// the runner publishes; if that publish never lands (crash, killed at cap,
	// failed push), bee-<taskid> ends up on no local ref, no submodule remote
	// ref, the gitlink never advanced onto it, and no change doc exists —
	// nothing for a review/arbitration pass to find, so today it idle-times-out
	// or strands until an operator hand-resets it. Only when ALL of those are
	// confirmed absent does this reset the task to TODO (claim cleared, attempts
	// incremented) so the work is simply reimplemented from intent. Conservative:
	// a merely-slow fetch, an unpushed-but-local branch, or any single positive
	// signal (doc present, gitlink already advanced, branch present anywhere)
	// leaves the task alone. Fails OPEN on any uncertainty, exactly like the
	// guards below.
	if sel.Kind == selectt.Review || sel.Kind == selectt.Arbitrate {
		// Durable-record already-merged guard (lost-work-durable-fix): runs BEFORE
		// recoverIfLost so an interrupted review whose merge landed on tracked main
		// but whose bee-<taskid> branch was since reclaimed/reused is finalized to
		// DONE from the task's OWN recorded reviewed commit — instead of recoverIfLost
		// misreading the vanished branch as lost work and looping the task. Fails
		// OPEN (falls through) on any uncertainty. See finalizeIfMergedByRecord.
		finalized, ferr := r.finalizeIfMergedByRecord(ctx, sel, absRoot)
		if ferr != nil && r.Debug != nil {
			fmt.Fprintf(r.Debug, "[honeybee] durable-record already-merged pre-check for %s failed; continuing: %v\n", sel.Task.ID, ferr)
		}
		if finalized {
			res.Completed = true
			if r.Debug != nil {
				fmt.Fprintf(r.Debug, "[honeybee] %s recorded reviewed commit already merged into tracked main; runner-finalized DONE (branch gone/reused) without spawning a %s pass\n", sel.Task.ID, sel.Kind)
			}
			return res, nil
		}
		recovered, rerr := r.recoverIfLost(ctx, sel, res.Branch, absRoot)
		if rerr != nil && r.Debug != nil {
			fmt.Fprintf(r.Debug, "[honeybee] lost-work recovery pre-check for %s failed; dispatching normally: %v\n", sel.Task.ID, rerr)
		}
		if recovered {
			res.Completed = true
			if r.Debug != nil {
				fmt.Fprintf(r.Debug, "[honeybee] %s implementer commit unrecoverable (no branch, no remote, no doc); reset to TODO instead of dispatching a doomed %s pass\n", sel.Task.ID, sel.Kind)
			}
			return res, nil
		}
	}

	// Review-dispatch-side already-merged guard (session-audit-005 F-1; the
	// symmetric counterpart to the reachability guard below): before ever
	// opening a session for a NEEDS-REVIEW task, check whether bee-<taskid>'s
	// OWN tip is ALREADY an ancestor of the submodule's tracked main (NEVER
	// the ambient beehive-layer gitlink pointer, which names whichever task's
	// Work pass most recently bumped the single shared gitlink path and can
	// trivially already be an ancestor for reasons unrelated to THIS branch —
	// review-finalize-branch-ancestor-gap, ui-audit-008). That shape can only
	// follow a PRIOR review that merged bee-<taskid> into tracked main and
	// pushed (durable) but was interrupted before it could commit the
	// hive-layer half (gitlink bump + PLAN.md DONE) — spawning a whole second
	// review would only re-discover the already-landed merge. Finalize
	// deterministically instead, with zero agent turns spent. Fails OPEN
	// (falls through to dispatch normally) on any uncertainty, exactly like
	// bounceIfUnreachable. Gated (session-audit-007 Finding #1) on
	// bee-<taskid> actually existing somewhere first: a ZERO-code-diff work
	// pass (every session-audit-NNN task) never creates or pushes that branch
	// at all, yet leaves the recorded pointer trivially an ancestor of tracked
	// main since it was never moved — see finalizeIfAlreadyMerged/
	// sourceBranchExists.
	if sel.Kind == selectt.Review {
		finalized, ferr := r.finalizeIfAlreadyMerged(ctx, sel, res.Branch, absRoot)
		if ferr != nil && r.Debug != nil {
			fmt.Fprintf(r.Debug, "[honeybee] already-merged pre-check for %s failed; dispatching review normally: %v\n", sel.Task.ID, ferr)
		}
		if finalized {
			res.Completed = true
			if r.Debug != nil {
				fmt.Fprintf(r.Debug, "[honeybee] %s implementer commit already merged into tracked main; runner-finalized DONE without spawning a review\n", sel.Task.ID)
			}
			return res, nil
		}
	}

	// Review-dispatch-side reachability guard (session-audit-003 F-LIVE): before
	// ever opening a session for a NEEDS-REVIEW task, verify its implementer
	// branch/commit is reachable somewhere this host can see. A task set
	// NEEDS-REVIEW whose commit is reachable nowhere can only spawn a review pass
	// that spelunks git internals and idle-times-out — forever, since the runner
	// just re-dispatches it next cycle. Bounce it straight to NEEDS-ARBITRATION
	// with a concrete reason instead, deterministically, with zero agent turns
	// spent. Fails OPEN (dispatches the review normally) on any uncertainty — a
	// submodule this host cannot check out, or a remote fetch error — so a
	// transient fault or a host that simply cannot verify never wrongly bounces a
	// good task; only a POSITIVE, git-confirmed absence bounces.
	if sel.Kind == selectt.Review {
		bounced, berr := r.bounceIfUnreachable(ctx, sel, res.Branch, absRoot)
		if berr != nil && r.Debug != nil {
			fmt.Fprintf(r.Debug, "[honeybee] reachability pre-check for %s failed; dispatching review normally: %v\n", sel.Task.ID, berr)
		}
		if bounced {
			res.Completed = true
			if r.Debug != nil {
				fmt.Fprintf(r.Debug, "[honeybee] %s implementer commit unreachable; bounced to NEEDS-ARBITRATION without spawning a review\n", sel.Task.ID)
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
				"Beehive layer: write submodules/%[1]s/PLAN.md (status only) and submodules/%[1]s/docs/. Your task card "+
				"(with its `Review:` note) is provided below — do NOT open PLAN.md or ROI.md to read it.\n"+
				"Implementer's work is on branch bee-%[2]s in submodules/%[1]s/repo — inspect read-only via git "+
				"(fetch from origin if the branch is absent locally). Change doc: submodules/%[1]s/docs/bee-%[2]s-%[2]s.md.\n"+
				"APPROVE -> merge the submodule pointer bump + PLAN.md task DONE + unlock dependents. "+
				"REJECT -> PLAN.md task NEEDS-ARBITRATION + rejection doc submodules/%[1]s/docs/%[2]s-review-reject.md.\n"+
				"The run completes when the task leaves NEEDS-REVIEW. Act autonomously.\n\n",
			smName, sel.Task.ID)
	case selectt.Arbitrate:
		preamble = fmt.Sprintf(
			"# Context (ARBITRATION — settle the dispute, do NOT reimplement)\n"+
				"cwd is the beehive repo root. Submodule: %[1]s. Task in arbitration: %[2]s.\n"+
				"Beehive layer: write submodules/%[1]s/PLAN.md (status only) and submodules/%[1]s/docs/. Your task card is "+
				"provided below — do NOT open PLAN.md or ROI.md to read it.\n"+
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
				"Beehive layer: write submodules/%[1]s/PLAN.md ONLY to flip this task's status, and "+
				"submodules/%[1]s/docs/ for the change doc. Your task is provided below — do NOT open PLAN.md or "+
				"ROI.md for task context.\n"+
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
	// Task description handoff (Work/Review/Arbitrate): the runner ALWAYS hands a
	// task-bearing pass its full task card, so the agent never opens PLAN.md or ROI.md
	// to discover or understand its task. It still WRITES PLAN.md, but only to record
	// the status transition and unlock dependents. Bootstrap/Reconcile carry no task
	// and author the plan from ROI themselves, so they get no card.
	if hasTask(sel) {
		preamble += fmt.Sprintf(
			"## Your task (provided by the runner — your COMPLETE task description; do NOT open PLAN.md or ROI.md "+
				"to find or understand it)\n%[1]sThe card above is your task. Read it, not the plan. Write "+
				"submodules/%[2]s/PLAN.md ONLY to record this task's status transition (and to unlock dependents on "+
				"DONE); never read PLAN.md or ROI.md for task context.\n\n",
			sel.Task.Card(), smName)
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
	// the setup already resolved, the deterministic doc-path/commit-stamp, and head
	// excerpts of its own files — so it skips discovery plumbing and a whole-tree scan.
	// The task card itself is handed unconditionally above (task-description handoff);
	// this brief is the Work-only precompute layer, inert by default (LeanBrief off).
	if r.LeanBrief && sel.Kind == selectt.Work {
		preamble += r.buildTaskBrief(ctx, sel, wg, wtAbs, absRoot, res.Branch).render()
	}
	first = preamble + first

	// Pass kind + submodule: always-on concise activity so the journal names what
	// this scheduled pass is, even without --debug. One line (not teed to Debug) so
	// --debug never doubles it.
	r.logConcise("[honeybee] dir=%s submodule=%s kind=%s opening session...\n", absRoot, sel.Submodule.Name, sel.Kind)
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
	//
	// passModel is the model this pass actually runs on — the routed override when
	// one applies, else the client's preconfigured fallback (r.Model). It is stamped
	// into the transcript header below so the stats page can derive per-model
	// performance from git without any stored state.
	passModel := r.Model
	if r.ModelFor != nil {
		if ms, ok := r.Client.(modelSelector); ok {
			if m := r.ModelFor(string(sel.Kind)); m != "" {
				ms.SetModel(m)
				passModel = m
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
	// Transcript header doubles as the per-session metadata block the stats page
	// reads back: the `model:` field is the sole source of truth for git-derived
	// per-model performance. Always emit it when known (the host always resolves a
	// model); omit only in tests that leave Runner.Model empty, where the read side
	// defaults it.
	modelTag := ""
	if passModel != "" {
		modelTag = " · model: " + passModel
	}
	// Stamp the runner binary's build SHA (internal/version) into the header so a
	// future audit pass can determine which commit produced a session from the
	// repo alone — no out-of-repo host read. Fall back to "dev" for an unstamped
	// build exactly like version.String()'s rule; never fabricate a SHA.
	runnerTag := " · runner: dev"
	if sha, ok := version.Build(); ok {
		runnerTag = " · runner: " + sha
	}
	rec := &recorder{
		sess:    sess,
		path:    sessionFile,
		header:  fmt.Sprintf("# session %s\n\nsubmodule: %s · kind: %s · branch: %s%s%s\n", sid, sel.Submodule.Name, sel.Kind, res.Branch, modelTag, runnerTag),
		concise: r.Concise,
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
	// Cancellable context for the recorder goroutine, created only AFTER the
	// fallible startSession above so an early return there cannot leak it. finish()
	// calls recStop then waits on recDone.
	recCtx, recStop := context.WithCancel(ctx)
	recDone := make(chan struct{})
	go func() { rec.loop(recCtx); close(recDone) }()

	cl := &claim.Claimer{
		Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now,
		Session: r.Session, Publish: r.Publish, Remote: r.Remote,
	}
	deadline := r.now().Add(r.WallCap)
	prompt := first
	// gateHint carries a handoff verify-gate failure from the turn that detected it
	// to the NEXT prompt: when the gate reds a would-be NEEDS-REVIEW handoff we keep
	// the claim and feed the agent the failure to fix forward (set in the gate block
	// below, consumed in the prompt selection at the loop's end).
	var gateHint string
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
	// idleRetriesUsed bounds in-place recovery from idle stalls across this pass (see
	// Runner.TurnIdleRetries). Cumulative, not per-turn, so the total wall time a
	// pass can burn re-waiting the idle window is bounded by retries × TurnIdleTimeout.
	idleRetriesUsed := 0
	var progressGit *git.Repo
	if sel.Kind == selectt.Work && wtAbs != "" {
		progressGit = git.New(wtAbs)
	}
	// finishTranscript stops the recorder, optionally records an abort warning to
	// the transcript (so it shows in the UI), and commits the final transcript to
	// the session branch. Split out of finish so the claim-ttl-wallcap race guard
	// (abandonForRace, below) can durably record its warning WITHOUT the
	// publish-the-branch step finish() always does next — see abandonForRace's own
	// doc comment for why that step must never run there.
	finishTranscript := func(warning string) error {
		recStop()
		<-recDone
		if err := rec.snapshot(ctx); err != nil { // final flush after the last turn settles
			return sessionTranscriptError{err: fmt.Errorf("write final session transcript: %w", err)}
		}
		if warning != "" {
			if err := rec.appendWarning(warning); err != nil {
				return sessionTranscriptError{err: fmt.Errorf("append session warning: %w", err)}
			}
			// The abandon/GC reason is concise activity: always-on to the journal so a
			// killed pass explains itself live, not only in the --debug tee.
			r.logConcise("\n⚠️  %s\n", warning)
		}
		if err := r.streamSession(ctx, sessionRel); err != nil { // durable final transcript on the session branch (beehived's live source)
			return sessionTranscriptError{err: fmt.Errorf("stream final session transcript: %w", err)}
		}
		return nil
	}
	// finish stops the recorder, optionally records an abort warning to the
	// transcript (so it shows in the UI), commits the final transcript to the
	// session branch, then squashes+merges the durable final to main once and
	// publishes the agent's branch.
	finish := func(warning string) error {
		if err := finishTranscript(warning); err != nil {
			return err
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
	// abandonForRace durably records the claim-ttl-wallcap race-guard warning
	// (racePeerOwnsTask, below) to the session transcript exactly like finish()
	// does, but DELIBERATELY skips finish()'s publishWithResolution step: at this
	// call site the agent already committed this pass's own status-flip edit to
	// the SAME PLAN.md task line a peer's now-published, active claim also owns,
	// so publishing here could only ever (a) spuriously conflict — burning
	// MergeRetries agent turns on a resolution for a task we no longer own and
	// must not complete — or (b) land a redundant/confusing no-op alongside the
	// peer's real claim. A peer already legitimately holds this task, so nothing
	// on this pass's worktree branch is worth landing; only the (separate-path,
	// conflict-free) session transcript remains worth publishing for
	// observability. The whole worktree/branch is discarded by the caller
	// regardless once Run returns (cmd/honeybee's own worktree teardown), so
	// leaving the branch unpublished here loses nothing.
	abandonForRace := func(warning string) error {
		if err := finishTranscript(warning); err != nil {
			return err
		}
		if terr := r.finalizeSession(ctx, sid, sessionRel, stubCommit); terr != nil {
			fmt.Fprintf(os.Stderr, "honeybee: WARNING session transcript publish to main failed (non-fatal; transcript kept on branch %s): %v\n", r.SessionBranch, terr)
		} else {
			res.SessionPublished = true
		}
		return nil
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
		// Always-on turn boundary: a live "still working, on turn N" heartbeat in the
		// journal so a stalled pass is visibly stalled rather than silent.
		r.logConcise("[honeybee] ── turn %d/%d ──\n", res.Turns, r.MaxTurns)
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
						r.recordPublishFailureWarning(ctx, rec, sessionRel, res.Warning)
						return res, nil
					}
					res.Completed = true
					w, demoted, lerr := r.landSourceBranch(ctx, sel, wg, res.Branch)
					if lerr != nil {
						cleanup()
						return res, lerr
					}
					if w != "" {
						res.Warning = w
					}
					if demoted {
						res.Completed = false
						cleanup()
						return res, nil
					}
					if w := r.recordReviewedCommit(ctx, sel, absRoot, cl); w != "" && res.Warning == "" {
						res.Warning = w
					}
					// Delete a rejected attempt's orphan bee branch (see the mirror
					// block in the done path): an arbitration -> TODO rework must clear
					// the superseded remote branch so the next attempt's push is not
					// walled non-fast-forward.
					if sel.Kind == selectt.Arbitrate {
						if st, serr := r.taskStatus(sel); serr == nil && st == plan.StatusTODO {
							if w := r.deleteSourceBranch(ctx, sel.Submodule, res.Branch); w != "" && res.Warning == "" {
								res.Warning = w
							}
						}
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
		var turnCtx context.Context
		var cancelTurn context.CancelFunc
		if r.TurnTimeout > 0 {
			turnCtx, cancelTurn = context.WithTimeout(ctx, r.TurnTimeout)
		} else {
			// Always a real cancellable child (never the bare, uncancelable ctx): the
			// mid-turn completion-predicate watchdog below needs a ctx it can cancel
			// to hard-stop JUST this turn without also canceling the whole run.
			turnCtx, cancelTurn = context.WithCancel(ctx)
		}
		// Mid-turn completion-predicate hard stop (see Runner.PredicatePoll /
		// predicateWatch): while this turn streams, poll for the agent having
		// already driven the task to its terminal predicate and, the instant it is
		// observed, cancel turnCtx so opencode solicits no further tool call. Scoped
		// to task-bearing kinds (Work/Review/Arbitrate) via hasTask — Bootstrap/
		// Reconcile have no per-task terminal flip for this to race past.
		var predHit *atomic.Bool
		if hasTask(sel) {
			predHit = r.predicateWatch(turnCtx, cancelTurn, sel, res.Branch)
		}
		// Mid-turn heartbeat keepalive (duplicate-dispatch-selection-guard,
		// session-audit-015): the heartbeat just above (and this loop's own
		// top-of-turn heartbeat, before sess.Prompt started) only refreshes ONCE
		// per turn, so a turn that legitimately runs long — TurnTimeout defaults
		// to 3x the default TTL — crosses the TTL staleness line mid-turn and a
		// peer's Candidates reads this claim as dead. Re-stamp it on a
		// background ticker for the duration of just this turn so it never
		// lapses; stopKeepalive blocks until the goroutine has fully exited
		// before we touch PLAN.md/the claim again below.
		stopKeepalive := r.heartbeatKeepalive(turnCtx, cl, sel)
		_, perr := sess.Prompt(turnCtx, prompt)
		timedOut := r.TurnTimeout > 0 && turnCtx.Err() == context.DeadlineExceeded
		idleStalled := errors.Is(perr, ErrTurnIdle)
		// Cancel turnCtx BEFORE waiting on stopKeepalive: the keepalive goroutine
		// only exits on turnCtx.Done() (or its own tick), so canceling first is
		// what lets stopKeepalive's wait return promptly instead of blocking
		// until the next ~TTL/3 tick (or deadlocking outright on a turn that
		// finished well before any tick was ever due).
		cancelTurn()
		stopKeepalive()
		// The watchdog's cancel surfaces here as a plain ctx-cancellation error, not
		// ErrTurnIdle/DeadlineExceeded, so it falls through neither existing branch
		// above. Recognize it explicitly and treat it as an ordinary, successful
		// turn settle — never as the fatal/timeout/idle paths below — since the
		// predicate it fired on is the SAME deterministic check the normal
		// post-turn r.complete call just below re-confirms.
		hardStopped := predHit != nil && predHit.Load() && errors.Is(perr, context.Canceled)
		if hardStopped {
			perr = nil
			timedOut = false
			idleStalled = false
			r.logConcise("[honeybee] turn %d: completion predicate observed mid-turn; hard-stopping the turn\n", res.Turns)
		}
		// In-place idle recovery: a wedged upstream turn (github-copilot holding the
		// stream open with zero output, no error, and no opencode read-timeout) is
		// probabilistic. With retry budget and wall time left, abort the dead turn
		// server-side and re-drive the SAME session (its investigation is preserved)
		// instead of abandoning the whole pass — which would restart from scratch and
		// re-roll the same hang. res.Turns < MaxTurns guarantees a real turn slot
		// remains after the loop's post-increment.
		if idleStalled && idleRetriesUsed < r.TurnIdleRetries && res.Turns < r.MaxTurns && r.now().Before(deadline) {
			idleRetriesUsed++
			if ab, ok := sess.(aborter); ok {
				abCtx, abCancel := context.WithTimeout(ctx, 15*time.Second)
				_ = ab.Abort(abCtx)
				abCancel()
			}
			fmt.Fprintf(os.Stderr, "honeybee: turn %d idle-stalled after %s; aborted the wedged turn, retrying in-place (%d/%d)\n",
				res.Turns, r.TurnIdleTimeout, idleRetriesUsed, r.TurnIdleRetries)
			if tc != nil {
				prompt = r.leanContextPrompt(ctx, sess, tc, sel, res.Branch)
			} else {
				prompt = r.nextPrompt(sel, res.Branch)
			}
			continue
		}
		if perr != nil {
			if timedOut || idleStalled {
				res.GCMarked = true
				if idleStalled {
					res.Warning = fmt.Sprintf("turn %d made no progress within the %s idle timeout (stalled agent); abandoning for GC", res.Turns, r.TurnIdleTimeout)
				} else {
					res.Warning = fmt.Sprintf("turn %d exceeded the %s per-turn ceiling; abandoning for GC", res.Turns, r.TurnTimeout)
				}
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
		// Runner-owned handoff verify-gate: before ACCEPTING a Work task's flip to
		// NEEDS-REVIEW as complete, the code worktree must pass the mechanical checks
		// (gofmt/vet/test) so a reviewer never burns a whole session rejecting a
		// regression a deterministic check catches. Red => do NOT complete: keep the
		// claim and hand the agent the failure as the next prompt (fix forward, same
		// session). Pin sel.Task.Status to the flipped status so the NEXT turn's
		// heartbeat re-stamps (ownership — and the claim that keeps peers off a not-
		// yet-ready NEEDS-REVIEW — stays fresh) instead of tripping ErrResolved on a
		// terminal status we deliberately left in place. A non-Work kind, a non-
		// NEEDS-REVIEW flip, and a green gate all return "" and leave completion
		// unchanged; an infra failure to run a check is fail-closed (blocks it).
		if done {
			hint, gerr := r.verifyGate(ctx, sel, wtAbs)
			if gerr != nil {
				if ferr := finish(""); ferr != nil {
					return res, errors.Join(gerr, ferr)
				}
				return res, gerr
			}
			if hint != "" {
				done = false
				gateHint = hint
				sel.Task.Status = plan.NeedsReview
			}
		}
		// Claim-ttl-wallcap race guard (session-audit-013 F1): the heartbeat above
		// only refreshed at the TOP of THIS turn, before sess.Prompt ran — so a turn
		// (or, below, finish()'s own conflict-retry sub-turns) that runs long enough
		// since that stamp can reach this point with a stale claim a peer has
		// already, correctly, taken over. Re-verify ownership against PUBLISHED main
		// (never merged into our own branch) before trusting this local "done" and
		// publishing over — or getting silently superseded by — a live peer. Scoped
		// to task-bearing kinds; Bootstrap/Reconcile hold no per-task claim and are
		// already covered by mainAdvanced below.
		if done && hasTask(sel) && r.racePeerOwnsTask(ctx, sel) {
			res.Lost = true
			res.Warning = fmt.Sprintf(
				"task %s reached completion locally but another session already claimed it first (claim-ttl-wallcap race); reselecting",
				taskID(sel))
			if ferr := abandonForRace(res.Warning); ferr != nil {
				cleanup()
				return res, ferr
			}
			cleanup()
			return res, nil
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
				r.recordPublishFailureWarning(ctx, rec, sessionRel, res.Warning)
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
				r.recordPublishFailureWarning(ctx, rec, sessionRel, res.Warning)
				return res, nil
			}
			res.Completed = true
			w, demoted, lerr := r.landSourceBranch(ctx, sel, wg, res.Branch)
			if lerr != nil {
				cleanup()
				return res, lerr
			}
			if w != "" {
				res.Warning = w
			}
			if demoted {
				res.Completed = false
				cleanup()
				return res, nil
			}
			if w := r.recordReviewedCommit(ctx, sel, absRoot, cl); w != "" && res.Warning == "" {
				res.Warning = w
			}
			// Arbitration that sided with the reviewer sends the task back to TODO for
			// rework. The rejected attempt's pushed bee-<taskid> branch is now a
			// superseded orphan that reclaim never touches (unmerged + divergent) and
			// that, because the name is reused, would block the next attempt's push
			// non-fast-forward. Delete it here as a first-class step so the rework
			// starts from a clean remote. Best-effort: a delete warning is recorded
			// only when no warning is already set.
			if sel.Kind == selectt.Arbitrate {
				if st, serr := r.taskStatus(sel); serr == nil && st == plan.StatusTODO {
					if w := r.deleteSourceBranch(ctx, sel.Submodule, res.Branch); w != "" && res.Warning == "" {
						res.Warning = w
					}
				}
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
		if gateHint != "" {
			// The gate red'd this turn's would-be handoff: the NEXT turn's prompt IS the
			// failure so the agent fixes forward. One-shot — clear it so a later clean
			// turn falls back to the normal lean/continue prompt.
			prompt = gateHint
			gateHint = ""
		} else if tc != nil {
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

// predicateWatch starts the mid-turn completion-predicate hard-stop watchdog
// (see Runner.PredicatePoll) and returns a flag the caller reads AFTER the turn
// returns to tell "we hard-stopped it here" apart from a normal settle, a
// TurnTimeout, or an idle-stall. It polls the EXACT SAME check r.complete uses
// at the top of the next turn — so the predicate observed here is never looser
// than the one that already gates completion — on turnCtx (never the caller's
// unbounded ctx), so it exits the instant the turn ends for any reason and never
// outlives it. On a positive read it cancels turnCtx (aborting the in-flight
// opencode POST, which unblocks sess.Prompt promptly the same way TurnTimeout
// already does) and stops; it deliberately never re-checks after that, since one
// positive, deterministic read is a complete signal, not a fingerprint to
// debounce. Fails OPEN: a complete() error or a not-yet-met predicate is treated
// identically to "keep waiting" — the watchdog never cancels on uncertainty, so
// a turn that never reaches its predicate runs to its normal settle/TurnTimeout
// exactly as before this existed.
func (r *Runner) predicateWatch(turnCtx context.Context, cancel context.CancelFunc, sel *selectt.Selection, branch string) *atomic.Bool {
	interval := r.PredicatePoll
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	hit := &atomic.Bool{}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-turnCtx.Done():
				return
			case <-t.C:
			}
			done, err := r.complete(sel, branch)
			if err != nil || !done {
				continue // fail open: not yet met (or transiently unreadable) -> keep watching
			}
			hit.Store(true)
			cancel()
			return
		}
	}()
	return hit
}

// heartbeatKeepalive re-stamps the active claim on a background ticker
// (~TTL/3) for the duration of exactly one turn, closing the mid-turn
// liveness gap the per-turn heartbeat leaves open (duplicate-dispatch-
// selection-guard, session-audit-015): the loop's own heartbeat only refreshes
// ONCE, at the top of a turn, before sess.Prompt runs — so a turn that
// legitimately runs long (TurnTimeout defaults to 3x the default TTL) crosses
// the TTL staleness line mid-turn, is read as dead by a peer's Candidates, and
// gets duplicate-dispatched despite being fully alive. Reuses the exact same
// claim.Heartbeat path a normal turn boundary uses, so a keepalive tick is
// indistinguishable on main from an ordinary heartbeat commit.
//
// Scoped to task-bearing kinds (hasTask) and disabled when TTL<=0 (a
// degenerate/test config with nothing to keep alive). Guarded entirely by
// turnCtx: the ticker goroutine exits the instant the turn ends — completion,
// cancellation, or timeout alike — never outliving the turn it was started
// for. A tick failure (a lock conflict with the agent's OWN concurrent tool
// calls committing PLAN.md/docs in this exact worktree, an ErrResolved because
// the agent already flipped the status this same turn, a transient publish
// hiccup) is swallowed and simply retried next tick — never fatal, never
// surfaced as a run error, since a missed tick just leaves the NEXT tick (or
// the following turn's own heartbeat) to refresh it.
//
// Returns a stop func the caller MUST invoke once the turn ends, before
// touching PLAN.md/the claim again: it blocks until the background goroutine
// has fully exited, so no keepalive tick can still be in flight racing the
// caller's own next read/write of the same files.
func (r *Runner) heartbeatKeepalive(turnCtx context.Context, cl *claim.Claimer, sel *selectt.Selection) func() {
	if !hasTask(sel) || r.TTL <= 0 {
		return func() {}
	}
	interval := r.TTL / 3
	if interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-turnCtx.Done():
				return
			case <-t.C:
			}
			if err := cl.Heartbeat(turnCtx, sel.Task.ID, sel.Task.Status, r.now()); err != nil {
				r.logConcise("[honeybee] keepalive heartbeat tick failed (non-fatal, retrying): %v\n", err)
			}
		}
	}()
	return func() { <-done }
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
	if t.Status == plan.NeedsHuman && !t.EscalationReady() {
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
// is a stamp/plan on main. Work/Review/Arbitrate reach completion via the
// task-bearing done path instead, gated by their OWN two dedicated checks: a
// rejected publish returns an error from finish and blocks completion, and
// racePeerOwnsTask (below) re-verifies this pass still owns the claim on
// published main before finish() ever runs — the claim-ttl-wallcap-race-guard
// fix for the gap a plain publish-conflict check cannot catch (a peer's claim
// that does not textually conflict with this pass's own edit). So Work/Review/
// Arbitrate return true here unconditionally; this function owns only the
// bootstrap/reconcile stamp-on-main shape. The no-publish local mode
// (Publish == nil; tests / single-host with no convergence) has no separate
// "main" to advance beyond the local commit, so it also returns true. Every git
// failure is surfaced (fail-closed: the caller treats an error
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

// racePeerOwnsTask is the claim-ttl-wallcap race guard: it reports whether a
// DIFFERENT, currently-ACTIVE session has claimed sel's task on the PUBLISHED
// main, read directly off that ref via `git show` — never merged into this
// honeybee's own worktree branch, so the check can never be confused with (or
// conflict against) this pass's own not-yet-published status-flip commit sitting
// uncommitted-to-main in the same worktree.
//
// The race (session-audit-013 F1, confirmed three times in one window: 243
// wasted turns, the largest single-pass total that series has measured): the
// unified claim protocol's heartbeat only refreshes at the TOP of a turn loop
// iteration, immediately before that turn's sess.Prompt call (see Run). Once a
// turn's local worktree shows the task done, the runner previously trusted that
// local read unconditionally and went straight to finish()'s publish — with NO
// re-check that this pass still owns the claim. So a pass whose tail — the
// CURRENT turn's own duration (turn timeouts default to 3x the default claim
// TTL: 180 vs 60 minutes, so a single slow turn can alone outlive the claim),
// or finish()'s own publishWithResolution conflict-retry sub-turns, which run
// with NO heartbeat refresh of their own — happens to run long enough since
// that last stamp can complete every one of its own steps (commit, push,
// status flip, zero warnings) while a peer's Claim()/Heartbeat() has ALREADY,
// and CORRECTLY per the exact same TTL, taken over the identical task and
// started (or finished) its own independent implementation. Neither side ever
// saw an error — exactly the "clean self-reported delivery, then a fresh
// redispatch of the identical task" shape the audit traced through
// PushBranchReconciled silently absorbing a live peer's branch under its
// documented-but-now-violated "only one live session at a time" assumption.
//
// Calling this immediately before trusting a local "done" as real, durable
// completion turns that silent race into a DETECTED, durably-warned Lost pass
// (the caller folds a positive result into the exact res.Lost handling the
// per-turn heartbeat's own ErrLost case already uses) instead of two sessions
// both silently believing they finished the same task. This is deliberately a
// separate, narrowly-scoped read rather than a reuse of Claimer.Heartbeat:
// Heartbeat's syncMain merges main INTO this worktree's branch and compares
// status against an expected `from`, which — called this late, AFTER our own
// turn already rewrote the local status — would either false-positive
// claim.ErrResolved on our OWN not-yet-published edit or spuriously conflict
// merging a peer's line change into our branch before we've even decided
// whether to keep going; a pure read of published main sidesteps both.
//
// Fails OPEN (false) on any uncertainty — no Publish wired (unit tests with no
// concept of a shared main), an unreadable ref, a task absent from the
// published plan (taskRemoved already owns genuine removal) — so a transient
// git hiccup can never falsely abort a good completion; only a POSITIVE,
// git-confirmed foreign active claim reports lost, mirroring
// bounceIfUnreachable's own fail-open contract.
func (r *Runner) racePeerOwnsTask(ctx context.Context, sel *selectt.Selection) bool {
	if r.Publish == nil {
		return false // no shared main to race against (unit tests / no-publish mode)
	}
	ref := "main"
	if r.Remote != "" {
		if err := r.Git.Fetch(ctx, r.Remote, "main"); err != nil {
			return false
		}
		ref = r.Remote + "/main"
	}
	planRel := "submodules/" + sel.Submodule.Name + "/" + repo.PlanFile
	body, err := r.Git.Show(ctx, ref, planRel)
	if err != nil {
		return false // no published PLAN.md to compare against: nothing to detect
	}
	p, err := plan.Parse(body)
	if err != nil {
		return false
	}
	t := p.Find(taskID(sel))
	if t == nil {
		return false // removed on main; taskRemoved's own guard owns this shape
	}
	return t.Session != "" && t.Session != r.Session && t.Active(r.now(), r.TTL)
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
		return t.EscalationReady(), nil
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
//
// Reconciliation for a stale-remote collision (session-audit-003 F-LIVE):
// bee-<taskid> is a disposable per-attempt ref reused verbatim across attempts,
// so a prior attempt that pushed the branch but never landed on main (a
// capped/abandoned pass, or one whose rejection cleanup did not run) leaves the
// name occupied by a DEAD ORPHAN commit. The new attempt's plain push is then
// rejected non-fast-forward. Never force-push and never delete the orphan
// (HONEYBEE.md's no-force-push invariant, AND doing so would make the orphan's
// commit unreachable for anyone who already recorded it): PushBranchReconciled
// fetches the orphan and folds it in with an ours-merge (keeps our tree, keeps
// the orphan reachable as an ancestor) so the retried push is a genuine
// fast-forward. The caller (landSourceBranch) is responsible for demoting the
// task when even reconciliation cannot land the commit — this function only
// reports the warning.
func (r *Runner) pushSourceBranch(ctx context.Context, wg *git.Repo, branch string) string {
	if wg == nil {
		return ""
	}
	rem, err := wg.Remote(ctx)
	if err != nil || rem == "" {
		return ""
	}
	if err := wg.PushBranchReconciled(ctx, rem, branch); err != nil {
		return fmt.Sprintf("source branch %s could not be pushed to %s, even after reconciling a divergent dead orphan (%v); the submodule pointer will dangle until pushed", branch, rem, err)
	}
	return ""
}

// landSourceBranch pushes (reconciling any dead orphan; see pushSourceBranch)
// the Work task's implementer commit to the submodule's origin. On success it
// returns ("", false, nil): the caller proceeds to report the task complete
// exactly as before. On a push that still cannot be made to land, it demotes
// the task from NEEDS-REVIEW back to a workable status (TODO, or NEEDS-HUMAN
// past the reject limit) and publishes that correction itself via
// demoteUnpushed, returning (warning, true, nil) so the caller reports the pass
// as NOT completed instead of a phantom done — the runner must never leave a
// task NEEDS-REVIEW pointing at a commit no reviewer can reach (session-
// audit-003 F-LIVE). A nil wg (Review/Arbitrate/Bootstrap/Reconcile;
// pushSourceBranch's own no-op) returns ("", false, nil) same as a clean push.
// A push failure on a task that is NOT (or no longer) NEEDS-REVIEW — outside
// this guard's owned shape, e.g. an agent set a terminal status other than
// NEEDS-REVIEW, or a peer already resolved it — returns (warning, false, nil):
// the caller still SEES the warning but is never falsely told a demotion
// happened when demoteUnpushed correctly no-op'd. Only a failure to even
// DEMOTE a genuinely-NEEDS-REVIEW stranded task (expected to be exceedingly
// rare — e.g. a filesystem or git-repository fault) is a hard error: falling
// through to report the pass complete in that case would be the exact
// phantom-done this guard exists to prevent.
func (r *Runner) landSourceBranch(ctx context.Context, sel *selectt.Selection, wg *git.Repo, branch string) (warning string, demoted bool, err error) {
	w := r.pushSourceBranch(ctx, wg, branch)
	if w == "" {
		return "", false, nil
	}
	reason := fmt.Sprintf("task %s's implementer commit could not be landed on the submodule origin: %s", taskID(sel), w)
	ok, derr := r.demoteUnpushed(ctx, sel, reason)
	if derr != nil {
		return "", false, fmt.Errorf("%s; demoting the stranded task ALSO failed (task left NEEDS-REVIEW at an unreachable commit): %w", reason, derr)
	}
	if !ok {
		return reason, false, nil
	}
	return reason + "; demoted back to a workable status instead of leaving it NEEDS-REVIEW at an unreachable commit", true, nil
}

// demoteUnpushed corrects a Work task this run just drove to NEEDS-REVIEW
// (already published to main by finish()) back to a workable status when its
// implementer commit could not be durably landed on the submodule's origin. It
// commits AND publishes the correction itself via Claimer.Strand — finish()
// already ran, so there is no LATER publish in this turn to piggyback on — so
// main never keeps showing NEEDS-REVIEW at an unreachable commit past this one
// correction. Returns demoted=false (no-op, no publish, nil error) when the
// task is not (or no longer) NEEDS-REVIEW: the shape this guard owns; anything
// else (e.g. a peer already resolved it) is left alone rather than fought —
// the caller (landSourceBranch) uses this bool to tell a real demotion from a
// no-op, never conflating the two.
func (r *Runner) demoteUnpushed(ctx context.Context, sel *selectt.Selection, reason string) (demoted bool, err error) {
	if st, err := r.taskStatus(sel); err != nil || st != plan.StatusReview {
		return false, nil
	}
	cl := &claim.Claimer{
		Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now,
		Session: r.Session, Publish: r.Publish, Remote: r.Remote,
	}
	limit := r.RejectLimit
	if limit <= 0 {
		limit = 3
	}
	if _, err := cl.Strand(ctx, sel.Task.ID, reason, limit); err != nil {
		return false, err
	}
	if err := r.publish(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// finalizeIfAlreadyMerged is the Review-dispatch-side SYMMETRIC counterpart to
// bounceIfUnreachable (session-audit-005 F-1): a review's two effects are not
// atomic — it (1) merges bee-<taskid> into the submodule's tracked main and
// pushes (durable, irreversible), then (2) commits the hive-layer bookkeeping
// (gitlink bump + PLAN.md NEEDS-REVIEW -> DONE) for the runner to land.
// Interrupted between them, the code is merged-and-DONE at origin while the
// hive PLAN still reads NEEDS-REVIEW at the PRE-merge pointer, so the runner
// re-selects the task and spawns a WHOLE second review just to re-discover the
// merge (observed live: delivery-traceability's review re-fetched/re-tested/
// re-diffed an already-merged task to byte-identity across 58 turns, only to
// flip DONE). bounceIfUnreachable handles the OPPOSITE shape (recorded pointer
// reachable NOWHERE); here the pointer IS an ancestor of tracked main, so that
// guard correctly does not fire and a redundant review would be dispatched
// instead — this guard closes that gap.
//
// Resolves the task's recorded submodule pointer identically to
// bounceIfUnreachable (`git rev-parse HEAD:submodules/<sm>/repo` on this
// honeybee's own freshly branched worktree — the exact commit a Work pass
// bumped the gitlink to) purely as a sanity precondition (this submodule does
// have a tracked pointer at all); that ambient value is NEVER what ancestry is
// tested against (review-finalize-branch-ancestor-gap, ui-audit-008) — see
// below. Before trusting ancestry at all, confirms branch (bee-<taskid>) is a
// REAL, resolvable ref AND captures its OWN tip: reclaimSourceBranch's own
// sg.LsRemoteBranch (a live ls-remote query, no fetch) in remote mode, or a
// direct local-ref lookup (sg.RevParse("refs/heads/"+branch), the same idiom
// sweep.go's resolveRef uses) in local-sharing mode. This gate matters because
// a ZERO-code-diff work pass (every session-audit-NNN task, by design — a
// diagnose-only pass never bumps the gitlink) never creates or pushes
// bee-<taskid> at all, yet leaves the recorded pointer identical to (hence
// trivially an ancestor of) tracked main — sg.CommitReachable
// (bounceIfUnreachable's check) cannot rule this out either, since an unmoved
// pointer is trivially "reachable" too: the exact triviality that also defeats
// IsAncestor. Only once branch resolves somewhere does this check
// git.Repo.IsAncestor of branch's OWN resolved tip (never the ambient
// beehive-layer gitlink pointer above) against the submodule's tracked
// branch: origin/<branch> in remote mode (fetched fresh, alongside an
// explicit fetch of bee-<taskid> itself — reclaimSourceBranch's identical
// two-fetch idiom — since sourceBranchExists's ls-remote is a live query only
// and never transfers objects, so the tip it reports is not yet resolvable in
// this checkout without it), or the local <branch> ref directly in
// local-sharing mode (a shared checkout with no remote — no mode flag, derived
// from remotes exactly like bounceIfUnreachable/CommitReachable, and already
// locally resolvable with no fetch needed). Testing branch's own tip (not the
// ambient pointer) matters because the single shared gitlink path only ever
// records whichever task's Work pass most recently bumped IT — once ANY other
// task on this submodule lands after this one, the ambient pointer no longer
// names this task's commit at all, so testing it is testing an unrelated
// value that can easily already be an ancestor of tracked main for reasons
// having nothing to do with THIS task ever being merged (confirmed live:
// commit-sha-deep-links, session-transcript-rendered-toc, and
// breadcrumb-nav-trail all had a real, resolvable bee-<taskid> whose own tip
// was NOT an ancestor of tracked main, yet the ambient-pointer check had
// wrongly passed). Submodule main only ever advances via an APPROVED review
// merge, so ancestry-of-main (once bee-<taskid> is confirmed real AND it is
// specifically ITS tip being tested) can only follow a prior approval:
// auto-finalizing is safe — it completes interrupted bookkeeping, it never
// approves anything itself, and it never fires on genuinely pending work (not
// yet an ancestor) NOR on a task whose implementer branch never existed. On a
// positive match it syncs THIS pass's own private submodule checkout to the
// tracked branch's current tip (git.Repo.HardReset, mirroring
// syncWorktreeBase) and transitions the task straight to DONE with a terse
// note via Claimer.FinalizeAlreadyMerged, whose CommitPaths bumps the
// beehive-layer gitlink by reading that now-synced checkout HEAD (a gitlink
// staged any other way, e.g. `update-index --cacheinfo` without moving the
// checkout, is silently overwritten the moment anything re-`git add`s the
// path) and commits AND publishes immediately, mirroring BounceUnreachable —
// this runs before any session/turn loop exists.
//
// Returns finalized=true only once that correction has actually landed. Any
// uncertainty (no recorded gitlink to check, the submodule checkout cannot be
// initialized here, a configured remote errors reaching it, bee-<taskid>
// resolves nowhere, or the ancestry check itself errors — e.g. the recorded
// sha does not even exist locally, bounceIfUnreachable's shape, not this
// guard's) returns (false, err) or, for a plain absent branch, (false, nil):
// the caller falls through to the reachability guard / normal dispatch rather
// than risk a false finalize of a genuinely pending — or never attempted —
// review.
// recordReviewedCommit durably stamps the submodule commit a just-completed
// Work pass handed to review (its NEEDS-REVIEW gitlink tip) onto the task, so a
// later pass can recognize the work as already-merged-into-tracked-main even
// after the disposable bee-<taskid> branch is reclaimed or reused
// (finalizeIfMergedByRecord). It runs only for a Work pass whose published
// status is NEEDS-REVIEW; every other kind/state is a no-op. Best-effort: it
// returns a warning string, never a hard error, so a failed record never blocks
// completion — finalize still has its branch-based check (finalizeIfAlreadyMerged)
// as a fallback.
func (r *Runner) recordReviewedCommit(ctx context.Context, sel *selectt.Selection, absRoot string, cl *claim.Claimer) string {
	if sel.Kind != selectt.Work || !hasTask(sel) {
		return ""
	}
	st, err := r.taskStatus(sel)
	if err != nil || st != plan.StatusReview {
		return ""
	}
	rel, err := filepath.Rel(absRoot, sel.Submodule.RepoDir())
	if err != nil {
		return ""
	}
	sha, err := r.Git.RevParse(ctx, "HEAD:"+rel)
	if err != nil || sha == "" {
		return ""
	}
	if err := cl.RecordReviewCommit(ctx, sel.Task.ID, sha); err != nil {
		return fmt.Sprintf("could not record reviewed commit for %s: %v", sel.Task.ID, err)
	}
	return ""
}

// finalizeIfMergedByRecord is the durable-record counterpart to
// finalizeIfAlreadyMerged (lost-work-durable-fix). finalizeIfAlreadyMerged can
// only recognize an interrupted-review merge WHILE the bee-<taskid> branch still
// resolves somewhere — but the same successful merge that lands the commit on
// tracked main also reclaims that branch, and a later reworked attempt reuses the
// name at a different tip. Once the branch is gone/moved, finalizeIfAlreadyMerged
// falls through and recoverIfLost (which also only checks the branch) misreads the
// vanished branch as lost work, looping the task forever — escalating to
// NEEDS-HUMAN once attempts pass the limit (observed live: phantom-library
// m14-per-user-rig). This guard closes that gap using the task's OWN durably
// recorded reviewed commit (plan.Task.ReviewCommit, stamped by recordReviewedCommit
// the moment the Work pass landed NEEDS-REVIEW) INSTEAD of the branch: if that
// recorded sha is an ancestor of the submodule's tracked main, the work WAS merged
// — complete the interrupted bookkeeping to DONE, no branch and no re-review
// required.
//
// Safe for the same reason finalizeIfAlreadyMerged is: submodule main advances
// ONLY via an approved review merge, so ancestry-of-main of THIS task's own
// recorded commit can only follow a prior approval. The recorded sha is
// task-specific (never the ambient shared gitlink pointer, whose triviality
// finalizeIfAlreadyMerged's sourceBranchExists gate exists to dodge), so it cannot
// be trivially-an-ancestor for reasons unrelated to this task. Fails OPEN on any
// uncertainty — no record, submodule not checked out here, a remote error, or the
// recorded sha not even resolvable (so a merge cannot be proven) — returning
// (false, err)/(false, nil) so the caller falls through to the branch-based guards
// and normal dispatch; a genuinely pending or truly lost task is never wrongly
// finalized. Handles a NEEDS-REVIEW or NEEDS-ARBITRATION task.
func (r *Runner) finalizeIfMergedByRecord(ctx context.Context, sel *selectt.Selection, absRoot string) (bool, error) {
	sha := sel.Task.ReviewCommit
	if sha == "" {
		return false, nil
	}
	repoDir := sel.Submodule.RepoDir()
	rel, err := filepath.Rel(absRoot, repoDir)
	if err != nil {
		return false, err
	}
	if _, err := r.Git.RevParse(ctx, "HEAD:"+rel); err != nil {
		return false, fmt.Errorf("resolve submodule %s pointer: %w", sel.Submodule.Name, err)
	}
	if !isSourceCheckout(ctx, repoDir) {
		if _, err := r.Git.Run(ctx, "submodule", "update", "--init", "--", rel); err != nil {
			return false, err
		}
	}
	if !isSourceCheckout(ctx, repoDir) {
		return false, fmt.Errorf("submodule %s not checked out at %s", sel.Submodule.Name, repoDir)
	}
	sg := git.New(repoDir)
	rem, err := sg.Remote(ctx)
	if err != nil {
		return false, err
	}
	tracked := r.trackedBranch(ctx, rel)
	trackedRef := tracked
	if rem != "" {
		// Fetch tracked main so the ancestry check sees the latest tip AND so the
		// recorded commit's object is present locally when it lives only on main.
		if err := sg.Fetch(ctx, rem, tracked); err != nil {
			return false, err
		}
		trackedRef = rem + "/" + tracked
	}
	merged, err := sg.IsAncestor(ctx, sha, trackedRef)
	if err != nil || !merged {
		// err: the recorded sha is not even resolvable here (a merge cannot be
		// proven) — fail open. !merged: genuinely not on tracked main yet — fall
		// through to the branch-based guards / normal dispatch.
		return false, err
	}
	// Ancestry proves the recorded commit is on tracked main, but a mis-recorded
	// sha or a merge that dropped the change's files could still leave the actual
	// source unlanded. Refuse to auto-finalize DONE unless the change doc's Files:
	// entries are present in the merged tree — never rubber-stamp a "merge" whose
	// effect is absent.
	docPath := filepath.Join(sel.Submodule.Path, "docs", fmt.Sprintf("bee-%s-%s.md", sel.Task.ID, sel.Task.ID))
	if err := r.assertDocFilesLanded(ctx, sg, trackedRef, docPath); err != nil {
		return false, err
	}
	// Merged: sync THIS pass's private submodule checkout to the tracked tip so the
	// gitlink bump (FinalizeAlreadyMerged -> CommitPaths) records the real HEAD,
	// exactly as finalizeIfAlreadyMerged does.
	if err := sg.HardReset(ctx, trackedRef); err != nil {
		return false, fmt.Errorf("sync submodule %s to %s: %w", sel.Submodule.Name, trackedRef, err)
	}
	tip, err := sg.RevParse(ctx, "HEAD")
	if err != nil {
		return false, err
	}
	note := fmt.Sprintf(
		"recorded reviewed commit %s already merged into tracked %s (%s) by a prior interrupted review; runner-finalized DONE (no re-review, branch not required)",
		sha, tracked, tip)
	cl := &claim.Claimer{
		Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now,
		Session: r.Session, Publish: r.Publish, Remote: r.Remote,
	}
	if err := cl.FinalizeAlreadyMerged(ctx, sel.Task.ID, rel, note); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Runner) finalizeIfAlreadyMerged(ctx context.Context, sel *selectt.Selection, branch, absRoot string) (bool, error) {
	repoDir := sel.Submodule.RepoDir()
	rel, err := filepath.Rel(absRoot, repoDir)
	if err != nil {
		return false, err
	}
	if _, err := r.Git.RevParse(ctx, "HEAD:"+rel); err != nil {
		return false, fmt.Errorf("resolve submodule %s pointer: %w", sel.Submodule.Name, err)
	}
	if !isSourceCheckout(ctx, repoDir) {
		if _, err := r.Git.Run(ctx, "submodule", "update", "--init", "--", rel); err != nil {
			return false, err
		}
	}
	if !isSourceCheckout(ctx, repoDir) {
		return false, fmt.Errorf("submodule %s not checked out at %s", sel.Submodule.Name, repoDir)
	}
	sg := git.New(repoDir)
	rem, err := sg.Remote(ctx)
	if err != nil {
		return false, err
	}
	branchTip, exists, err := r.sourceBranchExists(ctx, sg, rem, branch)
	if err != nil || !exists {
		return false, err
	}
	tracked := r.trackedBranch(ctx, rel)
	trackedRef := tracked
	if rem != "" {
		// Fetch the tracked main (so the ancestry check sees the latest, incl. a
		// just-merged approval) and the source branch itself (so branchTip — so
		// far only a live ls-remote-advertised sha, never transferred by
		// sourceBranchExists's existence check — actually resolves locally).
		// Mirrors reclaimSourceBranch's identical two-fetch idiom.
		if err := sg.Fetch(ctx, rem, tracked); err != nil {
			return false, err
		}
		if err := sg.Fetch(ctx, rem, branch); err != nil {
			return false, err
		}
		trackedRef = rem + "/" + tracked
	}
	merged, err := sg.IsAncestor(ctx, branchTip, trackedRef)
	if err != nil || !merged {
		return false, err
	}
	// Ancestry proves the branch tip is on tracked main, but the same completion
	// assertion applies: refuse to auto-finalize DONE unless the change doc's
	// Files: entries actually landed in the merged tree.
	docPath := filepath.Join(sel.Submodule.Path, "docs", fmt.Sprintf("bee-%s-%s.md", sel.Task.ID, sel.Task.ID))
	if err := r.assertDocFilesLanded(ctx, sg, trackedRef, docPath); err != nil {
		return false, err
	}
	// The recorded pointer is already folded into tracked main: sync THIS pass's
	// own (private, per-pass) submodule checkout to that tip — exactly
	// syncWorktreeBase's pattern — so the beehive-layer gitlink bump below
	// (Claimer.FinalizeAlreadyMerged -> CommitPaths) reads the checkout's real,
	// now-matching HEAD. Staging a gitlink any other way (e.g. directly via
	// `update-index --cacheinfo` without moving the checkout) is silently
	// overwritten the moment anything re-`git add`s the path, because git always
	// re-reads a live nested checkout's OWN HEAD when staging its gitlink —
	// there is no such thing as a gitlink stage that survives independent of the
	// checkout's actual state.
	if err := sg.HardReset(ctx, trackedRef); err != nil {
		return false, fmt.Errorf("sync submodule %s to %s: %w", sel.Submodule.Name, trackedRef, err)
	}
	tip, err := sg.RevParse(ctx, "HEAD")
	if err != nil {
		return false, err
	}
	note := fmt.Sprintf(
		"already merged into tracked %s (%s) by a prior interrupted review; runner-finalized DONE (no re-review)",
		tracked, tip)
	cl := &claim.Claimer{
		Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now,
		Session: r.Session, Publish: r.Publish, Remote: r.Remote,
	}
	if err := cl.FinalizeAlreadyMerged(ctx, sel.Task.ID, rel, note); err != nil {
		return false, err
	}
	return true, nil
}

// docReferencedFiles reads the change doc at docPath and returns the concrete,
// non-glob submodule-source paths named on its `Files:` line (reusing
// filesFromCard's conservative parse). Beehive-layer entries (PLAN.md, docs/…)
// and wildcard/glob patterns are dropped: only real tracked source paths remain,
// so a caller checks exactly the files whose landing proves the merge carried the
// actual change. Returns the read error when the doc is absent/unreadable so the
// caller can fail open.
func docReferencedFiles(docPath string) ([]string, error) {
	data, err := os.ReadFile(docPath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range filesFromCard(strings.Split(string(data), "\n")) {
		if strings.ContainsAny(f, "*?[") {
			continue // a glob names no single concrete path to assert
		}
		if f == "PLAN.md" || strings.HasPrefix(f, "docs/") {
			continue // beehive-layer, not submodule source
		}
		out = append(out, f)
	}
	return out, nil
}

// assertDocFilesLanded verifies every concrete source path named on the change
// doc's Files: line is PRESENT in the merged tracked tree (trackedRef) of the
// submodule, so an auto-finalize to DONE cannot silently pass a "merge" whose
// files never actually landed. Returns an error naming the first missing path.
// Fails OPEN — an absent/unreadable doc, or a doc with no parseable concrete
// source path (glob-only or doc-only Files:), yields nil — so a legitimately
// merged task still finalizes.
func (r *Runner) assertDocFilesLanded(ctx context.Context, sg *git.Repo, trackedRef, docPath string) error {
	files, err := docReferencedFiles(docPath)
	if err != nil {
		return nil // no readable change doc — fail open, other guards decide
	}
	for _, f := range files {
		if !sg.Exists(ctx, trackedRef, f) {
			return fmt.Errorf(
				"change doc %s lists %s but it is absent from the merged tracked tree %s: "+
					"refusing to auto-finalize DONE for a merge that never landed the change",
				docPath, f, trackedRef)
		}
	}
	return nil
}


// resolvable ref for this task's OWN attempt — the gate finalizeIfAlreadyMerged
// applies before ever trusting IsAncestor — and, when it is, ALSO returns
// branch's own resolved tip sha so the caller can test ancestry against THAT
// (review-finalize-branch-ancestor-gap): the caller's ambient beehive-layer
// gitlink pointer is a SEPARATE value (whichever task's Work pass most
// recently bumped the single shared gitlink path for this submodule) that can
// easily already be an ancestor of tracked main for reasons having nothing to
// do with THIS branch ever being merged, so discarding this resolution (as a
// bare bool) and re-testing the ambient pointer instead reopens exactly the
// triviality this gate exists to close. A genuine (if interrupted) review
// merge always has a real bee-<taskid> ref behind it (a Work pass's worktree
// branch, pushed to origin in remote mode); a ZERO-code-diff work pass (every
// session-audit-NNN task, by design) never creates or pushes one at all, yet
// leaves the recorded gitlink identical to — hence trivially an ancestor of —
// tracked main. Mirrors reclaimSourceBranch's mode split: remote mode reuses
// its exact sg.LsRemoteBranch plumbing (a live ls-remote query, no fetch —
// the returned tip is not yet resolvable in this checkout's own object
// database until the caller separately fetches branch, exactly like
// reclaimSourceBranch does); local-sharing mode (rem == "", every honeybee on
// the host sharing one checkout) looks the ref up directly via
// sg.RevParse("refs/heads/"+branch) — the same idiom sweep.go's resolveRef
// closure uses — since a real source branch there is always a plain local
// ref, already resolvable with no fetch, never something a fetch could
// produce. Returns ("", false, nil) — never an error — for a branch that
// simply does not exist either way, so the caller treats "never existed"
// exactly like "not yet merged": fall through to normal dispatch.
func (r *Runner) sourceBranchExists(ctx context.Context, sg *git.Repo, rem, branch string) (string, bool, error) {
	if rem == "" {
		tip, err := sg.RevParse(ctx, "refs/heads/"+branch)
		if err != nil {
			return "", false, nil
		}
		return tip, true, nil
	}
	tip, err := sg.LsRemoteBranch(ctx, rem, branch)
	if err != nil {
		return "", false, err
	}
	return tip, tip != "", nil
}

// bounceIfUnreachable is the Review-dispatch-side half of the F-LIVE fix: it
// verifies the task's REVIEWABLE COMMIT — the specific sha the submodule
// pointer (gitlink) is recorded at, which a Work pass bumps as part of
// completing to NEEDS-REVIEW — is reachable somewhere this host can see
// (git.Repo.CommitReachable: already in the submodule's own object database,
// or the bee-<taskid> branch fetched from a configured remote; never a doomed
// `fetch origin` against a shared checkout with no remote). Checking the
// SPECIFIC sha (not just whether bee-<taskid> resolves to SOMETHING) matters:
// the F-LIVE failure mode leaves that branch name resolving to the WRONG
// commit — a dead orphan from a prior attempt — while the actually-recorded
// implementer commit is what is missing; a plain branch-existence check would
// miss it. If unreachable, bounces the task straight to NEEDS-ARBITRATION with
// a concrete reason via Claimer.BounceUnreachable (commits AND publishes
// immediately — this runs before any session/turn loop exists). Returns
// bounced=true only once that correction has actually landed. Any uncertainty
// (no recorded gitlink to check, the submodule checkout cannot be initialized
// here, or a configured remote errors reaching it) returns (false, err): the
// caller falls back to dispatching the review normally rather than risk a
// false bounce of a good task.
func (r *Runner) bounceIfUnreachable(ctx context.Context, sel *selectt.Selection, branch, absRoot string) (bool, error) {
	repoDir := sel.Submodule.RepoDir()
	rel, err := filepath.Rel(absRoot, repoDir)
	if err != nil {
		return false, err
	}
	// The submodule pointer recorded on THIS honeybee's beehive worktree HEAD
	// (freshly branched off the just-published main before any turn has run) is
	// the reviewable commit: a Work pass bumps this gitlink, alongside the
	// PLAN.md flip to NEEDS-REVIEW, as part of the SAME merge to main.
	sha, err := r.Git.RevParse(ctx, "HEAD:"+rel)
	if err != nil {
		return false, fmt.Errorf("resolve submodule %s pointer: %w", sel.Submodule.Name, err)
	}
	if !isSourceCheckout(ctx, repoDir) {
		if _, err := r.Git.Run(ctx, "submodule", "update", "--init", "--", rel); err != nil {
			return false, err
		}
	}
	if !isSourceCheckout(ctx, repoDir) {
		return false, fmt.Errorf("submodule %s not checked out at %s", sel.Submodule.Name, repoDir)
	}
	sg := git.New(repoDir)
	rem, err := sg.Remote(ctx)
	if err != nil {
		return false, err
	}
	reachable, err := sg.CommitReachable(ctx, rem, branch, sha)
	if err != nil {
		return false, err
	}
	if reachable {
		return false, nil
	}
	where := "not present in the object database"
	if rem != "" {
		where = fmt.Sprintf("not present in the object database, and fetching %s/%s did not produce it either", rem, branch)
	}
	reason := fmt.Sprintf(
		"reviewable commit unreachable: the submodule pointer %s (recorded for this task) is %s",
		sha, where)
	cl := &claim.Claimer{
		Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now,
		Session: r.Session, Publish: r.Publish, Remote: r.Remote,
	}
	if err := cl.BounceUnreachable(ctx, sel.Task.ID, reason); err != nil {
		return false, err
	}
	return true, nil
}

// recoverIfLost is the earliest-stage review/arbitration dispatch guard
// (needs-review-auto-recover-lost-work): before the already-merged /
// reachability split even runs (both of which assume there is SOMETHING
// reachable — an ancestor of tracked main, or a commit present somewhere),
// this checks whether there is anything reachable AT ALL. A work pass can
// flip a task NEEDS-REVIEW/NEEDS-ARBITRATION after authoring in its worktree
// but before the runner publishes (finish()'s worktree-branch merge + gitlink
// bump + PLAN.md flip to main); if that publish never lands (crash, killed at
// a turn/wall-clock cap, a push that never succeeded), bee-<taskid> ends up
// unreachable in every sense: no local ref, no ref on the submodule's remote,
// the beehive-layer gitlink was never advanced onto it, and no change doc was
// ever written to submodules/<sm>/docs/bee-<taskid>-<taskid>.md. That shape is
// permanently unrecoverable by a review or arbitration pass — there is
// nothing for either to find — so today the task idle-times-out over and over
// until an operator hand-resets it (observed live: phantom-library
// m14-per-user-rig escalated NEEDS-HUMAN purely for this).
//
// Checks, ALL of which must confirm absence before recovering:
//  1. branch (bee-<taskid>) has no local ref in this submodule checkout.
//  2. branch has no ref on the submodule's configured remote either, after an
//     explicit prune-fetch (git.Repo.Fetch already passes --prune) — not a
//     cached/stale view.
//  3. the beehive-layer gitlink recorded on THIS honeybee's freshly branched
//     worktree HEAD (submodules/<sm>/repo) is not already this task's commit
//     — checked implicitly: once checks 1 and 2 confirm bee-<taskid> resolves
//     NOWHERE, there is no tip left for the gitlink to be pointing at, so it
//     trivially cannot "already carry this task's work forward" (the shape
//     finalizeIfAlreadyMerged, which runs for Review right after this guard,
//     exists to catch: a gitlink that DOES already reflect a completed merge).
//  4. no change doc exists at submodules/<sm>/docs/bee-<taskid>-<taskid>.md.
//
// This is deliberately conservative per its own accept criteria: a merely
// slow fetch, an unpushed-but-local branch, or a present change doc/gitlink
// must NOT be reset (no false-positive data loss) — a single positive signal
// on any of the four checks aborts the recovery and falls through to normal
// dispatch. Fails OPEN (returns false, err) on any uncertainty (submodule
// checkout cannot be initialized, remote errors reaching it) exactly like
// bounceIfUnreachable/finalizeIfAlreadyMerged, so a transient fault never
// wrongly resets a good task. Recovery is logged by the caller, not silent.
func (r *Runner) recoverIfLost(ctx context.Context, sel *selectt.Selection, branch, absRoot string) (bool, error) {
	repoDir := sel.Submodule.RepoDir()
	rel, err := filepath.Rel(absRoot, repoDir)
	if err != nil {
		return false, err
	}
	// The submodule pointer recorded on THIS honeybee's beehive worktree HEAD —
	// the same value bounceIfUnreachable/finalizeIfAlreadyMerged resolve — is
	// only checked as a sanity precondition below (this submodule does have a
	// tracked pointer at all, mirroring finalizeIfAlreadyMerged); it is never
	// itself part of the recoverability test (see check 3's doc comment).
	if _, err := r.Git.RevParse(ctx, "HEAD:"+rel); err != nil {
		return false, fmt.Errorf("resolve submodule %s pointer: %w", sel.Submodule.Name, err)
	}
	if !isSourceCheckout(ctx, repoDir) {
		if _, err := r.Git.Run(ctx, "submodule", "update", "--init", "--", rel); err != nil {
			return false, err
		}
	}
	if !isSourceCheckout(ctx, repoDir) {
		return false, fmt.Errorf("submodule %s not checked out at %s", sel.Submodule.Name, repoDir)
	}
	sg := git.New(repoDir)
	rem, err := sg.Remote(ctx)
	if err != nil {
		return false, err
	}
	// Check 0 (lost-work-durable-fix): never reset a task whose durably-recorded
	// reviewed commit is already an ancestor of tracked main. That is completed work
	// an interrupted review left mid-bookkeeping (finalizeIfMergedByRecord finalizes
	// it to DONE), NOT lost work — and its bee-<taskid> branch is legitimately gone,
	// which the branch checks below would otherwise misread as a loss. Backstops
	// finalizeIfMergedByRecord in case that guard failed open on a transient error.
	// Only a positive, git-confirmed merge aborts the reset; any error is ignored
	// here so the conservative branch checks below stay authoritative.
	if sha := sel.Task.ReviewCommit; sha != "" {
		tracked := r.trackedBranch(ctx, rel)
		trackedRef := tracked
		if rem != "" {
			_ = sg.Fetch(ctx, rem, tracked)
			trackedRef = rem + "/" + tracked
		}
		if merged, aerr := sg.IsAncestor(ctx, sha, trackedRef); aerr == nil && merged {
			return false, nil // recorded reviewed commit is on tracked main: not lost.
		}
	}
	// Check 1: local ref.
	if _, err := sg.RevParse(ctx, "refs/heads/"+branch); err == nil {
		return false, nil // present locally: recoverable, do not reset.
	}
	// Check 2: remote ref, after an explicit prune-fetch.
	if rem != "" {
		if ferr := sg.Fetch(ctx, rem, branch); ferr != nil && !isRemoteRefMissingFetch(ferr) {
			return false, ferr // uncertain (network/auth): fail open.
		}
		tip, lerr := sg.LsRemoteBranch(ctx, rem, branch)
		if lerr != nil {
			return false, lerr
		}
		if tip != "" {
			return false, nil // present on the remote: recoverable, do not reset.
		}
	}
	// Check 3 (see doc comment above): implied by 1+2 both confirming absence.
	// Check 4: no change doc on disk.
	docPath := filepath.Join(sel.Submodule.Path, "docs", fmt.Sprintf("bee-%s-%s.md", sel.Task.ID, sel.Task.ID))
	if _, err := os.Stat(docPath); err == nil {
		return false, nil // doc exists: recoverable, do not reset.
	} else if !os.IsNotExist(err) {
		return false, err
	}
	reason := fmt.Sprintf(
		"implementer commit for bee-%s unrecoverable: no local branch, no branch on the submodule remote%s, and no change doc at docs/bee-%s-%s.md",
		sel.Task.ID, remoteNoteFor(rem), sel.Task.ID, sel.Task.ID)
	cl := &claim.Claimer{
		Repo: r.Repo, Sub: sel.Submodule, Git: r.Git, TTL: r.TTL, Now: r.Now,
		Session: r.Session, Publish: r.Publish, Remote: r.Remote,
	}
	limit := r.RejectLimit
	if limit <= 0 {
		limit = 3
	}
	if err := cl.RecoverLostWork(ctx, sel.Task.ID, reason, limit); err != nil {
		return false, err
	}
	return true, nil
}

// remoteNoteFor renders a clause naming the remote checked, for
// recoverIfLost's reason string; empty when there is no remote (local sharing).
func remoteNoteFor(rem string) string {
	if rem == "" {
		return ""
	}
	return fmt.Sprintf(" (%s, after a prune-fetch)", rem)
}

// isRemoteRefMissingFetch reports whether a fetch of a specific branch failed
// only because the remote simply has no such ref — a definitive, confirmed
// absence distinct from a network/auth failure talking to the remote at all.
// Mirrors git.isCouldNotFindRemoteRef/isRemoteRefMissing (unexported in
// package git), re-implemented here against the same git message shapes since
// CommitReachable's identical fetch-then-check idiom lives in that package,
// not this one.
func isRemoteRefMissingFetch(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "couldn't find remote ref") || strings.Contains(msg, "not found in upstream")
}

// deleteSourceBranch unconditionally removes a task's bee-<taskid> branch from the
// submodule origin (and drops any local ref). Unlike reclaimSourceBranch it does
// NOT gate on the branch being merged — it exists to throw away a SUPERSEDED
// attempt on purpose. An arbitration that sides with the reviewer sends the task
// back to TODO for rework, orphaning the rejected attempt's pushed branch; because
// the branch name is reused per attempt and reclaim only deletes MERGED branches,
// leaving it would block the next attempt's push non-fast-forward (the exact wedge
// this repairs). A bee branch is a disposable per-attempt ref, so deleting its
// unmerged commit here is correct, not lossy. Best-effort: returns a warning
// string, never a hard error; a host without the submodule checked out (so its
// origin is unreachable — Review/Arbitrate hosts may lack it), a missing remote,
// or an already-gone branch is a silent no-op (the push failsafe is the backstop).
func (r *Runner) deleteSourceBranch(ctx context.Context, sub repo.Submodule, branch string) string {
	repoDir := sub.RepoDir()
	if !isSourceCheckout(ctx, repoDir) {
		return "" // no checkout here to reach the submodule origin; next work attempt's push failsafe clears it
	}
	sg := git.New(repoDir)
	rem, err := sg.Remote(ctx)
	if err != nil || rem == "" {
		return "" // no remote: nothing was ever pushed
	}
	if err := sg.DeleteRemoteBranch(ctx, rem, branch); err != nil {
		return fmt.Sprintf("superseded source branch %s was NOT deleted on %s (%v); it will block the next attempt's push until reclaimed", branch, rem, err)
	}
	// The remote branch is gone; drop any lingering local ref so it does not accrue.
	_ = sg.DeleteBranch(ctx, branch)
	return ""
}

// taskStatus reads the selected task's current status from the (published)
// PLAN.md. Used by the arbitration-reject cleanup to distinguish a rework
// (-> TODO, orphan branch must be deleted) from an approval (-> DONE, whose merge
// makes reclaimSourceBranch delete the branch instead). A task removed from the
// plan returns "" (no cleanup; the removed-guard owns deletions).
func (r *Runner) taskStatus(sel *selectt.Selection) (plan.Status, error) {
	b, err := os.ReadFile(sel.Submodule.PlanPath())
	if err != nil {
		return "", err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return "", err
	}
	t := p.Find(sel.Task.ID)
	if t == nil {
		return "", nil
	}
	return t.Status, nil
}

// taskStatusByID reads a task's current status from sub's (published) PLAN.md by
// id, for callers that hold a submodule + branch but no Selection (e.g.
// reclaimSourceBranch's DONE-gate). A missing/unparseable plan or an absent task
// returns "" (no status), which callers read as "no constraint".
func (r *Runner) taskStatusByID(sub repo.Submodule, id string) (plan.Status, error) {
	b, err := os.ReadFile(sub.PlanPath())
	if err != nil {
		return "", err
	}
	p, err := plan.Parse(string(b))
	if err != nil {
		return "", err
	}
	t := p.Find(id)
	if t == nil {
		return "", nil
	}
	return t.Status, nil
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
	// Merged into tracked main, but ALSO require the hive task to have reached DONE
	// before deleting (lost-work-durable-fix). A branch can be merged into submodule
	// main while its hive PLAN task is still NEEDS-REVIEW — an approved review whose
	// merge landed (durable) but whose DONE bookkeeping was interrupted. In that
	// window the branch is the evidence finalizeIfAlreadyMerged needs to complete
	// the bookkeeping; deleting it here would strand the task (recoverIfLost misreads
	// the vanished branch as lost work and loops). Keep it until DONE lands; a task
	// absent from the plan (removed) is fine to reclaim (the removed-guard owns it).
	if id := strings.TrimPrefix(branch, "bee-"); id != branch {
		if st, serr := r.taskStatusByID(sub, id); serr == nil && st != "" && st != plan.StatusDone {
			return fmt.Sprintf("source branch %s is merged into %s/%s but task %s is %s (not DONE); kept as already-merged finalize evidence until DONE lands", branch, rem, tracked, id, st)
		}
	}
	if err := sg.DeleteRemoteBranch(ctx, rem, branch); err != nil {
		return fmt.Sprintf("merged source branch %s was NOT deleted on %s (%v); it will accumulate until reclaimed", branch, rem, err)
	}
	// The remote branch is gone and the commit survives on main; drop the local ref.
	_ = sg.DeleteBranch(ctx, branch)
	return ""
}
