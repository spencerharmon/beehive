// Command beehived is the frontend daemon, the only long-running process.
// It serves file-derived read views and git-backed writes over the beehive repo.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/web"
)

func main() {
	root := flag.String("repo", ".", "beehive repo root")
	addr := flag.String("addr", ":8955", "listen address")
	pullEvery := flag.Duration("pull-interval", web.DefaultPullInterval, "how often to git pull --ff-only the beehive repo to follow off-box sessions (repos with a remote only)")
	flag.Parse()

	// Resolve the registry the daemon serves: a present host repos.yaml (a
	// validated multi-repo registry) or, on a bare single-host install, a
	// one-entry registry synthesized from the single --repo root so behavior is
	// byte-identical to before. An invalid repos.yaml fails startup here.
	reg, err := config.ResolveRegistry(*root)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	// Web routing is single-repo until multi-repo-web-routing lands; serve the
	// active (first by sorted name) registered repo. For a bare install that is
	// the synthesized single entry == today's --repo, projected to the same config.
	entry, cfg, err := serveTarget(reg)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	if len(reg.Repos) > 1 {
		log.Printf("beehived: %d repos registered; serving %q until multi-repo routing lands", len(reg.Repos), entry.Name)
	}
	r, err := repo.Open(entry.Root)
	if err != nil {
		log.Fatalf("open repo %s: %v", entry.Root, err)
	}
	s, err := web.New(r, cfg)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	// Follow off-box honeybees: periodically fast-forward the beehive repo's main
	// from the remote so the session panes re-render the transcripts other hosts
	// pushed. A no-remote (single-host) repo makes this a no-op. The process owns
	// the puller for its whole lifetime.
	s.StartPuller(context.Background(), *pullEvery)
	log.Printf("beehived listening on %s (repo %s)", *addr, entry.Root)
	log.Fatal(http.ListenAndServe(*addr, s.Routes()))
}

// serveTarget selects the active repo entry the daemon serves from a resolved
// registry — the first repo by sorted name — and projects its effective config:
// entry.Config(config.Resolve(entry.Root, "")), i.e. the per-repo keyring + agent
// overrides layered over that repo's own resolved base config. This is the
// temporary single-repo bridge; multi-repo-web-routing replaces it by handing the
// whole registry to the web server so every repo is served under its own routes.
func serveTarget(reg config.Registry) (config.RepoEntry, config.Config, error) {
	names := reg.Names()
	if len(names) == 0 {
		return config.RepoEntry{}, config.Config{}, fmt.Errorf("empty registry: no repo to serve")
	}
	entry, ok := reg.Repo(names[0])
	if !ok {
		return config.RepoEntry{}, config.Config{}, fmt.Errorf("registry: repo %q missing", names[0])
	}
	base, err := config.Resolve(entry.Root, "")
	if err != nil {
		return config.RepoEntry{}, config.Config{}, err
	}
	return entry, entry.Config(base), nil
}
