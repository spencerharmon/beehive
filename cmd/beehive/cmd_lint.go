package main

import (
	"fmt"
	"strings"

	"github.com/spencerharmon/beehive/internal/repo"
	selectt "github.com/spencerharmon/beehive/internal/select"
	"github.com/spf13/cobra"
)

// lintCmd validates the combined cross-submodule dependency graph. It powers the
// pre-commit guard: a PLAN.md dep-tag commit that forms a wait cycle is rejected.
func lintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint",
		Short: "validate the combined cross-submodule dependency graph is acyclic",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			rp, err := repo.Open(root)
			if err != nil {
				return err
			}
			g, err := selectt.LoadEdges(rp)
			if err != nil {
				return err
			}
			if c := g.Validate(); c != nil {
				return fmt.Errorf("dependency cycle: %s", strings.Join(c, " -> "))
			}
			fmt.Println("beehive: dependency graph OK")
			return nil
		},
	}
}
