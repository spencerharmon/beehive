package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spencerharmon/beehive/internal/guards"
	"github.com/spencerharmon/beehive/internal/repo"
	"github.com/spf13/cobra"
)

func guardsCmd() *cobra.Command {
	c := &cobra.Command{Use: "guards", Short: "manage diff-scoped mutation guards (GUARDS.md)"}
	c.AddCommand(guardsLintCmd())
	return c
}

// guardsLintCmd validates a submodule's GUARDS.md (the diff-scoped mutation-guard
// registry, at the submodule repo root) at AUTHOR time — the symmetry to the
// runner's baseline parse gate. It reports a missing registry (benign), a parse
// error (a stub missing a Protects glob / a Command, a trivially-passing no-op
// Command, a duplicate id), and lists the registered guards with their protected
// globs. Read-only; exits non-zero on a parse error so a hook/CI can gate on it.
// It does NOT run guards or judge policy — that is the four-way authoring test's job.
func guardsLintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lint <submodule>",
		Short: "validate a submodule's GUARDS.md registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			subName, err := taskSubmoduleName(args[0])
			if err != nil {
				return err
			}
			sub := repo.Submodule{Name: subName, Path: filepath.Join(root, "submodules", subName)}
			path := filepath.Join(sub.RepoDir(), guards.GuardsFile)
			g, err := guards.Load(path)
			if errors.Is(err, guards.ErrNoGuardsFile) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: no %s (submodule declares no mutation guards)\n", subName, guards.GuardsFile)
				return nil
			}
			if err != nil {
				return fmt.Errorf("%s/%s does not validate: %w", subName, guards.GuardsFile, err)
			}
			if len(g.Stubs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s parses but registers no guards\n", subName, guards.GuardsFile)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s OK — %d guard(s):\n", subName, guards.GuardsFile, len(g.Stubs))
			for _, st := range g.Stubs {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-32s protects %v -> %s\n", st.ID, st.ProtectsRaw, st.Command)
			}
			return nil
		},
	}
	return cmd
}
