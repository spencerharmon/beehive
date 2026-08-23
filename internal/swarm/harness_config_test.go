package swarm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
)

// writeSwarmCfg writes a config layer file at root/rel, creating parent dirs as
// needed, for TestBuildClientPrecedenceAcrossLayers.
func writeSwarmCfg(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBuildClientDefaultsToOpencode proves that omitting the harness key (a
// zero-value/default Config, exactly what an install with no `harness:` layer
// resolves to — see config.Defaults) makes BuildClient return an *Opencode
// wired identically to the pre-selector construction in cmd/honeybee/main.go
// and internal/editor/editor.go: same Base/Model/Temperature/MaxTokens/
// IdleTimeout, so an existing install's behavior/config surface is
// byte-for-byte unchanged.
func TestBuildClientDefaultsToOpencode(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.AgentURL = "http://127.0.0.1:4096"
	cfg.Model = "provider/model"
	cfg.Temperature = 0.4
	cfg.MaxTokens = 1234
	idle := 15 * time.Minute

	client := BuildClient(cfg, idle)
	oc, ok := client.(*Opencode)
	if !ok {
		t.Fatalf("BuildClient with default Harness = %T, want *Opencode", client)
	}
	if oc.Base != cfg.AgentURL {
		t.Errorf("Base = %q, want %q", oc.Base, cfg.AgentURL)
	}
	if oc.Model != cfg.Model {
		t.Errorf("Model = %q, want %q", oc.Model, cfg.Model)
	}
	if oc.Temperature != cfg.Temperature {
		t.Errorf("Temperature = %v, want %v", oc.Temperature, cfg.Temperature)
	}
	if oc.MaxTokens != cfg.MaxTokens {
		t.Errorf("MaxTokens = %d, want %d", oc.MaxTokens, cfg.MaxTokens)
	}
	if oc.IdleTimeout != idle {
		t.Errorf("IdleTimeout = %v, want %v", oc.IdleTimeout, idle)
	}
	if oc.HTTP == nil {
		t.Error("HTTP client must be set (non-nil) exactly as the historical construction did")
	}
}

// TestBuildClientEmptyHarnessIsOpencode confirms an explicit empty string
// (never went through config.Defaults) still resolves to *Opencode — the
// switch's default case, not merely config.Defaults' seeded value — so any
// caller path that ends up with a zero Config still gets the historical
// driver.
func TestBuildClientEmptyHarnessIsOpencode(t *testing.T) {
	var cfg config.Config
	client := BuildClient(cfg, time.Minute)
	if _, ok := client.(*Opencode); !ok {
		t.Fatalf("BuildClient with Harness=%q = %T, want *Opencode", cfg.Harness, client)
	}
}

// TestBuildClientPiSelectsPiDriver proves harness=pi resolves *Pi wired from
// the pi-specific config fields (PiBin/PiThinking) plus the SAME shared
// Model/Temperature/MaxTokens/idle-timeout knobs the opencode path uses, so
// switching harness never silently drops the shared agent settings.
func TestBuildClientPiSelectsPiDriver(t *testing.T) {
	cfg := config.Config{
		Harness:     "pi",
		PiBin:       "/opt/pi/bin/pi",
		PiThinking:  "high",
		Model:       "anthropic/claude",
		Temperature: 0.7,
		MaxTokens:   999,
	}
	idle := 3 * time.Minute

	client := BuildClient(cfg, idle)
	pi, ok := client.(*Pi)
	if !ok {
		t.Fatalf("BuildClient with Harness=pi = %T, want *Pi", client)
	}
	if pi.Bin != cfg.PiBin {
		t.Errorf("Bin = %q, want %q", pi.Bin, cfg.PiBin)
	}
	if pi.Thinking != cfg.PiThinking {
		t.Errorf("Thinking = %q, want %q", pi.Thinking, cfg.PiThinking)
	}
	if pi.Model != cfg.Model {
		t.Errorf("Model = %q, want %q", pi.Model, cfg.Model)
	}
	if pi.Temperature != cfg.Temperature {
		t.Errorf("Temperature = %v, want %v", pi.Temperature, cfg.Temperature)
	}
	if pi.MaxTokens != cfg.MaxTokens {
		t.Errorf("MaxTokens = %d, want %d", pi.MaxTokens, cfg.MaxTokens)
	}
	if pi.IdleTimeout != idle {
		t.Errorf("IdleTimeout = %v, want %v", pi.IdleTimeout, idle)
	}
}

// TestBuildClientPrecedenceAcrossLayers is the end-to-end proof the task asks
// for: resolve a layered config (config.Resolve, exercising the real
// host/global/per-submodule precedence — see internal/config's own
// TestResolveHarnessLayering for the merge-rule unit coverage) and confirm
// BuildClient instantiates the harness that resolution actually selected, at
// each scope in turn.
func TestBuildClientPrecedenceAcrossLayers(t *testing.T) {
	hostDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("BEEHIVE_CONFIG_DIR", hostDir)

	// Global default: opencode (no layer sets harness anywhere yet).
	cfg, err := config.Resolve(root, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := BuildClient(cfg, time.Minute).(*Opencode); !ok {
		t.Fatal("bare install must resolve *Opencode")
	}

	// In-repo global sets harness=pi: every submodule with no override now gets pi.
	writeSwarmCfg(t, root, "config.yaml", "harness: pi\npi_bin: pi\n")
	cfg, err = config.Resolve(root, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := BuildClient(cfg, time.Minute).(*Pi); !ok {
		t.Fatal("global harness=pi must resolve *Pi for a submodule with no override")
	}

	// Per-submodule override wins back to opencode for THIS submodule only.
	writeSwarmCfg(t, root, "submodules/sub/config.yaml", "harness: opencode\n")
	cfg, err = config.Resolve(root, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := BuildClient(cfg, time.Minute).(*Opencode); !ok {
		t.Fatal("per-submodule harness=opencode must override the global pi setting")
	}

	// A sibling submodule with no override still resolves the global pi setting.
	cfg, err = config.Resolve(root, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := BuildClient(cfg, time.Minute).(*Pi); !ok {
		t.Fatal("sibling submodule with no override must still resolve *Pi (global falls through)")
	}
}
