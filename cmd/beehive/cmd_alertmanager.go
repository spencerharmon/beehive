package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spencerharmon/beehive/internal/alertmanager"
	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/git"
	"github.com/spf13/cobra"
)

func alertmanagerCmd() *cobra.Command {
	c := &cobra.Command{Use: "alertmanager", Short: "outbound-only Alertmanager notifications"}
	c.AddCommand(alertmanagerEmitCmd(), alertmanagerDesiredCmd())
	return c
}

// alertmanagerDesiredCmd is a read-only repository diagnostic: no config/host
// reads, no POST, no receiver GET, and no notifier sent-set mutation. Its output
// lets a deployment verifier compare live labels with actual committed conditions
// without injecting synthetic failures or accidentally resolving production alerts.
func alertmanagerDesiredCmd() *cobra.Command {
	var root, instance string
	cmd := &cobra.Command{
		Use: "desired", Short: "print the repository's desired alert identities without delivering",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			if root == "" {
				if root, err = findRoot(); err != nil {
					return err
				}
			}
			if root, err = filepath.Abs(root); err != nil {
				return err
			}
			if instance == "" {
				instance = filepath.Base(root)
			}
			rev, err := git.New(root).Run(cmd.Context(), "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			desired, err := alertmanager.HiveSource(root, rev, instance, "")(cmd.Context())
			if err != nil {
				return err
			}
			labels := make([]map[string]string, 0, len(desired))
			for _, d := range desired {
				labels = append(labels, d.Labels)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Revision string              `json:"revision"`
				Labels   []map[string]string `json:"labels"`
			}{rev, labels})
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "hive root (default: discovered from cwd)")
	cmd.Flags().StringVar(&instance, "instance", "", "stable instance label (default: hive dir name; use the registry name for beehived)")
	return cmd
}

// alertmanagerEmitCmd runs ONE tick of the production emitter (the same
// NewHiveNotifier beehived runs in the background) against the configured
// alertmanager_url: it POSTs the hive's currently-desired alerts and any pending
// resolutions, then exits. It never reads Alertmanager. Exits non-zero when
// alertmanager_url is unset or delivery fails, so operators and the live
// acceptance harness (tests/alertmanager-live.test.sh) can gate on it.
func alertmanagerEmitCmd() *cobra.Command {
	var root, instance, generator string
	cmd := &cobra.Command{
		Use:   "emit",
		Short: "deliver the hive's current alerts once (production emitter, one tick)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			if root == "" {
				if root, err = findRoot(); err != nil {
					return err
				}
			}
			if root, err = filepath.Abs(root); err != nil {
				return err
			}
			cfg, err := config.Resolve(root, "")
			if err != nil {
				return err
			}
			if strings.TrimSpace(cfg.AlertmanagerURL) == "" {
				return fmt.Errorf("alertmanager_url is not configured (config dir %s)", cfg.Dir)
			}
			if instance == "" {
				instance = filepath.Base(root)
			}
			ctx := context.Background()
			n, err := alertmanager.NewHiveNotifier(ctx, cfg.AlertmanagerURL, root, instance, generator)
			if err != nil {
				return err
			}
			n.Logf = func(f string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", a...) }
			if err := n.Tick(ctx); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "alertmanager: delivered for %s\n", instance)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "hive root (default: discovered from cwd)")
	cmd.Flags().StringVar(&instance, "instance", "", "stable instance label (default: hive dir name; beehived uses the registry repo name)")
	cmd.Flags().StringVar(&generator, "generator-url", "", "beehived external base URL for generatorURL links")
	return cmd
}
