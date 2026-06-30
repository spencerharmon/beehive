package repo

import (
	"os"
	"path/filepath"

	"github.com/spencerharmon/beehive/internal/instruct"
)

// Init scaffolds an empty beehive repo at root: the submodules/ tree, an empty
// INFRASTRUCTURE.md, and the managed instruction files (AGENTS.md, HONEYBEE.md,
// BOOTSTRAP.md) from the binary's defaults. Deterministic, no LLM. Existing files
// are left untouched.
func Init(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "submodules"), 0o755); err != nil {
		return err
	}
	if _, err := instruct.Install(root); err != nil {
		return err
	}
	files := map[string]string{
		InfraFile: "",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}
