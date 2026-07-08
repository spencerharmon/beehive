// Command honeybee runs one autonomous agent on one task: deterministic
// selection, commit-race claim, then an opencode session turn loop with
// per-turn completion checks, heartbeats, and turn/wall-clock caps.
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spencerharmon/beehive/internal/claim"
	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/instruct"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spencerharmon/beehive/internal/swarm"
	"github.com/spencerharmon/beehive/internal/version"
	"github.com/spencerharmon/beehive/prompts"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "honeybee:", err)
		os.Exit(1)
	}
}

func run() error {
	root := "."
	debug := false
	for _, a := range os.Args[1:] {
		if a == "--debug" {
			debug = true
			continue
		}
		root = a
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	ttl := time.Duration(c.TTLMinutes) * time.Minute

	// Each honeybee works in its own worktree of the beehive repo on a private
	// branch, then merges to main and pushes — no shared index, no write lock,
	// conflict-free convergence. Create it off the freshest main first.
	primaryRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	primary := git.New(primaryRoot)
	remote, err := primary.Remote(ctx)
	if err != nil {
		return err
	}
	// Snapshot the repo's remote config up front, and revert any drift on exit.
	// git config is shared across all worktrees, so a `git remote add` the agent
	// runs in its worktree leaks into the live beehive repo and breaks repo-rooted
	// readers (the editor cuts edit worktrees from origin/main). Honeybees publish
	// via worktree merges to main; they must never mutate the shared repo config.
	remoteSnap, err := primary.RemoteConfig(ctx)
	if err != nil {
		return err
	}
	restoreRemotes := func(c context.Context) { _ = primary.RestoreRemotes(c, remoteSnap) }
	defer restoreRemotes(context.Background())

	// Preflight guard: never start the (token-costly) agent on a checkout that
	// cannot reach a clean, publishable state. Two sharing modes, detected from the
	// repo alone with NO configuration:
	//   - LOCAL sharing  (no remote configured): components may share this same
	//     filesystem/checkout; convergence relies on main staying a clean
	//     projection of committed history.
	//   - REMOTE sharing (a remote configured): a private checkout that converges by
	//     pull/push. A hybrid swarm mixes both; each component decides per-repo.
	// A dirty checkout is reset to HEAD (always safe) but that is WARNED, because it
	// is never normal: it signals a bug in the honeybee protocol/process, in
	// beehived, or a rogue model writing outside its worktree. If it cannot be made
	// clean, abort here — before any LLM tokens are spent — rather than do work that
	// can only fail to publish (exactly the wedge that spun the swarm for two days).
	mode := "local-sharing"
	if remote != "" {
		mode = "remote-sharing"
	}
	if healed, herr := primary.EnsureCleanCheckout(ctx); herr != nil {
		return fmt.Errorf("preflight: %s checkout at %s is dirty and cannot be reset to a clean projection of HEAD (%w); aborting before starting the agent — investigate the honeybee/beehived process or protocol", mode, primaryRoot, herr)
	} else if healed {
		fmt.Fprintf(os.Stderr, "honeybee: WARNING preflight reset a dirty %s checkout at %s to HEAD before starting; a dirty live checkout is not normal and signals a honeybee/beehived protocol or process bug (or a rogue model writing outside its worktree)\n", mode, primaryRoot)
	}

	base := "main"
	if remote != "" {
		if err := primary.Fetch(ctx, remote, "main"); err != nil {
			// Remote-sharing pull failure: work done without being able to catch up is
			// invalid, so this is fatal at startup (no LLM is started).
			return fmt.Errorf("preflight: %s cannot pull %s main (%w); aborting before starting the agent", mode, remote, err)
		}
		base = remote + "/main"
	}
	baseMain, err := primary.RevParse(ctx, base)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", base, err)
	}

	// Select once on the primary checkout to learn which submodule we'll work, so
	// the worktrees can be named <submodule>-<epoch> and are easy for an operator
	// to find. The real selection/claim happens below in the isolated worktree:
	// this seeds attempt 0; a lost-claim reselect picks a fresh task there.
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	rp0, err := repo.Open(primaryRoot)
	if err != nil {
		return err
	}

	// Prompt-embed drift guard (observability only): the prompts (HONEYBEE.md et
	// al.) and all code are compiled into these binaries, so a change merged to
	// main only reaches a live pass after an operator redeploys (LOCALS.md:
	// beehive-rebuild) — there is no auto-trigger. If this binary's build SHA is a
	// commit the beehive submodule's tracked-main tip no longer contains, the
	// deployed binaries predate merged changes and their embedded prompts/code may
	// be stale; warn in the same style as the dirty-checkout warning above. This is
	// deliberately non-fatal and never touches selection/claim/publish: a "dev"
	// build (no stamped SHA) or any unresolved git state simply stays silent.
	if subs0, serr := rp0.Submodules(); serr == nil {
		if w := promptEmbedDriftWarning(ctx, primary, baseMain, version.SHA, subs0); w != "" {
			fmt.Fprintf(os.Stderr, "honeybee: %s\n", w)
		}
	}

	// Instruction-drift preflight, Axis B (observability only): Axis A above asks
	// whether THIS BINARY is stale relative to the tracked submodule tip. Axis B
	// asks a different question — whether the ON-DISK instruction files at the hive
	// root (HONEYBEE.md, AGENTS.md, BOOTSTRAP.md, skills/*), which honeybeeProtocol
	// and friends read straight off disk every pass, still match what THIS SAME
	// binary would install fresh. Refreshing them is the separate, still-manual
	// `beehive instruction update` — nothing auto-triggers it, so a freshly
	// rebuilt (Axis-A-clean) binary can keep injecting a stale on-disk HONEYBEE.md
	// (Axis-B-dirty) indefinitely. Runs alongside, not instead of, Axis A above.
	if w := instructionDriftWarning(primaryRoot); w != "" {
		fmt.Fprintf(os.Stderr, "honeybee: %s\n", w)
	}

	sel0er := &selectt.Selector{Repo: rp0, Git: primary, Rand: rnd, TTL: ttl}
	if debug {
		sel0er.Debug = os.Stderr
	}
	sel0, err := sel0er.Select(ctx)
	if err != nil {
		return err
	}
	if sel0 == nil {
		fmt.Println("honeybee: no workable task")
		return nil
	}
	// Resolve the effective, layered config for the submodule we'll work
	// (Defaults -> host -> in-repo global -> per-submodule override). The agent
	// model knobs (URL, model, temperature, max tokens) and turn cap below come
	// from this per-submodule view, not the flat host-only Load() used for the
	// pre-selection coordination TTL.
	eff, err := config.Resolve(primaryRoot, sel0.Submodule.Name)
	if err != nil {
		return err
	}
	wtBranch := swarm.SessionID(sel0.Submodule.Name, time.Now())
	wtPath := filepath.Join(primaryRoot, ".worktrees", wtBranch)
	if err := primary.WorktreeAdd(ctx, wtPath, wtBranch, base); err != nil {
		return fmt.Errorf("create beehive worktree: %w", err)
	}
	// A SECOND beehive worktree, on its own branch, dedicated to the session
	// transcript. Keeping session commits off the agent's branch means the agent's
	// PLAN.md/docs commits and the recorder's session commits never share an index
	// (which would clobber each other); both publish to main on distinct paths.
	sessBranch := wtBranch + "-session"
	sessPath := filepath.Join(primaryRoot, ".worktrees", sessBranch)
	if err := primary.WorktreeAdd(ctx, sessPath, sessBranch, base); err != nil {
		return fmt.Errorf("create session worktree: %w", err)
	}
	sessionBranchDisposable := false
	defer func() {
		_ = primary.WorktreeRemove(context.Background(), wtPath)
		_, _ = primary.Run(context.Background(), "branch", "-D", wtBranch)
		// Drop the session branch only after Runner confirms the final transcript
		// replaced the live stub on main. On any publish/finalize failure, keep local
		// and remote stream branches so beehived can still show the transcript instead
		// of an orphaned-stub ended message.
		if sessionBranchDisposable && remote != "" {
			_, _ = primary.Run(context.Background(), "push", remote, "--delete", sessBranch)
		}
		_ = primary.WorktreeRemove(context.Background(), sessPath)
		if sessionBranchDisposable {
			_, _ = primary.Run(context.Background(), "branch", "-D", sessBranch)
		}
	}()

	// Re-root every read and write at the isolated worktree.
	rp, err := repo.Open(wtPath)
	if err != nil {
		return err
	}
	gitRepo := git.New(wtPath)
	publish := func(ctx context.Context) error { return gitRepo.PublishToMain(ctx, remote) }
	sessGit := git.New(sessPath)
	sessPublish := func(ctx context.Context) error { return sessGit.PublishToMain(ctx, remote) }
	// When the hive has a remote, push the isolated session branch to it on every
	// stream commit so a beehived on another host can read the live transcript.
	// Local-only hives (no remote) leave this nil: beehived reads the local branch.
	var sessPush func(context.Context) error
	if remote != "" {
		sessPush = func(ctx context.Context) error { return sessGit.Push(ctx, remote, sessBranch) }
	}

	selector := &selectt.Selector{Repo: rp, Git: gitRepo, Rand: rnd, TTL: ttl}
	if debug {
		selector.Debug = os.Stderr
	}
	// Rebind the primary selection onto the worktree repo so attempt 0 works the
	// exact submodule the worktrees are named after.
	seed, err := rebindSelection(rp, sel0)
	if err != nil {
		return err
	}
	// This process's unique claim token: the worktree branch is already unique per
	// honeybee. Stamped on whatever task we work so peers see it as actively held.
	session := wtBranch

	runner := &swarm.Runner{
		Repo: rp, Git: gitRepo, MaxTurns: eff.MaxTurns, MergeRetries: eff.MergeRetries, WallCap: ttl, TTL: ttl, Publish: publish,
		// RejectLimit bounds how many times a Work task's implementer commit can
		// fail to land on the submodule's origin (landSourceBranch/demoteUnpushed)
		// before it escalates to NEEDS-HUMAN instead of recycling to TODO yet
		// again — the same layered-config knob Claimer.Reject uses for a review/
		// arbitration livelock.
		RejectLimit: eff.RejectLimit,
		Remote:      remote, BaseMain: baseMain, Session: session,
		SessionGit: sessGit, SessionRoot: sessPath, SessionBranch: sessBranch,
		SessionPublish: sessPublish, SessionPush: sessPush,
		RestoreConfig:   restoreRemotes,
		TurnTimeout:     time.Duration(c.TurnTimeoutMinutes) * time.Minute,
		TurnIdleTimeout: time.Duration(eff.TurnIdleTimeoutMinutes) * time.Minute,
		// In-place recovery budget for idle-stalled turns: abort the wedged upstream
		// turn and re-drive the same session rather than abandoning the pass to a
		// transient provider hang. From the layered config (default 2).
		TurnIdleRetries: eff.TurnIdleRetries,
		// Per-kind model routing from the layered config (honeybee-model-routing):
		// a near-deterministic kind can run on a cheap model while code Work runs on
		// the strong one. eff.ModelFor falls through to the single Model when a kind
		// has no override, and returns "" when nothing is configured, so a single-
		// model host routes to the same model it already used (inert). oc.Model below
		// stays the fallback the client is built with.
		ModelFor: eff.ModelFor,
		// Effective fallback model (the single layered-config Model the Opencode
		// client is built with, below). Stamped into each session transcript header
		// so the stats page derives per-model performance from git; when ModelFor
		// routes a kind to an override, the runner stamps that instead.
		Model: eff.Model,
		// Idle-churn cap from the layered config: abandon a Work pass that makes no
		// code-worktree progress for StallTurns consecutive turns. 0 (the default)
		// leaves the pass bounded only by the turn/wall caps, exactly as before.
		StallTurns: eff.StallTurns,
		// Opt-in per-pass injection trim. Deliberately an env flag rather than a
		// config knob: the layered-config surface is owned by honeybee-model-routing,
		// and gating here keeps the injected set byte-identical to the historical
		// path until a site sets BEEHIVE_LEAN_INJECT=1.
		LeanInject: os.Getenv("BEEHIVE_LEAN_INJECT") == "1",
		// Opt-in per-turn context bounding (diffs + rolling summary instead of a bare
		// "continue" that invites re-reading every file each turn). Same env-flag
		// rationale as LeanInject; off keeps the per-turn prompt byte-identical to the
		// historical bare "continue"/lean-hint and skips the extra session poll.
		LeanContext: os.Getenv("BEEHIVE_LEAN_CONTEXT") == "1",
		// Opt-in precomputed task brief on a Work dispatch (resolved worktree/branch/
		// pointer + deterministic doc-path/commit-stamp + the task card + head
		// excerpts of the task's own files) so the agent skips discovery plumbing and
		// a whole-tree scan. Same env-flag rationale as LeanInject; off keeps the
		// injected preamble byte-identical to the historical path.
		LeanBrief: os.Getenv("BEEHIVE_LEAN_BRIEF") == "1",
		// Host build/test environment (CGO_ENABLED=0 + root-fs GOTMPDIR/GOCACHE, …)
		// resolved from the layered config. The runner exports it into the honeybee
		// process at agent spawn AND states the mandated invocation once in the
		// injected preamble (both sourced from this one map so they never drift), so
		// no honeybee re-derives the build env (audit session-audit-001 F1). Inert
		// (nil) on a normal host — LOCALS.md is the human record of what to set.
		BuildEnv: eff.BuildEnv,
	}
	oc := &swarm.Opencode{Base: eff.AgentURL, Model: eff.Model, Temperature: eff.Temperature, MaxTokens: eff.MaxTokens, IdleTimeout: time.Duration(eff.TurnIdleTimeoutMinutes) * time.Minute, HTTP: &http.Client{Timeout: 0}}
	// Always-on concise activity to stderr so every scheduled `systemd-run … honeybee`
	// pass is observable live via `journalctl --user -t honeybee` (pass kind, per-turn
	// boundaries, tool-call names, abandon/GC reasons) — not just the runner's warnings.
	runner.Concise = os.Stderr
	// --debug additionally tees the verbose full transcript (model reasoning,
	// assistant text, tool OUTPUT bodies): a superset of the concise stream.
	if debug {
		runner.Debug = os.Stderr
		oc.Debug = os.Stderr
	}
	runner.Client = oc

	// Select -> claim -> run, reselecting on a lost claim race so a conflict costs
	// at most a wasted selection, never a wasted session. `tried` stops us from
	// spinning on a task selection keeps handing back.
	const maxReselect = 8
	tried := map[string]bool{}
	for attempt := 0; attempt < maxReselect; attempt++ {
		sel := seed
		if attempt > 0 {
			s, err := selector.Select(ctx)
			if err != nil {
				return err
			}
			sel = s
		}
		if sel == nil {
			fmt.Println("honeybee: no workable task")
			return nil
		}
		key := string(sel.Kind)
		if claimable(sel.Kind) {
			key = sel.Submodule.Name + ":" + sel.Task.ID
		}
		if tried[key] {
			fmt.Println("honeybee: no fresh workable task")
			return nil
		}

		if claimable(sel.Kind) {
			cl := &claim.Claimer{
				Repo: rp, Sub: sel.Submodule, Git: gitRepo, TTL: ttl,
				Session: session, Publish: publish, Remote: remote,
			}
			if err := cl.Claim(ctx, sel.Task.ID, time.Now().UTC()); err != nil {
				if errors.Is(err, claim.ErrLost) {
					tried[key] = true
					fmt.Fprintf(os.Stderr, "honeybee: lost claim for %s, reselecting\n", sel.Task.ID)
					continue
				}
				return err
			}
		} else {
			// Bootstrap/Reconcile carry no task to claim and would otherwise be run in
			// parallel by every pass that sees the same ROI drift. A singleton lock
			// (same commit-race, on a dedicated lock file) makes exactly one pass do it;
			// the losers reselect a real task instead of duplicating the reconcile.
			cl := &claim.Claimer{
				Repo: rp, Sub: sel.Submodule, Git: gitRepo, TTL: ttl,
				Session: session, Publish: publish, Remote: remote,
			}
			if err := cl.ClaimLock(ctx, string(sel.Kind)); err != nil {
				if errors.Is(err, claim.ErrLost) {
					tried[key] = true
					fmt.Fprintf(os.Stderr, "honeybee: %s already held by another pass, reselecting\n", sel.Kind)
					continue
				}
				return err
			}
			defer cl.ReleaseLock(context.Background(), string(sel.Kind))
		}

		if debug {
			fmt.Fprintf(os.Stderr, "[honeybee] agent_url=%s model=%q kind=%s worktree=%s remote=%q\n",
				eff.AgentURL, eff.Model, sel.Kind, wtPath, remote)
		}
		res, err := runner.Run(ctx, sel, honeybeeProtocol(primaryRoot), firstPrompt(sel))
		if err != nil {
			return err
		}
		// SessionPublished => a real transcript merged to main; the stream branch is
		// safe to drop. A reconcile the dedup guard found already-applied returns
		// Completed with no SessionID: its stream branch is a bare stub (no transcript
		// ever recorded or pushed), so dispose it too rather than leaking a local ref.
		sessionBranchDisposable = res.SessionPublished || (res.Completed && res.SessionID == "")
		if res.Lost {
			tried[key] = true
			if res.Warning != "" {
				fmt.Fprintf(os.Stderr, "honeybee: %s\n", res.Warning)
			}
			continue
		}
		if res.Warning != "" {
			fmt.Fprintf(os.Stderr, "honeybee: WARNING %s\n", res.Warning)
		}
		fmt.Printf("honeybee: kind=%s branch=%s session=%s turns=%d done=%v gc=%v\n",
			sel.Kind, res.Branch, res.SessionID, res.Turns, res.Completed, res.GCMarked)
		return nil
	}
	fmt.Println("honeybee: reselect cap reached, exiting")
	return nil
}

