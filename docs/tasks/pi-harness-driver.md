# pi harness driver — a second Harness implementation behind pi-harness-interface

## Problem

`internal/swarm` had exactly one Harness implementation, `*Opencode`
(`opencode.go`): every honeybee turn, the chat-diff editor, and the
resolution/bootstrap agents drove opencode's HTTP session API directly.
`pi-harness-interface` already carved the `Client`/`Session` contract both
callers depend on (`swarm.go`, proven satisfiable by a non-opencode fake in
`harness_test.go`), but nothing implemented it for
[pi](https://pi.dev/), the second coding agent an install might want to run
honeybee/editor turns on.

## What changed

`internal/swarm/pi.go`: `*Pi` (a `Client`) and `*piSession` (a `Session`)
implementing the exact same contract `*Opencode`/`*ocSession` do, so an
install can swap `Runner.Client`/the editor's client field to a `*Pi` value
with no other code change.

- **Integration**: pi's RPC mode (`pi --mode rpc`), a line-delimited JSON
  protocol over stdin/stdout
  ([docs](https://pi.dev/docs/latest/rpc)) — never pi's interactive TUI.
  `Open` spawns one `pi --mode rpc --no-session ...` process per session and
  keeps it running for the session's whole lifetime; every `Prompt` call sends
  one `{"type":"prompt",...}` command to the SAME process, so pi's own
  in-process conversation state (not anything this driver tracks) provides
  the "continue" semantics — a second `Prompt` needs no extra bookkeeping.
- **Idle watchdog**: `awaitSettled` waits for pi's `agent_settled` event,
  resetting an idle clock on ANY stdout activity (any event line at all —
  `message_update` deltas, `tool_execution_*`, etc.), and — if nothing arrives
  for `Pi.IdleTimeout` — sends `abort` and returns the exact same `ErrTurnIdle`
  sentinel `*Opencode` returns, so the runner's turn loop needs no
  harness-specific branch to detect a stalled pass.
- **Model/config knobs**: `Pi.Model` ("provider/model") splits to pi's
  `--provider`/`--model` flags at spawn time; `Pi.Thinking` maps to
  `--thinking`. `Pi.SetModel` makes `*Pi` satisfy the optional `modelSelector`
  capability the runner's per-kind routing (`Runner.ModelFor`) already checks
  for via a type assertion — proven by the same
  `TestHarnessContractModelSelectorOptional`-shaped compile-time assertions in
  `pi_test.go`. `Temperature`/`MaxTokens` fields exist on `*Pi` for structural
  parity with `*Opencode` (a caller configuring a `Harness` generically never
  fails on a missing field) but are currently unused: pi's RPC protocol has no
  per-session temperature/max-tokens knob — model and thinking level are the
  levers it exposes.
- **Context loading**: the caller's assembled system prompt
  (HONEYBEE.md + AGENTS.md + task brief — identical to what `*Opencode` is
  handed) is passed as `--append-system-prompt` at spawn time, so it is
  APPENDED to (never replaces) whatever pi's own `AGENTS.md`/`CLAUDE.md`/skill
  discovery already loads for the project directory — the full brief reaches
  pi without double-injecting or losing any part of it, and pi's native
  context path still runs on top.
- **Transcript recording**: `Messages()` calls pi's `get_messages` RPC command
  and converts pi's `AgentMessage` wire shape (`piMessage`/`piContentPart`,
  tolerant of both the block-array and bare-string content shapes pi may
  emit) into the package's own `Message`/`Part` type — the EXACT type
  `recorder` (`record.go`) renders to `sessions/*.md` — so a transcript
  recorded from a pi-driven turn has the identical shape a transcript from an
  equivalent opencode-driven turn sequence would have, keeping the on-repo
  stats/tag facets (which are git-derived from that shared format) harness-
  agnostic. `piSession` does not implement the optional `streamer` capability,
  so the recorder falls back to its polling path for a pi-backed session —
  same code path already exercised for any non-streaming `Session` (a test
  mock, or an opencode server predating the stream endpoint).
- **Constraint alignment**: pi is spawned as an external OS process via
  `os/exec`, so no cgo link is introduced (`CGO_ENABLED=0` unaffected); `Open`
  always passes `--no-session` so pi never writes/reads its own on-disk
  session store outside the repo — this driver's `Messages()`/recorder
  transcript is the only persisted record, exactly as for opencode.
- **Process spawning is abstracted** behind `Pi.spawn` (unexported, defaults
  to `execSpawn`) so tests substitute a fake stdin/stdout pipe pair — a
  goroutine speaking the real RPC protocol shape — with zero dependency on pi
  actually being installed.

## Testing

`internal/swarm/pi_test.go` adds a `fakePiProc` (an in-memory, protocol-
accurate stand-in for a real `pi --mode rpc` process wired via
`os.Pipe`-backed `io.Pipe`s) and:

- `TestPiOpenPromptContinue` — open, a first turn, then a SECOND ("continue")
  turn on the SAME `Session`, asserting each reply and that the fake process
  received both prompts (mirrors `harness_test.go`'s
  `TestHarnessContractOpenSessionTurnContinue`, run against `*Pi` instead of
  the interface-only fake).
- `TestPiIdleWatchdogSurfacesErrTurnIdle` — a turn the fake process accepts
  but never settles is abandoned by the idle watchdog and surfaces
  `ErrTurnIdle` (mirrors `TestHarnessContractIdleTimeoutSurfacesErrTurnIdle`).
- `TestPiMessagesTranscriptShape` — `Messages()` returns the ordered
  user/assistant history in the package's `Message`/`Part` shape across two
  turns.
- `TestPiRecorderTranscriptMatchesOpencodeShape` — the SAME `recorder` used
  for opencode, driven against a pi-backed `Session`, renders a
  `sessions/*.md` file byte-identical in shape to one rendered from an
  equivalent opencode-shaped message sequence via a `stubSession`.
- Compile-time assertions (`_ Client = (*Pi)(nil)`, `_ Session =
  (*piSession)(nil)`, `_ modelSelector = (*Pi)(nil)`, `_ Harness = (*Pi)(nil)`)
  matching `harness_test.go`'s pattern.

Verified:

```
$ CGO_ENABLED=0 go test ./internal/swarm/...
ok  	github.com/spencerharmon/beehive/internal/swarm	11.105s
```

## Not done here

Wiring an install's actual `beehive` config to choose `*Pi` over `*Opencode`
at runtime (a `harness = "pi"` config knob, or similar) is a separate
follow-up — this task delivers the driver implementing the contract, proven
against a mock process, not the config plumbing to select it in production.
