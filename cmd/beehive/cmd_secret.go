package main

import (
	"fmt"

	"github.com/spencerharmon/beehive/internal/config"
	"github.com/spencerharmon/beehive/internal/secrets"
	"github.com/spf13/cobra"
)

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
		cfg, err := config.Load()
		if err != nil {
			return secrets.Store{}, err
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
