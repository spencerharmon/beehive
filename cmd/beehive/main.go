// Command beehive is the deterministic CLI: repo init, submodule add/link, plan
// archive (lean DONE cards), secret add/update/edit, worktree add/rm, task human
// escalation, honeybee start, and git-hook
// install (ROI-protect pre-commit + submodule-sync post-receive). No LLM; every
// command is plain git + file ops.
package main

import (
	"fmt"
	"os"

	"github.com/spencerharmon/beehive/internal/version"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "beehive",
		Short:         "beehive deterministic CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Version powers the root `--version` flag (cobra auto-registers it). The
		// precise release+commit is stamped via -ldflags at deploy/release time
		// (internal/version); an unstamped dev build prints "beehive dev".
		Version: version.String(),
	}
	// Print exactly the version line (no "beehive version <x>" wrapper) so
	// `beehive --version` and `beehive version` agree byte-for-byte.
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(
		initCmd(),
		versionCmd(),
		submoduleCmd(),
		secretCmd(),
		worktreeCmd(),
		gitCmd(),
		editCmd(),
		taskCmd(),
		planCmd(),
		guardsCmd(),
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
