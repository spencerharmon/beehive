package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"gopkg.in/yaml.v3"
)

// listSecretKeys decrypts SECRETS.yaml.gpg and returns sorted top-level keys
// only. Values are never exposed. Missing file => no keys, no error. gpgHome is
// the ACTIVE repo's keyring and is REQUIRED: an empty home is refused (fail
// loudly) rather than run against gpg's process-default keyring, so a request
// can never read secrets through a shared keyring.
func listSecretKeys(ctx context.Context, gpgHome, path string) ([]string, error) {
	if gpgHome == "" {
		return nil, errors.New("secrets: no gpg keyring configured (refusing shared-keyring fallback)")
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	plain, err := gpgDecrypt(ctx, gpgHome, path)
	if err != nil {
		return nil, err
	}
	m := map[string]interface{}{}
	if err := yaml.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// setSecret decrypts (if present), sets key=value, and re-encrypts the doc.
// gpgHome/recipient are the ACTIVE repo's keyring and are REQUIRED: an empty home
// is refused (fail loudly) so a write can never land in a shared keyring.
func setSecret(ctx context.Context, gpgHome, path, recipient, key, value string) error {
	if gpgHome == "" {
		return errors.New("secrets: no gpg keyring configured (refusing shared-keyring fallback)")
	}
	m := map[string]interface{}{}
	if _, err := os.Stat(path); err == nil {
		plain, err := gpgDecrypt(ctx, gpgHome, path)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(plain, &m); err != nil {
			return fmt.Errorf("parse secrets: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	m[key] = value
	plain, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return gpgEncrypt(ctx, gpgHome, path, recipient, plain)
}

func gpgDecrypt(ctx context.Context, gpgHome, path string) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "gpg", "--homedir", gpgHome, "--batch", "--yes", "--decrypt", path)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gpg decrypt: %w: %s", err, errb.String())
	}
	return out.Bytes(), nil
}

func gpgEncrypt(ctx context.Context, gpgHome, path, recipient string, plain []byte) error {
	if recipient == "" {
		return errors.New("secrets: no gpg recipient configured")
	}
	var errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "gpg", "--homedir", gpgHome, "--batch", "--yes",
		"--recipient", recipient, "--output", path, "--encrypt")
	cmd.Stdin = bytes.NewReader(plain)
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gpg encrypt: %w: %s", err, errb.String())
	}
	return nil
}
