package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAlertmanagerURLLayering(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	c, err := resolve(dir, root, "")
	if err != nil || c.AlertmanagerURL != "" {
		t.Fatalf("default must be unset/disabled: %q %v", c.AlertmanagerURL, err)
	}
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("alertmanager_url: https://am.example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = resolve(dir, root, "")
	if err != nil || c.AlertmanagerURL != "https://am.example.com" {
		t.Fatalf("alertmanager_url not loaded: %q %v", c.AlertmanagerURL, err)
	}
}
