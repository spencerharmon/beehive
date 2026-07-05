// Package secrets reads and writes SECRETS.yaml.gpg: a single gpg-encrypted yaml
// document (no document separators) holding key/value secrets. A global file
// lives at repo root; per-submodule files live at submodules/<name>/. Encryption
// shells out to gpg using the configured GPGHome keyring. Deterministic; no LLM.
//
// GPGHome is REQUIRED for any gpg operation: an empty GPGHome is a hard error,
// never a silent fall-through to gpg's process-default keyring. In a multi-repo
// daemon every repo carries its OWN keyring (config.RepoEntry.Config), so a
// blank home could only mean a mis-wired caller — and letting it reach the shared
// default keyring would break the per-repo secret isolation the registry
// guarantees. Fail loudly instead.
package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spencerharmon/beehive/internal/repo"
	"gopkg.in/yaml.v3"
)

// Store decrypts/encrypts one SECRETS.yaml.gpg via gpg with GPGHome + Recipient.
type Store struct {
	Path      string // path to SECRETS.yaml.gpg
	GPGHome   string // keyring dir (REQUIRED; empty is an error, never gpg default)
	Recipient string // gpg recipient (key id/email) for encryption
}

// GlobalPath returns the repo-root global secrets path.
func GlobalPath(root string) string { return filepath.Join(root, repo.SecretsFile) }

// SubmodulePath returns the per-submodule secrets path.
func SubmodulePath(root, name string) string {
	return filepath.Join(root, "submodules", name, repo.SecretsFile)
}

// base returns the shared gpg args (keyring home + batch flags), erroring when no
// keyring is configured. An empty GPGHome is REFUSED rather than run against
// gpg's process-default keyring, so a mis-wired caller can never cross the
// per-repo secret-isolation boundary (no shared-keyring fallback).
func (s Store) base() ([]string, error) {
	if s.GPGHome == "" {
		return nil, errors.New("secrets: no gpg keyring configured (refusing shared-keyring fallback)")
	}
	return []string{"--homedir", s.GPGHome, "--batch", "--yes"}, nil
}

// Load decrypts the file into a single yaml document. A missing file is an empty
// map, not an error.
func (s Store) Load(ctx context.Context) (map[string]any, error) {
	enc, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(enc)) == 0 {
		return map[string]any{}, nil
	}
	args, err := s.base()
	if err != nil {
		return nil, err
	}
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "gpg", append(args, "--decrypt")...)
	cmd.Stdin = bytes.NewReader(enc)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gpg decrypt %s: %w: %s", s.Path, err, errb.String())
	}
	doc := map[string]any{}
	if err := yamlSingleDoc(out.Bytes(), &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// Save encrypts a single yaml document to the file, replacing any prior content.
func (s Store) Save(ctx context.Context, doc map[string]any) error {
	if s.Recipient == "" {
		return errors.New("secrets: no gpg recipient configured")
	}
	args, err := s.base()
	if err != nil {
		return err
	}
	plain, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "gpg", append(args, "--recipient", s.Recipient, "--encrypt")...)
	cmd.Stdin = bytes.NewReader(plain)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gpg encrypt %s: %w: %s", s.Path, err, errb.String())
	}
	return os.WriteFile(s.Path, out.Bytes(), 0o600)
}

// Add merges a yaml file's keys, failing on any existing key collision.
func (s Store) Add(ctx context.Context, yamlFile string) error {
	return s.merge(ctx, yamlFile, false)
}

// Update merges a yaml file's keys, overwriting existing keys.
func (s Store) Update(ctx context.Context, yamlFile string) error {
	return s.merge(ctx, yamlFile, true)
}

func (s Store) merge(ctx context.Context, yamlFile string, overwrite bool) error {
	b, err := os.ReadFile(yamlFile)
	if err != nil {
		return err
	}
	incoming := map[string]any{}
	if err := yamlSingleDoc(b, &incoming); err != nil {
		return err
	}
	cur, err := s.Load(ctx)
	if err != nil {
		return err
	}
	for k, v := range incoming {
		if _, ok := cur[k]; ok && !overwrite {
			return fmt.Errorf("secrets: key %q exists; use update", k)
		}
		cur[k] = v
	}
	return s.Save(ctx, cur)
}

// Edit opens the decrypted doc in $EDITOR and re-encrypts the result.
func (s Store) Edit(ctx context.Context) error {
	cur, err := s.Load(ctx)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "beehive-secret-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	plain, _ := yaml.Marshal(cur)
	if _, err := tmp.Write(plain); err != nil {
		return err
	}
	tmp.Close()
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.CommandContext(ctx, editor, tmp.Name())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
	}
	edited, err := os.ReadFile(tmp.Name())
	if err != nil {
		return err
	}
	doc := map[string]any{}
	if err := yamlSingleDoc(edited, &doc); err != nil {
		return err
	}
	return s.Save(ctx, doc)
}

// yamlSingleDoc enforces the no-separator rule: a document separator yields error.
func yamlSingleDoc(b []byte, out *map[string]any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("secrets: parse yaml: %w", err)
	}
	if err := dec.Decode(&map[string]any{}); err == nil {
		return errors.New("secrets: multiple yaml documents not allowed")
	}
	return nil
}
