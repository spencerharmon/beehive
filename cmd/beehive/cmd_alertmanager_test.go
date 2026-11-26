package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAlertmanagerDesiredReadsCommittedConditionsWithoutHostConfig(t *testing.T) {
	root := t.TempDir()
	host := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", host)
	// A malformed host config proves this diagnostic does not resolve/read it.
	if err := os.WriteFile(filepath.Join(host, "config.yaml"), []byte("alertmanager_url: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDo(t, root, "init", "-q", "-b", "main")
	gitDo(t, root, "config", "user.name", "test")
	gitDo(t, root, "config", "user.email", "test@example.com")
	path := filepath.Join(root, "submodules", "fixture", "PLAN.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := "# Plan\n\n## blocked [NEEDS-HUMAN] <!-- attempts=0 deps= weight=1 category=secret -->\nHuman-needed: fixture secret\n"
	if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDo(t, root, "add", "-A")
	gitDo(t, root, "commit", "-qm", "condition")
	rev := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(path, []byte("# uncommitted removal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := alertmanagerCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"desired", "--root", root, "--instance", "production"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Revision string
		Labels   []map[string]string
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != rev || len(got.Labels) != 1 || got.Labels[0]["task"] != "blocked" || got.Labels[0]["instance"] != "production" {
		t.Fatalf("wrong committed conditions: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "beehive", "alertmanager-sent-production.json")); !os.IsNotExist(err) {
		t.Fatalf("diagnostic must not write a notifier sent set: %v", err)
	}
}
