package config

import (
	"context"
	"fmt"

	"github.com/spencerharmon/beehive/internal/git"
	"gopkg.in/yaml.v3"
)

// ResolveCommittedGlobal resolves the host/default layers plus the hive's
// committed global config. The returned revision pins both the existence check
// and blob read, so a concurrent main advance cannot mix two configurations.
// Unlike Resolve, this ignores dirty/staged config and has no submodule layer:
// it is for hive-wide background services, not per-task agent settings.
func ResolveCommittedGlobal(ctx context.Context, root string) (Config, string, error) {
	base, err := Load()
	if err != nil {
		return Config{}, "", err
	}
	g := git.New(root)
	rev, err := g.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return Config{}, "", err
	}
	entry, err := g.Run(ctx, "ls-tree", "--name-only", rev, "--", FileName)
	if err != nil {
		return Config{}, "", err
	}
	if entry == "" {
		return base, rev, nil
	}
	text, err := g.Run(ctx, "show", rev+":"+FileName)
	if err != nil {
		return Config{}, "", err
	}
	var layer Config
	if err := yaml.Unmarshal([]byte(text), &layer); err != nil {
		return Config{}, "", fmt.Errorf("parse committed %s at %s: %w", FileName, rev, err)
	}
	return merge(base, layer), rev, nil
}
