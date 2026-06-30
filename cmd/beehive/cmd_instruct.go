package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/instruct"
	"github.com/spf13/cobra"
)

// instructionCmd manages the beehive-shipped instruction files (AGENTS.md,
// HONEYBEE.md, BOOTSTRAP.md) at a repo root: list their drift, or update them to
// the binary's current defaults (backing up any the operator customized).
func instructionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "instruction",
		Aliases: []string{"instructions"},
		Short:   "manage beehive instruction files (AGENTS.md, HONEYBEE.md, BOOTSTRAP.md)",
	}
	c.AddCommand(instructionListCmd(), instructionUpdateCmd())
	return c
}

func instructionListCmd() *cobra.Command {
	var repoDir string
	c := &cobra.Command{
		Use:   "list",
		Short: "show each managed instruction file and whether it matches the current default",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := filepath.Abs(repoDir)
			if err != nil {
				return err
			}
			st, err := instruct.Scan(root)
			if err != nil {
				return err
			}
			for _, f := range instruct.Files() {
				fmt.Printf("%-14s %s\n", f.Name, st[f.Name])
			}
			return nil
		},
	}
	c.Flags().StringVar(&repoDir, "repo", ".", "beehive repo root")
	return c
}

func instructionUpdateCmd() *cobra.Command {
	var repoDir string
	var clobber bool
	var yes bool
	c := &cobra.Command{
		Use:   "update",
		Short: "refresh managed instruction files to the binary's current defaults",
		Long: "Rewrites AGENTS.md, HONEYBEE.md and BOOTSTRAP.md to the binary's current\n" +
			"defaults and commits the change. A missing file is created; an unchanged\n" +
			"file is left alone. A file you have modified is, by default, offered for\n" +
			"confirmation; with --clobber (or --yes) it is backed up to\n" +
			"<name>.<epoch>.bak and replaced, committing both. LOCALS.md and per-repo\n" +
			"content are never touched.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := filepath.Abs(repoDir)
			if err != nil {
				return err
			}
			confirm := instruct.Confirm(func(name string) bool {
				if yes {
					return true
				}
				return promptYesNo(fmt.Sprintf(
					"%s differs from the new default. Overwrite (backup kept)?", name))
			})
			results, err := instruct.Update(cmd.Context(), root, clobber, confirm)
			if err != nil {
				return err
			}
			for _, r := range results {
				if r.Backup != "" {
					fmt.Printf("%-14s %s (backup: %s)\n", r.Name, r.Action, r.Backup)
				} else {
					fmt.Printf("%-14s %s\n", r.Name, r.Action)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&repoDir, "repo", ".", "beehive repo root")
	c.Flags().BoolVar(&clobber, "clobber", false, "overwrite modified files without prompting (backup kept)")
	c.Flags().BoolVar(&yes, "yes", false, "answer yes to every overwrite prompt (backup kept)")
	return c
}

// promptYesNo asks a y/N question on stdin. A non-interactive or empty answer is
// treated as "no" so an automated run never silently clobbers customized files.
func promptYesNo(q string) bool {
	fmt.Printf("%s [y/N] ", q)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
