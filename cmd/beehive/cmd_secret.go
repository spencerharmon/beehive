package main

import (
	"fmt"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/secrets"
	"github.com/spf13/cobra"
)

// secretStore builds the secrets.Store for the beehive repo at root, scoped to
// the ACTIVE repo's OWN gpg keyring — never a process-global keyring.
//
//   - Multi-repo (a host repos.yaml is present): the entry whose root is this
//     repo supplies the keyring via RepoEntry.Config, exactly as the daemon wires
//     each per-repo server. A root that is NOT a registered repo is a hard error:
//     we refuse to fall back to a shared/global keyring (the isolation guarantee).
//   - Legacy single-repo (no repos.yaml): the host-layer config keyring is used,
//     byte-identical to before, so bare installs are unchanged.
//
// recipient, when non-empty, overrides the resolved recipient (an explicit
// operator choice); submodule selects the per-submodule secrets path under the
// same repo root and keyring.
func secretStore(root, submodule, recipient string) (secrets.Store, error) {
	reg, err := config.LoadRegistry()
	if err != nil {
		return secrets.Store{}, err
	}
	var cfg config.Config
	if reg.Empty() {
		// Legacy single-repo path: unchanged host-layer keyring resolution.
		if cfg, err = config.Load(); err != nil {
			return secrets.Store{}, err
		}
	} else {
		e, ok := reg.RepoByRoot(root)
		if !ok {
			return secrets.Store{}, fmt.Errorf(
				"beehive secret: repo at %s is not registered in %s; refusing a shared-keyring fallback (register it with its own gpg_home/gpg_recipient)",
				root, config.RegistryFile)
		}
		base, err := config.Resolve(root, "")
		if err != nil {
			return secrets.Store{}, err
		}
		cfg = e.Config(base) // per-repo isolated keyring (GPGHome + GPGRecipient)
	}
	rcpt := recipient
	if rcpt == "" {
		rcpt = cfg.GPGRecipient
	}
	path := secrets.GlobalPath(root)
	if submodule != "" {
		path = secrets.SubmodulePath(root, submodule)
	}
	return secrets.Store{Path: path, GPGHome: cfg.GPGHome, Recipient: rcpt}, nil
}

func secretCmd() *cobra.Command {
	var submodule, recipient string
	c := &cobra.Command{Use: "secret", Short: "manage gpg-encrypted SECRETS.yaml.gpg"}
	c.PersistentFlags().StringVar(&submodule, "submodule", "", "per-submodule secrets (default: global)")
	c.PersistentFlags().StringVar(&recipient, "recipient", "", "gpg recipient (default: config gpg_recipient)")

	store := func() (secrets.Store, error) {
		root, err := findRoot()
		if err != nil {
			return secrets.Store{}, err
		}
		return secretStore(root, submodule, recipient)
	}

	var file string
	add := &cobra.Command{
		Use: "add", Short: "add new keys from a yaml file (fails on collision)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			if err := s.Add(cmd.Context(), file); err != nil {
				return err
			}
			fmt.Println("secrets added")
			return nil
		},
	}
	add.Flags().StringVarP(&file, "file", "f", "", "yaml file to merge")
	add.MarkFlagRequired("file")

	var ufile string
	update := &cobra.Command{
		Use: "update", Short: "update keys from a yaml file (overwrites)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			if err := s.Update(cmd.Context(), ufile); err != nil {
				return err
			}
			fmt.Println("secrets updated")
			return nil
		},
	}
	update.Flags().StringVarP(&ufile, "file", "f", "", "yaml file to merge")
	update.MarkFlagRequired("file")

	edit := &cobra.Command{
		Use: "edit", Short: "edit decrypted secrets in $EDITOR",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			if err := s.Edit(cmd.Context()); err != nil {
				return err
			}
			fmt.Println("secrets saved")
			return nil
		},
	}
	c.AddCommand(add, update, edit)
	return c
}
