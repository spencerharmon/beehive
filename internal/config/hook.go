package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// preCommitHook is the beehive pre-commit guard. It enforces two repo invariants:
//
//  1. ROI.md is human-owned: a honeybee identity (BEEHIVE_HONEYBEE=1) may never
//     change it. The frontend (env unset) is allowed. A server pre-receive
//     mirrors this for pushes.
//  2. PLAN.md dependency tags stay acyclic: when a commit touches any PLAN.md it
//     runs `beehive lint`, which loads the combined cross-submodule graph and
//     exits non-zero on a wait cycle. This rejects a honeybee (or human) dep-tag
//     commit that would deadlock the swarm, mirroring links.AddDep's CLI-time
//     cycle check for plan writes that never go through the CLI. The check is
//     best-effort: if the beehive binary is not on PATH it is skipped (the lint
//     still runs in CI and on the next CLI invocation).
const preCommitHook = `#!/usr/bin/env sh
# beehive pre-commit guard (installed by CLI). See internal/config/hook.go.
if [ "${BEEHIVE_HONEYBEE:-0}" = "1" ]; then
  if git diff --cached --name-only | grep -E '(^|/)ROI\.md$' >/dev/null; then
    echo "beehive: honeybee identity may not modify ROI.md" >&2
    exit 1
  fi
fi
if git diff --cached --name-only | grep -E '(^|/)PLAN\.md$' >/dev/null; then
  if command -v beehive >/dev/null 2>&1; then
    if ! beehive lint; then
      echo "beehive: PLAN.md dependency tags form a wait cycle (rejected)" >&2
      exit 1
    fi
  fi
fi
exit 0
`

// InstallROIHook writes the beehive pre-commit guard into the repo's .git dir.
// (Named for its original ROI-only role; it now also installs the dep-cycle
// guard. See preCommitHook.)
func InstallROIHook(repoRoot string) error {
	dir := filepath.Join(repoRoot, ".git", "hooks")
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", repoRoot)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, "pre-commit")
	return os.WriteFile(p, []byte(preCommitHook), 0o755)
}
