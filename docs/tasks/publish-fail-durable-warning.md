# durably record a finish-internal publish failure to the session transcript

## Problem

A Work/Review/Arbitrate pass can complete every one of its own steps correctly
(code committed+pushed, `PLAN.md` flipped, doc written) and still have its entire
hive-layer publish silently discarded, with **zero trace in the session
transcript**. `internal/audit`'s whole anomaly surface (`Aborted` / `LostRace` /
`CompletionMiss`) keys exclusively off a trailing `## ⚠️ warning` block inside the
transcript (`parse.go:scanBody`), so such a pass is byte-for-byte indistinguishable
from an honest first-try success — a future pass redoes the "already correct" work
with no signal.

### Root cause

`Runner.finish` (`internal/swarm/swarm.go`) seals the transcript **before** it
attempts the publish whose failure it returns:

    recStop(); <-recDone
    rec.snapshot(ctx)             # final flush
    if warning != "" { rec.appendWarning(warning) }
    r.streamSession(ctx, rel)     # transcript sealed HERE
    ferr := r.publishWithResolution(ctx, sess)   # a failure here is too late
    r.finalizeSession(...)
    return ferr

A failure discovered at `publishWithResolution` (e.g. conflict-retry exhaustion) can
never land in the file `finish` just sealed. Three call sites turn that failure into
an already-correct GC-for-retry outcome (`res.GCMarked=true`, claim left stale so the
work is re-driven — the no-phantom-DONE guarantee is untouched):

- `~claim-resolved-mid-turn` done path,
- the primary Work/Review/Arbitrate done path,
- the Bootstrap/Reconcile-only `mainAdvanced` no-op guard,

but the descriptive `res.Warning` they build reaches only
`cmd/honeybee/main.go`'s stderr, never the repository.

## Fix

`swarm.go` — one small shared closure `durablyWarnPublishFailure(msg)`, defined
right after `finish`, reuses finish's own primitives to record the SAME message
`res.Warning` already carries (with the literal `ferr`/failure text, not a generic
phrase) after finish returns:

    rec.appendWarning(msg)        # safe: recorder goroutine already stopped in finish
    r.streamSession(ctx, rel)     # push the amended transcript

Each of the three post-finish GC-for-retry sites calls it with `res.Warning` before
`return res, nil`. It is best-effort — an append/stream error logs to stderr but
NEVER changes the GC-for-retry / no-phantom-DONE outcome the caller returns. The
literal failure text is included so a future pass can data-mine the true trigger
distribution.

**Zero `internal/audit` change.** `scanBody`'s existing last-trailing-warning-block
logic already does the right thing once the block exists: a Work-kind transcript with
a trailing warning becomes `Aborted==true` + `CompletionMiss==true`.

## Tests

`internal/swarm/swarm_test.go::TestRunPublishFailureRecordsDurableWarning` (two
subtests, single-host no-session-worktree harness so `streamSession` is a no-op and
the transcript is written straight to the on-disk sessions dir):

- `failed-publish-durably-warns` — publish returns "publish boom"; asserts the
  pre-existing GC/no-phantom-DONE behaviour (`Completed==false`, `GCMarked==true`)
  is unchanged, the transcript now carries a trailing `## ⚠️ warning` block with the
  LITERAL "publish boom" text, and `audit.ParseFile` reports `Aborted==true`,
  `CompletionMiss==true`, non-empty `AbortReason` with no audit change. Fails
  pre-fix (transcript is header-only).
- `clean-publish-writes-no-warning` — a successful publish yields a transcript with
  no warning block and `audit` flags neither `Aborted` nor `CompletionMiss` (no
  false positive).

`CGO_ENABLED=0 go build ./... && go test ./...` green.
