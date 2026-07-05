// Command beehived is the frontend daemon, the only long-running process.
// It serves file-derived read views and git-backed writes over the beehive repo.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/web"
)

func main() {
	root := flag.String("repo", ".", "beehive repo root")
	addr := flag.String("addr", ":8955", "listen address")
	flag.Parse()

	// Resolve the registry the daemon serves: a present host repos.yaml (a
	// validated multi-repo registry) or, on a bare single-host install, a
	// one-entry registry synthesized from the single --repo root so behavior is
	// byte-identical to before. An invalid repos.yaml fails startup here.
	reg, err := config.ResolveRegistry(*root)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	// Serve EVERY registered repo under per-active-repo routing: NewRegistry wires
	// one per-repo server (own repo/keyring/config) for each entry and dispatches
	// each request to the selected repo (POST /repo/{name} switch; default = the
	// first registered name). A single-entry (bare install) registry keeps today's
	// flat single-repo routes with no regression.
	s, err := web.NewRegistry(reg)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	// Startup housekeeping: recover in-flight editor sessions and prune stale edit
	// worktrees a prior beehived left behind, for every served repo. Best-effort —
	// a failure here must not stop the daemon from serving.
	if err := s.RecoverEditors(context.Background()); err != nil {
		log.Printf("editor recovery: %v", err)
	}
	if names := reg.Names(); len(names) > 1 {
		log.Printf("beehived: serving %d repos %v; default active %q (switch via POST /repo/{name})", len(names), names, names[0])
	}
	log.Printf("beehived listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, s.Routes()))
}
