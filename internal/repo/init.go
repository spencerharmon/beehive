package repo

import (
	"os"
	"path/filepath"

	"github.com/spencerharmon/beehive/prompts"
)

// Init scaffolds an empty beehive repo at root: AGENTS.md (the full honeybee
// protocol), INFRASTRUCTURE.md, and submodules/. Deterministic, no LLM.
// Existing files are left untouched.
func Init(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "submodules"), 0o755); err != nil {
		return err
	}
	files := map[string]string{
		AgentsFile: prompts.Agents,
		InfraFile:  "",
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
