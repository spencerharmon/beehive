package swarm

import (
	"net/http"
	"time"

	"github.com/spencerharmon/beehive/internal/config"
)

// BuildClient constructs the Harness (Client) implementation selected by the
// resolved config's Harness field, wiring model/temperature/max-token/idle-
// timeout settings identically to how the internal/swarm consumers
// (cmd/honeybee, internal/editor) already built *Opencode directly, so an
// install that never sets `harness:` gets a byte-identical *Opencode client to
// before this selector existed.
//
//   - cfg.Harness == "" or "opencode" (the default, resolved by
//     config.Defaults/config.Resolve when no layer sets the key): returns
//     *Opencode configured from cfg.AgentURL/cfg.Model/cfg.Temperature/
//     cfg.MaxTokens plus the given idle timeout.
//   - cfg.Harness == "pi": returns *Pi configured from cfg.PiBin/cfg.Model/
//     cfg.PiThinking/cfg.Temperature/cfg.MaxTokens plus the given idle
//     timeout.
//
// idle is the caller's already-resolved per-turn idle-watchdog duration (each
// consumer derives it slightly differently — cmd/honeybee from
// cfg.TurnIdleTimeoutMinutes directly, internal/editor via its own
// idleTimeout() helper with a 5-minute fallback — so BuildClient takes the
// resolved value rather than re-deriving it, keeping each caller's existing
// idle-timeout behavior unchanged).
//
// The returned Client is exactly the harness-interface (Client+Session,
// see swarm.go) type a caller type-asserts against for harness-specific
// follow-up (e.g. cmd/honeybee's ephemeral-opencode-server spawn, which only
// applies to *Opencode); BuildClient itself does no such follow-up.
func BuildClient(cfg config.Config, idle time.Duration) Client {
	switch cfg.Harness {
	case "pi":
		return &Pi{
			Bin:         cfg.PiBin,
			Model:       cfg.Model,
			Thinking:    cfg.PiThinking,
			Temperature: cfg.Temperature,
			MaxTokens:   cfg.MaxTokens,
			IdleTimeout: idle,
		}
	default: // "" and "opencode" both resolve to the historical default driver
		return &Opencode{
			Base:        cfg.AgentURL,
			Model:       cfg.Model,
			Temperature: cfg.Temperature,
			MaxTokens:   cfg.MaxTokens,
			HTTP:        &http.Client{Timeout: 0},
			IdleTimeout: idle,
		}
	}
}
