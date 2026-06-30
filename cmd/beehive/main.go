// Command beehive is the deterministic CLI: repo init, submodule add/link, plan
// rollback, secret add/update/edit, worktree add/rm, honeybee start, and git-hook
// install (ROI-protect pre-commit + submodule-sync post-receive). No LLM; every
// command is plain git + file ops.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "beehive",
		Short:         "beehive deterministic CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		initCmd(),
		versionCmd(),
		submoduleCmd(),
		secretCmd(),
		worktreeCmd(),
		honeybeeCmd(),
		hookCmd(),
		lintCmd(),
		auditCmd(),
		instructionCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "beehive:", err)
		os.Exit(1)
	}
}
