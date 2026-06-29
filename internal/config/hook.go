package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// roiHook rejects commits that touch ROI.md when authored by a honeybee identity.
// Honeybees export BEEHIVE_HONEYBEE=1; only the frontend (unset) may change ROI.md.
const roiHook = `#!/usr/bin/env sh
# beehive ROI-protect hook (installed by CLI). ROI.md is human-owned; honeybees
# must never change it. Enforced here for local commits; a server pre-receive
# mirrors this for pushes.
[ "${BEEHIVE_HONEYBEE:-0}" = "1" ] || exit 0
if git diff --cached --name-only | grep -E '(^|/)ROI\.md$' >/dev/null; then
  echo "beehive: honeybee identity may not modify ROI.md" >&2
  exit 1
fi
exit 0
`

// InstallROIHook writes the ROI-protect pre-commit hook into the repo's .git dir.
func InstallROIHook(repoRoot string) error {
	dir := filepath.Join(repoRoot, ".git", "hooks")
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", repoRoot)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, "pre-commit")
	return os.WriteFile(p, []byte(roiHook), 0o755)
}
