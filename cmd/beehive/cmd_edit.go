package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spencerharmon/beehive/internal/editor"
	"github.com/spf13/cobra"
)

// editCmd is the first-class, deterministic counterpart to the beehived "edit
// with AI" flow: a single call that runs the worktree -> write -> publish ->
// cleanup sequence for an operator-directed ROI/hive-layer edit, end to end,
// with no LLM in the loop (hive-edit-command). It shares
// internal/editor.PublishFile/PublishWorktree with the chat editor's
// Session.Merge, so this and the frontend "edit with AI" flow converge to main
// through the identical git sequence — replacing the manual Path-B worktree
// checklist in skills/modify-roi.md and skills/shared-checkout-edits.md with one
// verb. It cannot bypass the ROI honeybee-identity commit hook: the commit is
// authored as this process's own git identity (never BEEHIVE_HONEYBEE=1), the
// same as every other plain `beehive` CLI write.
func editCmd() *cobra.Command {
	var contentFile string
	var message string
	var confirmDelete bool
	c := &cobra.Command{
		Use:   "edit <file>",
		Short: "publish a one-shot edit to an ROI/hive-layer file via worktree -> publish -> cleanup",
		Long: `edit performs, as one deterministic call, the exact worktree -> write ->
commit -> publish-to-main -> update-local-main -> cleanup sequence
internal/editor.Session.Merge implements for the beehived "edit with AI" chat
flow — for an operator who already has the new content and wants no LLM in the
loop. <file> is a repo-relative coordination file: an ROI.md, INFRASTRUCTURE.md,
RULES.md, AGENTS.md, ARTIFACTS.md under a submodule, SUBMODULE-LINKS.yaml, or a
root instruction file — exactly the editable set the frontend's "edit with AI"
link exposes (never PLAN.md, secrets, or submodule code).

New content is read from --content-file, or from stdin when that flag is
omitted. A whole-file deletion of a human-owned file (ROI.md) is blocked unless
--confirm-delete is given.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := args[0]
			root, err := findRoot()
			if err != nil {
				return err
			}
			var content []byte
			if contentFile != "" {
				content, err = os.ReadFile(contentFile)
			} else {
				content, err = io.ReadAll(cmd.InOrStdin())
			}
			if err != nil {
				return fmt.Errorf("read content: %w", err)
			}
			return editor.PublishFile(cmd.Context(), root, file, string(content), message, confirmDelete)
		},
	}
	c.Flags().StringVar(&contentFile, "content-file", "", "path to a file holding the new content (default: read from stdin)")
	c.Flags().StringVar(&message, "message", "", `commit message (default "editor: <file>")`)
	c.Flags().BoolVar(&confirmDelete, "confirm-delete", false, "explicitly confirm a whole-file deletion of a human-owned file (e.g. ROI.md)")
	return c
}
