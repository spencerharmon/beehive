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
	wtBranch := swarm.SessionID("bee", time.Now())
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
	defer func() {
		_ = primary.WorktreeRemove(context.Background(), wtPath)
		_, _ = primary.Run(context.Background(), "branch", "-D", wtBranch)
		_ = primary.WorktreeRemove(context.Background(), sessPath)
		_, _ = primary.Run(context.Background(), "branch", "-D", sessBranch)
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

	selector := &selectt.Selector{
		Repo: rp, Git: gitRepo, Rand: rand.New(rand.NewSource(time.Now().UnixNano())), TTL: ttl,
	}
	// This process's unique claim token: the worktree branch is already unique per
	// honeybee. Stamped on whatever task we work so peers see it as actively held.
	session := wtBranch

	runner := &swarm.Runner{
		Repo: rp, Git: gitRepo, MaxTurns: c.MaxTurns, WallCap: ttl, TTL: ttl, Publish: publish,
		Remote: remote, BaseMain: baseMain, Session: session,
		SessionGit: sessGit, SessionRoot: sessPath, SessionPublish: sessPublish,
	}
	oc := &swarm.Opencode{Base: c.AgentURL, Model: c.Model, HTTP: &http.Client{Timeout: 0}}
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
		sel, err := selector.Select(ctx)
		if err != nil {
			return err
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
		}

		if debug {
			fmt.Fprintf(os.Stderr, "[honeybee] agent_url=%s model=%q kind=%s worktree=%s remote=%q\n",
				c.AgentURL, c.Model, sel.Kind, wtPath, remote)
		}
		res, err := runner.Run(ctx, sel, prompts.Agents, firstPrompt(sel))
		if err != nil {
			return err
		}
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
