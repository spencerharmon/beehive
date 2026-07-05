# audit: stub visibility — classify unfinalized session stubs, census the corpus

## Problem

`beehive audit` mines finalized honeybee transcripts under
`submodules/<sm>/sessions/`. Each running session first plants a **stub** on main
(`internal/repo.SessionStub`, written by `internal/swarm.startSession`): a one-line
`<!-- beehive-session-branch: <branch> -->` placeholder while the transcript
streams to an isolated branch; the durable transcript lands only when the session
finalizes and merges.

`internal/audit.ParseDir` treated every `*.md` as a transcript. A stub has no
`submodule · kind · branch` header, so `parseHeader` returned
`no submodule/kind/branch header line` and `ParseDir` folded the stub into its
joined per-file error. With 729 sessions unfinalized the engine dumped 729 such
errors to stderr and returned an **empty window** — byte-for-byte identical to a
genuinely rested, fully-audited swarm. That collision is how a 96%-corpus-loss
defect ran unnoticed from just after pass 1 until pass 2 read the files directly:
an empty window read as "nothing to audit" when it actually meant "almost nothing
is finalized."

A stub is a **known shape**, not corrupt noise — `repo.ParseSessionStub` already
recognises it (beehived uses the same recogniser to resolve a live stream).

## Fix (reporting only — does NOT finalize anything)

Finalizing stubs is `session-transcript-finalize`'s job. This task only makes the
corpus state self-announcing.

### Classify (`internal/audit/parse.go`)

New `ParseDirCensus(dir) (Census, error)` reads each `*.md` and, **before** the
transcript parse, tries `repo.ParseSessionStub`:

- recognised → an unfinalized **`Stub{SID, Branch}`** (SID = file stem, Branch =
  the live branch it streams to). Stub-sorted for determinism.
- else parse as a transcript → a finalized, mineable `Session` (epoch-sorted,
  reconcile-loop annotated) or, on failure, a genuine **error**.

Only files that are neither a valid transcript nor a recognised stub become
errors. `ParseDir` is now a thin wrapper over `ParseDirCensus`, preserving its
exact `(sessions, errors.Join(malformed…))` contract — stubs are neither sessions
nor errors, so existing callers/tests are unaffected. `audit` gaining an import of
`repo` is cycle-free (`repo` imports neither `audit` nor anything that does).

### Census + warning (`internal/audit/aggregate.go`)

`Census{Sessions, Stubs, Errors}` with `Total = Finalized + Stubs + Errors` and
`MineableFraction = Finalized / Total` (an empty dir is defined 1.0 — nothing is
broken, there is simply nothing there, so a truly empty sessions dir never warns).

`CorpusBroken(windowEmpty)` is the "unfinalized, not rested" predicate: **stubs
exist** AND (`windowEmpty` OR `MineableFraction < LowMineableFraction` = 0.5). A
fully-finalized corpus (zero stubs) is never broken, so a healthy rested swarm —
window empty only because everything was audited — stays silent. This is the whole
point: it distinguishes empty-because-audited (rest) from empty-because-unfinalized
(defect). The 0.5 threshold keeps a handful of live streams against a large
finalized corpus quiet while catching a majority-stub corpus even before the
window hits exactly zero.

`CorpusWarning(windowEmpty)` returns the loud `CORPUS BROKEN, NOT A REST` banner
(counts, fraction, threshold, and — when empty — the explicit "EMPTY because
unfinalized, NOT because rested" line) or `""` for a healthy corpus. It is
**byte-stable**: a healthy pass emits exactly zero bytes.

### Surface it (`cmd/beehive/cmd_audit.go`)

`beehive audit` now runs `ParseDirCensus`, prints a `# corpus census` TSV section
(`total / finalized / stub / malformed / mineable_fraction`, plus a
`sid \t branch` list when stubs exist) alongside the existing window/aggregate/
trend, and writes `CorpusWarning` to **stderr** (empty ⇒ nothing printed). Genuine
malformed files are still surfaced on stderr; a hard error is returned only when
nothing is usable **and** nothing is even a stub (no sessions AND no stubs) — the
729-stub defect now reports a census + loud warning instead of exiting on a wall of
errors. Audit stays read-only apart from its existing `--write` ledger append (no
new files, no ledger schema change).

## Tests

`internal/audit/census_test.go`:

- `TestParseDirCensusClassifies` — the binding acceptance: a dir mixing finalized
  transcripts + `repo.SessionStub` files (stem deliberately ≠ branch) + one
  malformed file yields finalized 2 / stubs 2 (with sids+branches) / errors 1;
  stubs are NOT in the error text; `ParseDir` over the same dir keeps its
  2-sessions + malformed-only-error contract.
- `TestParseDirCensusStubFixture` — a committed real stub
  (`testdata/stubs/…`) classifies as one stub, zero finalized, zero errors.
- `TestCorpusFinalizedNotStubbed` — the real finalized corpus is all mineable
  (fraction 1.0, never broken): no false positives.
- `TestCensusMineableMath` — census arithmetic incl. the empty-dir edge and the
  30/759 ≈ 0.04 defect ratio.
- `TestCorpusBroken` / `TestCorpusFractionThreshold` — the four regimes and the
  0.5 boundary (at-threshold silent, below fires).
- `TestCorpusWarningByteStable` — healthy ⇒ zero bytes; defect ⇒ loud banner
  naming the counts and the empty-window cause.

`go test ./...` green under `CGO_ENABLED=0`.
