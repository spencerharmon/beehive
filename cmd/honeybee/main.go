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
	"time"

	"github.com/spencerharmon/beehive/internal/claim"
	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spencerharmon/beehive/internal/swarm"
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
	base := "main"
	if remote != "" {
		if err := primary.Fetch(ctx, remote, "main"); err != nil {
			return fmt.Errorf("fetch %s main: %w", remote, err)
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
	sel0, err := (&selectt.Selector{Repo: rp0, Git: primary, Rand: rnd, TTL: ttl}).Select(ctx)
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
		Repo: rp, Git: gitRepo, MaxTurns: eff.MaxTurns, WallCap: ttl, TTL: ttl, Publish: publish,
		Remote: remote, BaseMain: baseMain, Session: session,
		SessionGit: sessGit, SessionRoot: sessPath, SessionBranch: sessBranch,
		SessionPublish: sessPublish, SessionPush: sessPush,
		RestoreConfig: restoreRemotes,
		TurnTimeout:   time.Duration(c.TurnTimeoutMinutes) * time.Minute,
		// Opt-in per-pass injection trim. Deliberately an env flag rather than a
		// config knob: the layered-config surface is owned by honeybee-model-routing,
		// and gating here keeps the injected set byte-identical to the historical
		// path until a site sets BEEHIVE_LEAN_INJECT=1.
		LeanInject: os.Getenv("BEEHIVE_LEAN_INJECT") == "1",
	}
	oc := &swarm.Opencode{Base: eff.AgentURL, Model: eff.Model, Temperature: eff.Temperature, MaxTokens: eff.MaxTokens, HTTP: &http.Client{Timeout: 0}}
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
		sessionBranchDisposable = res.SessionPublished
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