// rebindSelection re-roots a selection made on one repo onto another (the
// isolated worktree), matching the submodule by name so all paths resolve under
// the worktree.
func rebindSelection(rp *repo.Repo, sel *selectt.Selection) (*selectt.Selection, error) {
	subs, err := rp.Submodules()
	if err != nil {
		return nil, err
	}
	for _, sm := range subs {
		if sm.Name == sel.Submodule.Name {
			s := *sel
			s.Submodule = sm
			return &s, nil
		}
	}
	return nil, fmt.Errorf("submodule %s vanished from worktree", sel.Submodule.Name)
}

// claimable reports whether a selection carries a concrete task to claim
// (Work/Review/Arbitrate); Bootstrap/Reconcile operate on PLAN.md itself.
func claimable(k selectt.Kind) bool {
	switch k {
	case selectt.Work, selectt.Review, selectt.Arbitrate:
		return true
	default:
		return false
	}
}

// promptEmbedDriftWarning reports whether THIS binary is stale relative to the
// beehive submodule's tracked-main tip and, if so, returns a one-line warning (in
// the dirty-checkout warning's style) to print to stderr; it returns "" when the
// binary is up to date, when there is nothing to compare, or when any git state
// is unresolved. It is OBSERVABILITY ONLY: it never errors out and never affects
// selection/claim/publish, so an unstamped build or a transient git hiccup is
// silent rather than noisy or fatal.
//
// The "self" submodule (the one whose source produced these binaries) is the one
// whose repo object DB contains buildSHA — buildSHA is a commit of the beehive
// product repo, so only that target's checkout holds the object; unrelated
// targets (flux, helm-charts, …) never will. "Stale" means the tracked-main tip
// is NOT contained in the build (the build predates/behind the tip): the binary
// was compiled before commits the hive now pins were merged, so its embedded
// prompts/code lag. Silence when the build already contains the tip (fresh build,
// or a dev build ahead of the tracked pointer) — that is not drift.
func promptEmbedDriftWarning(ctx context.Context, hive *git.Repo, baseRef, buildSHA string, subs []repo.Submodule) string {
	if strings.TrimSpace(buildSHA) == "" {
		return "" // honest dev build: no stamped SHA, nothing to compare
	}
	for _, sm := range subs {
		smRepo := git.New(sm.RepoDir())
		if !smRepo.CommitExists(ctx, buildSHA) {
			continue // build SHA is not a commit of this target: not our source repo
		}
		// Self submodule found — its history holds our build commit.
		treePath := "submodules/" + sm.Name + "/repo"
		tip, err := hive.GitlinkAt(ctx, baseRef, treePath)
		if err != nil || tip == "" {
			return "" // no resolvable tracked pointer: cannot compare, stay silent
		}
		contains, err := smRepo.IsAncestor(ctx, tip, buildSHA)
		if err != nil {
			return "" // ancestry undeterminable (e.g. tip not fetched): stay silent
		}
		if contains {
			return "" // build already contains the tracked tip: up to date (or ahead)
		}
		return fmt.Sprintf("WARNING preflight: this honeybee binary was built from %s but %s's tracked-main tip is %s, which the binary does not contain — the deployed beehive binaries predate merged changes, so their embedded prompts (HONEYBEE.md et al.) and code may be stale; redeploy the binaries (LOCALS.md: beehive-rebuild)", shortSHA(buildSHA), treePath, shortSHA(tip))
	}
	return "" // no target's history holds our build SHA: cannot identify self, stay silent
}

