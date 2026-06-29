// Command honeybee runs one autonomous agent on one task: deterministic
// selection, commit-race claim, then an opencode session turn loop with
// per-turn completion checks, heartbeats, and turn/wall-clock caps.
package main

import (
	"context"
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
	defer func() {
		_ = primary.WorktreeRemove(context.Background(), wtPath)
		_, _ = primary.Run(context.Background(), "branch", "-D", wtBranch)
	}()

	// Re-root every read and write at the isolated worktree.
	rp, err := repo.Open(wtPath)
	if err != nil {
		return err
	}
	gitRepo := git.New(wtPath)
	publish := func(ctx context.Context) error { return gitRepo.PublishToMain(ctx, remote) }

	sel, err := (&selectt.Selector{
		Repo: rp, Git: gitRepo, Rand: rand.New(rand.NewSource(time.Now().UnixNano())), TTL: ttl,
	}).Select(ctx)
	if err != nil {
		return err
	}
	if sel == nil {
		fmt.Println("honeybee: no workable task")
		return nil
	}

	first := firstPrompt(sel)
	if sel.Kind == selectt.Work {
		cl := &claim.Claimer{Repo: rp, Sub: sel.Submodule, Git: gitRepo, TTL: ttl}
		if err := cl.Claim(ctx, sel.Task.ID, time.Now().UTC()); err != nil {
			if err == claim.ErrLost {
				fmt.Println("honeybee: lost claim race, exiting")
				return nil
			}
			return err
		}
		if err := publish(ctx); err != nil { // make the claim visible to peers at once
			return fmt.Errorf("publish claim: %w", err)
		}
	}

	runner := &swarm.Runner{
		Repo: rp, Git: gitRepo, MaxTurns: c.MaxTurns, WallCap: ttl, TTL: ttl, Publish: publish,
		Remote: remote, BaseMain: baseMain,
	}
	oc := &swarm.Opencode{Base: c.AgentURL, Model: c.Model, HTTP: &http.Client{Timeout: 0}}
	if debug {
		runner.Debug = os.Stderr
		oc.Debug = os.Stderr
		fmt.Fprintf(os.Stderr, "[honeybee] agent_url=%s model=%q kind=%s worktree=%s remote=%q\n",
			c.AgentURL, c.Model, sel.Kind, wtPath, remote)
	}
	runner.Client = oc
	res, err := runner.Run(ctx, sel, prompts.Agents, first)
	if err != nil {
		return err
	}
	if res.Warning != "" {
		fmt.Fprintf(os.Stderr, "honeybee: WARNING %s\n", res.Warning)
	}
	fmt.Printf("honeybee: kind=%s branch=%s session=%s turns=%d done=%v gc=%v\n",
		sel.Kind, res.Branch, res.SessionID, res.Turns, res.Completed, res.GCMarked)
	return nil
}

func firstPrompt(sel *selectt.Selection) string {
	switch sel.Kind {
	case selectt.Bootstrap:
		return prompts.Bootstrap
	case selectt.Reconcile:
		return prompts.Reconcile + "\n\n# diff range: " + sel.DiffRange
	default:
		return prompts.Select
	}
}
