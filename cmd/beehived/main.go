// Command beehived is the frontend daemon, the only long-running process.
// It serves file-derived read views and git-backed writes over the beehive repo.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spencerharmon/beehive/internal/web"
)

func main() {
	root := flag.String("repo", ".", "beehive repo root")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	// Global-effective config: Defaults -> host file -> in-repo global. The
	// daemon serves all submodules and the editor acts on root-level human files,
	// so there is no single submodule scope here (submodule="").
	cfg, err := config.Resolve(*root, "")
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	r, err := repo.Open(*root)
	if err != nil {
		log.Fatalf("open repo %s: %v", *root, err)
	}
	s, err := web.New(r, cfg)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	log.Printf("beehived listening on %s (repo %s)", *addr, *root)
	log.Fatal(http.ListenAndServe(*addr, s.Routes()))
}
