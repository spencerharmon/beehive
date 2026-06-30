package repo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/instruct"
)

// Init scaffolds a beehive repo at root: a git repository checked out on main,
// the submodules/ tree, an empty INFRASTRUCTURE.md, and the managed instruction
// files (AGENTS.md, HONEYBEE.md, BOOTSTRAP.md) from the binary's defaults.
// Deterministic, no LLM. Existing files are left untouched.
func Init(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := ensureGitMain(root); err != nil {
		return err
	}
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

func ensureGitMain(root string) error {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if _, err := runGit(root, "init"); err != nil {
			return err
		}
	}
	if _, err := runGit(root, "rev-parse", "--git-dir"); err != nil {
		return err
	}

	if _, err := runGit(root, "show-ref", "--verify", "--quiet", "refs/heads/main"); err == nil {
		_, err = runGit(root, "checkout", "main")
		return err
	}

	if _, err := runGit(root, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		_, err = runGit(root, "checkout", "-b", "main")
		return err
	}

	_, err := runGit(root, "symbolic-ref", "HEAD", "refs/heads/main")
	return err
}

func runGit(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
