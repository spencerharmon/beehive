// Command beehive is the deterministic CLI. P0 ships init + version; P1 adds
// submodule, secret, link, worktree, honeybee subcommands.
package main

import (
	"fmt"
	"os"

	"github.com/spencerharmon/beehive/internal/repo"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: beehive <init|version> [args]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: beehive init <path>")
			os.Exit(2)
		}
		if err := repo.Init(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "init:", err)
			os.Exit(1)
		}
		fmt.Println("beehive repo at", os.Args[2])
	case "version":
		fmt.Println("beehive dev")
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		os.Exit(2)
	}
}
