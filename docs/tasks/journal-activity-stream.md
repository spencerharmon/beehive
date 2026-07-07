# journal-activity-stream — always-on concise pass activity to the journal

## Problem

A scheduled honeybee pass was INVISIBLE in the journal. The recorder teed agent
activity to stderr only when `rc.debug != nil`, and the runner gated its own
progress lines behind `r.Debug != nil` — both set ONLY by the honeybee `--debug`
flag. The scheduler runs `systemd-run … honeybee <repo>` with NO `--debug`, so a
pass emitted only the runner's end-of-run warning lines and NOTHING about what the
agent was doing: no pass kind, no tool calls, no turn boundaries. Combined with a
stub-only transcript (`session-transcript-finalize`), a stalled pass was doubly
invisible — exactly how a 100%-stall task ran unseen for days.

## Fix — split the recorder's always-on concise sink from the debug-only verbose sink

Two DISJOINT live-activity sinks, so `--debug` is a clean superset of the concise
stream with no line doubled (in production both are the same `os.Stderr`).

### Recorder (`internal/swarm/record.go`)

`recorder` gains `concise io.Writer` alongside `debug io.Writer`. `streamDebug` is
renamed `streamActivity` and the `rc.debug`-gate is split:

- **concise (always-on, via new `rc.activity`)**: tool-call NAME lines — the
  pending/running `· <tool> <input>`, completed `✓ <tool>`, error `✗ <tool>: …`
  markers — plus the stream-health notices in `loop` (event-stream unavailable /
  fell back to polling). `activity` prefers `rc.concise`, falling back to
  `rc.debug` so a verbose-only test still sees them.
- **debug (--debug only, direct to `rc.debug`)**: the verbose extras — user-prompt
  markers, assistant-text + model-reasoning deltas, and full tool OUTPUT bodies.

`render` now calls `streamActivity` unconditionally (it self-gates); with neither
sink set (a plain unit test) `streamActivity` early-returns, so the durable
transcript path is untouched. The transcript file itself (`renderTranscript` +
`os.WriteFile`) is not touched by either sink — it is byte-identical regardless of
streaming.

### Runner (`internal/swarm/swarm.go`)

`Runner` gains `Concise io.Writer` (always-on) beside `Debug` (--debug). New
`logConcise` writes one always-on line (no Debug tee — disjoint, no doubling):

- the pass **kind** at session open (`[honeybee] dir=… submodule=… kind=… opening
  session…`), moved off the `Debug` gate;
- each **turn boundary** (`[honeybee] ── turn N/max ──`) at the top of the turn
  loop — a live "still working / stalled on turn N" heartbeat;
- the **abandon/GC reason** in `finish` (`⚠️  <warning>`), moved off the `Debug`
  gate so a killed pass explains itself live.

The recorder is constructed with `concise: r.Concise`. `finalizeSession` /
`reclaimSourceBranch` / the durable session-branch stream are untouched.

### Wiring (`cmd/honeybee/main.go`)

`runner.Concise = os.Stderr` UNCONDITIONALLY. `runner.Debug`/`oc.Debug` stay behind
`if debug`. So every scheduled pass streams concise activity to the journal; only
`--debug` adds the verbose full-transcript tee.

## Tests (`internal/swarm/*_test.go`)

- `TestRecorderConciseStreamsWithoutDebug` — binding: a recorder with ONLY the
  concise sink streams tool-call names and stays concise (no output body /
  reasoning / text / user marker). Verified to FAIL before the split (a no-debug
  recorder emitted nothing).
- `TestRecorderDebugSupersetsConcise` — the verbose tee and the concise stream are
  disjoint and their union is the full transcript (superset, no line doubled).
- `TestRecorderTranscriptByteIdenticalAcrossSinks` — the on-disk transcript is
  byte-identical with no sink / concise / debug attached.
- `TestRecorderNoSinksNoStream` — a sink-less recorder streams nothing.
- `TestRunnerConciseActivityAlwaysOn` — a no-`Debug` pass streams the kind, turn
  boundaries, and abandon/GC reason to `Concise`.

`go build ./...`, `go vet ./...`, `go test ./...` green under `CGO_ENABLED=0`.
