package repo

import (
	"os"
	"path/filepath"

	"github.com/spencerharmon/beehive/prompts"
)

// Init scaffolds an empty beehive repo at root: a slim repo-local AGENTS.md
// (a repo marker + local rules; the full honeybee protocol ships in the binary
// as the runtime system prompt, so it never freezes here), INFRASTRUCTURE.md,
// and submodules/. Deterministic, no LLM. Existing files are left untouched.
func Init(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "submodules"), 0o755); err != nil {
		return err
	}
	files := map[string]string{
		AgentsFile: prompts.RepoAgents,
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
