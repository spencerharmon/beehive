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
	rp, err := repo.Open(root)
	if err != nil {
		return err
	}
	ctx := context.Background()
	gitRepo := git.New(root)
	ttl := time.Duration(c.TTLMinutes) * time.Minute

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
	}

	runner := &swarm.Runner{
		Repo: rp, Git: gitRepo, MaxTurns: c.MaxTurns, WallCap: ttl, TTL: ttl,
	}
	oc := &swarm.Opencode{Base: c.AgentURL, Model: c.Model, HTTP: &http.Client{Timeout: 0}}
	if debug {
		runner.Debug = os.Stderr
		oc.Debug = os.Stderr
		fmt.Fprintf(os.Stderr, "[honeybee] agent_url=%s model=%q kind=%s\n", c.AgentURL, c.Model, sel.Kind)
	}
	runner.Client = oc
	res, err := runner.Run(ctx, sel, prompts.Agents, first)
	if err != nil {
		return err
	}
	fmt.Printf("honeybee: kind=%s branch=%s turns=%d done=%v gc=%v\n",
		sel.Kind, res.Branch, res.Turns, res.Completed, res.GCMarked)
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