// shortSHA abbreviates a full commit SHA to 12 hex chars for log lines, leaving a
// shorter or non-hex value untouched.
func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// instructionDriftWarning reports whether ANY beehive-managed instruction file at
// the hive root (HONEYBEE.md, AGENTS.md, BOOTSTRAP.md, skills/*) has drifted from,
// or is missing versus, THIS binary's own embedded default and, if so, returns a
// one-line warning (in promptEmbedDriftWarning's style, above) to print to stderr.
// It returns "" when every managed file is Clean, or when the scan itself errors
// (e.g. an unreadable root) — this is OBSERVABILITY ONLY and must never block a
// pass, mirroring promptEmbedDriftWarning's own never-fatal contract.
//
// This is Axis B of instruction drift, orthogonal to promptEmbedDriftWarning's
// Axis A: Axis A asks whether this BINARY predates the tracked submodule tip.
// Axis B asks whether the ON-DISK file this pass actually reads (honeybeeProtocol
// reads HONEYBEE.md straight off disk, falling back to the embedded default only
// when absent) matches what this SAME binary would install fresh — refreshing it
// is the separate, still-manual `beehive instruction update`, which nothing here
// triggers. A host can be Axis-A-clean (freshly rebuilt binary) yet Axis-B-dirty
// (nobody ran `instruction update` since), which is exactly the gap that left a
// freshly rebuilt binary still injecting a pre-fix HONEYBEE.md into every pass.
// Reuses instruct.Scan for the comparison (no new drift logic); run alongside,
// never instead of, Axis A.
func instructionDriftWarning(root string) string {
	st, err := instruct.Scan(root)
	if err != nil {
		return "" // best-effort: a scan error must never warn or block a pass
	}
	var drifted []string
	for name, s := range st {
		if s != instruct.Clean {
			drifted = append(drifted, fmt.Sprintf("%s(%s)", name, s))
		}
	}
	if len(drifted) == 0 {
		return ""
	}
	sort.Strings(drifted) // deterministic order: st iterates a map
	return fmt.Sprintf("WARNING preflight: %d beehive-managed instruction file(s) at the hive root have drifted from this binary's embedded default and were not refreshed by `beehive instruction update` (%s) — this pass may be running stale guidance even though the binary itself is up to date; run `beehive instruction update`", len(drifted), strings.Join(drifted, ", "))
}

// honeybeeProtocol returns the honeybee runtime protocol (the system prompt). It
// reads HONEYBEE.md from the beehive repo root — the on-disk, operator-editable
// copy is authoritative — and falls back to the binary's embedded default only
// when that file is absent (e.g. a repo not yet migrated by `beehive instruction
// update`).
func honeybeeProtocol(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "HONEYBEE.md"))
	if err == nil && len(b) > 0 {
		return string(b)
	}
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "honeybee: reading HONEYBEE.md: %v; using built-in default\n", err)
	} else {
		fmt.Fprintln(os.Stderr, "honeybee: HONEYBEE.md absent; using built-in default (run `beehive instruction update`)")
	}
	return prompts.Honeybee
}

func firstPrompt(sel *selectt.Selection) string {
	switch sel.Kind {
	case selectt.Bootstrap:
		return prompts.Bootstrap
	case selectt.Reconcile:
		return prompts.Reconcile + "\n\n# diff range: " + sel.DiffRange
	case selectt.Review:
		return prompts.Review
	case selectt.Arbitrate:
		return prompts.Arbitrate
	default:
		return prompts.Select
	}
}
