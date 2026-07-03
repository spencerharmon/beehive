// Command beehived is the frontend daemon, the only long-running process.
// It serves file-derived read views and git-backed writes over the beehive repo.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"strings"

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
	// Hand the WHOLE registry to the web server: it serves every registered repo
	// under its own routes, resolving each entry's own repo layout + keyring once
	// and selecting the active repo per request. A one-entry registry (the bare
	// install's synthesized entry) keeps today's flat single-repo routes, so an
	// unconfigured host is unchanged by the registry indirection.
	s, err := web.NewMulti(reg)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	// Startup housekeeping across every served repo: recover in-flight editor
	// sessions and prune stale edit worktrees a prior beehived left behind.
	// Best-effort — a failure here must not stop the daemon from serving.
	if err := s.RecoverEditors(context.Background()); err != nil {
		log.Printf("editor recovery: %v", err)
	}
	if names := reg.Names(); len(names) == 1 {
		log.Printf("beehived listening on %s (repo %s)", *addr, names[0])
	} else {
		log.Printf("beehived listening on %s (%d repos: %s)", *addr, len(names), strings.Join(names, ", "))
	}
	log.Fatal(http.ListenAndServe(*addr, s.Routes()))
}
