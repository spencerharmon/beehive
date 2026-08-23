# extract a provider-agnostic Harness interface behind opencode

## Problem

Every LLM turn in the codebase (honeybee work/review/arbitration, the chat-diff
editor, resolution/bootstrap agents) needed to keep working unmodified once a
future pi harness driver exists alongside opencode. `pi-harness-driver` and
`harness-config-selector` are both blocked on there being ONE stable,
documented contract for "open a session, send a turn, continue a session,
report an idle-stalled turn, feed a transcript" that a second driver can
implement without either caller (`swarm.Runner`, `internal/editor`) changing.

## What was already true

`internal/swarm/swarm.go` already defined `Session` (`Prompt`/`Messages`/
`Close`) and `Client` (`Open`) before this task, and every call site already
depended on those interfaces, not the concrete `*swarm.Opencode` type:

- `swarm.Runner.Client` is typed `Client` (`swarm.go`); `cmd/honeybee/main.go`
  constructs a concrete `&swarm.Opencode{...}` only at the single wiring site
  and hands it to the runner through that field.
- `internal/editor`'s local `Client` interface (`editor.go:25-27`) is its own
  narrower alias (`NewSession(...) (swarm.Session, string, error)`) over the
  same `swarm.Session`, again satisfied by `*swarm.Opencode` only at
  construction.
- The optional capabilities (`aborter` for `Abort`, `modelSelector` for
  `SetModel`) were already separated out via interface type-assertions
  (`swarm.go`'s `r.Client.(modelSelector)`), so a driver that doesn't support
  them just doesn't get routed/aborted — additive, not required.

So the FOUNDATION extraction (opencode.go's behavior behind an interface, with
no wire-format change) was already in place. What was missing was (a) a single
documented anchor tying `Client`+`Session` together as the named "Harness"
contract pi-harness-driver/harness-config-selector can point at, and (b) a
driver-agnostic contract test proving the interface — not `*Opencode`'s
internals — is what a second implementation must satisfy.

## What changed

- `internal/swarm/swarm.go`: added `type Harness interface { Client }` as the
  documented anchor for "Client to open a session, Session to drive its
  turns" and expanded the `Session`/`Client` doc comments to spell out the
  full contract (continue = a second `Prompt` on the same `Session`; the idle
  watchdog -> `ErrTurnIdle`; provider/model/temperature/max-tokens; the
  `Messages()` feed into the `sessions/` transcript) and to point at
  `harness_test.go`'s `fakeHarnessClient`/`fakeHarnessSession` as the shape a
  future pi driver must match. No field, method signature, or behavior of
  `*Opencode`/`*ocSession` changed — this is a pure documentation/naming
  addition (`type Harness interface { Client }`), zero wire-format risk.
- `internal/swarm/harness_test.go` (new): a table-driven contract test suite
  built entirely on `fakeHarnessClient`/`fakeHarnessSession` — a from-scratch
  `Client`+`Session` implementation sharing NO code with `*Opencode` — proving
  the four contract points the ROI names:
  - `TestHarnessContractOpenSessionTurnContinue` — open a session under a
    system prompt without sending a message, send a turn, then send a SECOND
    turn on the SAME `Session` ("continue") and confirm the harness saw both
    prompts in order purely because the caller reused the `Session` value.
  - `TestHarnessContractIdleTimeoutSurfacesErrTurnIdle` — a stalled turn
    reports `errors.Is(err, ErrTurnIdle)`, the exact sentinel the runner's
    turn loop already matches on (`swarm_test.go`'s `idleClient` /
    `TestIdleTurnAbandonsForGC`), regardless of which driver produced it.
  - `TestHarnessContractTranscriptRecord` — `Messages()` returns the ordered
    user/assistant transcript (the same shape `recorder`/`renderTranscript`
    consume), and a stalled turn contributes nothing to it.
  - `TestHarnessContractModelSelectorOptional` — `SetModel` is exercised only
    via the optional `modelSelector` type-assertion, proving routing works
    against any `Client` that implements it, not specifically `*Opencode`.
  - Compile-time assertions (`var _ Client = (*Opencode)(nil)`, `_ Harness =
    (*Opencode)(nil)`, `_ Client = (*fakeHarnessClient)(nil)`, `_ Session =
    (*fakeHarnessSession)(nil)`) pin both the real driver and the fake to the
    same interfaces so neither can silently drift from the contract.

## Verification

No behavior change: `*Opencode`/`ocSession` are untouched byte-for-byte except
comments; the existing opencode-backed suite
(`TestRunCompletes`, `TestIdleTurnAbandonsForGC`, `TestIdleTurnAbortsAndRedrives`,
`TestCompletionWaitsForTurnIdle`, etc.) still passes unmodified, proving the
transcript format and turn/idle semantics are byte-for-byte identical.

```
$ CGO_ENABLED=0 go build ./...
$ CGO_ENABLED=0 go test ./internal/swarm/...
ok  	github.com/spencerharmon/beehive/internal/swarm	10.9s
```

`gofmt -l internal/swarm/*.go` reports nothing; `go vet ./internal/swarm/...`
is clean.

## Follow-on

`pi-harness-driver` implements a second `Client`+`Session` (the `Harness`
contract) against a pi harness backend, satisfying the same interfaces
`fakeHarnessClient`/`fakeHarnessSession` exercise here.
`harness-config-selector` picks which `Harness` implementation
`cmd/honeybee/main.go`/`internal/editor` wire up, from config — swapping only
the construction site, since every caller already depends on the interface.
