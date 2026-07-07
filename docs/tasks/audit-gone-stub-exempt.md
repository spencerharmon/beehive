# audit: gone-stub exemption — permanently gone stubs never trip the corpus-broken alarm

## Problem

`Census.CorpusBroken` (`internal/audit/aggregate.go`) trips its "broken, not rested" alarm
whenever `len(Stubs) > 0` and either the audit window is empty or the mineable fraction is
below `LowMineableFraction`. `session-transcript-finalize`'s non-fatal sweep
(`internal/swarm/sweep.go`, wired into every `beehived` restart) has run across four
consecutive audit passes (003→004→005→006) without recovering any of the 99 legacy
unfinalized stubs: their stream branches are `GoneBranch` per `sweep.go`'s own
classification — confirmed to resolve nowhere (neither `refs/heads/<branch>` nor
`refs/remotes/<remote>/<branch>`) — so they are permanently un-finalizable, not a live or
growing defect.

Because `CorpusBroken` could not tell a permanently-gone stub from a live/pending one, the
next genuinely empty (rested) audit window would still trip the loud "CORPUS BROKEN, NOT A
REST" banner purely because these 99 dead files exist, misdirecting the reader to
`session-transcript-finalize` for a defect that isn't there — the mirror image of what
`audit-stub-visibility`/`audit-malformed-visibility` guard against: a permanently healthy
rest could never again go silent while these relics exist.

## Fix (reporting only — does NOT prune or finalize anything)

### Classify (`internal/audit/parse.go`)

New `BranchResolver func(branch string) (ref string)` and `ClassifyStubs(c *Census, resolve
BranchResolver)`, which sets each stub's `GoneBranch` to `resolve(branch) == ""`.
`ClassifyStubs` is a small post-processing step over an already-parsed `Census` — it takes no
git dependency itself, so `internal/audit` stays dependency-light and unit-testable with a
plain func literal; the caller (`cmd/beehive/cmd_audit.go`) supplies the real git-backed
resolution. A `nil` resolver is a safe no-op (every stub's `GoneBranch` stays `false`), so
`ParseDirCensus`'s existing signature — and every caller/test that never classifies — is
completely unaffected.

### Exempt (`internal/audit/aggregate.go`)

New `Stub.GoneBranch bool` field and `Census.NonGoneStubCount()` (stub count excluding
`GoneBranch`). `CorpusBroken` now gates on `NonGoneStubCount() == 0` (instead of
`len(Stubs) == 0`) and computes its low-fraction check as
`Finalized()/(Finalized()+NonGoneStubCount())` instead of the whole-corpus
`MineableFraction()`. Both branches (`windowEmpty` and the fraction) are therefore blind to
permanently-gone stubs: a corpus that is ALL gone stubs (plus zero errors) is never broken
regardless of `windowEmpty`, while a single NEW/still-resolvable stub, or a fraction that is
still low after excluding gone stubs, continues to trip the alarm exactly as before.
`Census.Stubs` keeps listing every stub (gone or not) and `Total()`/`MineableFraction()` keep
their whole-corpus denominators — nothing is hidden, pruned, or deleted; only the BROKEN
classification changes.

### Wire it (`cmd/beehive/cmd_audit.go`)

`resolveSessionBranch` mirrors `internal/swarm/sweep.go`'s private `resolveRef` closure
exactly: `refs/heads/<branch>` first, then (if a remote is configured)
`refs/remotes/<remote>/<branch>`, against the coordination repo at `root` — never a target's
`repo/` checkout — so the finalize sweep and the audit census can never drift on what counts
as gone. `beehive audit` calls `audit.ClassifyStubs` right after parsing the census (skipped
entirely when there are no stubs, so no git subprocess runs on a clean corpus), and
`printCensus`'s stub listing gains a third `gone` column so a human skimming the TSV can see
which stubs are permanently dead.

## Tests (`internal/audit/census_test.go`)

- `TestCorpusBrokenGoneStubsExempt` — the binding acceptance: all-gone stubs + empty window +
  zero errors yields `CorpusBroken(true) == false` and `CorpusWarning(true) == ""`
  (regression: today `true`, prints the banner); one NEW live stub alongside the 99 gone ones
  with an empty window still flags (no regression on the guard's original purpose); a mixed
  gone+live corpus whose non-gone-only fraction is below threshold still flags with a
  non-empty window; the symmetric at-threshold case stays silent.
- `TestClassifyStubs` / `TestClassifyStubsNilResolver` — the resolver correctly distinguishes
  `refs/heads`, `refs/remotes/<remote>`, and neither (mirroring `sweep.go`'s resolution
  order), and a `nil` resolver is a safe no-op.
- `TestCorpusBroken`, `TestCorpusFractionThreshold`, `TestCorpusWarningByteStable` — unmodified
  and still green: nothing is classified gone in these fixtures, so
  `NonGoneStubCount() == StubCount()` and behavior is byte-for-byte identical to before this
  task.

`go test ./internal/audit/...` and the full `go test ./...` green under `CGO_ENABLED=0`.

## Distinguishes

- all stubs gone + empty window + zero errors → silent, byte-for-byte (a genuine rest).
- any non-gone (live) stub + empty window → broken (unchanged original purpose).
- mixed gone+live, non-gone-only fraction below threshold → broken even with a non-empty window.
- mixed gone+live, non-gone-only fraction at/above threshold → silent.
- errors>0 → always broken regardless of gone/live stub mix (unchanged from `audit-malformed-visibility`).

## Caveats

- Reporting/classification only, exactly like the sibling `audit-stub-visibility`/
  `audit-malformed-visibility` guards: nothing here finalizes, prunes, or deletes a stub file.
  Actually removing the dead placeholders (if ever desired) is a separate, deliberately
  out-of-scope, higher-risk decision.
- Classification is by actual ref resolution (mirroring `sweep.go`'s exact order), never by an
  age/epoch heuristic — a brand-new stub whose branch happens to be reclaimed unusually fast is
  still correctly caught as gone, and nothing is ever misclassified by age alone.
- `resolveSessionBranch`'s resolution order deliberately duplicates `sweep.go`'s `resolveRef`
  rather than importing `internal/swarm` from `cmd/beehive`: if `sweep.go`'s resolution logic
  ever changes, update both call sites together (a shared exported helper is a future
  extraction, not done here).
