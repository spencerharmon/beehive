package plan

import (
	"context"
	"fmt"

	"github.com/spencerharmon/beehive/internal/git"
)

// Rollback restores a submodule's PLAN.md to its state at commit ref and stages
// it. The PLAN path is relative to the repo root (e.g. submodules/x/PLAN.md).
// The commit is left to the caller so the rollback is reviewable. Deterministic.
func Rollback(ctx context.Context, r *git.Repo, planPath, ref string) error {
	if ref == "" {
		return fmt.Errorf("plan: rollback needs a commit identifier")
	}
	if _, err := r.Run(ctx, "checkout", ref, "--", planPath); err != nil {
		return fmt.Errorf("plan rollback %s @ %s: %w", planPath, ref, err)
	}
	return nil
}
