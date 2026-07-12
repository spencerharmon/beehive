# review-doc-only-branch-hint

## Problem
Recent-50-session tool-call-failure mining shows `missing-git-ref` as the dominant
failure category, concentrated in REVIEW sessions of doc-only audit-series tasks.
Root cause: the Review (and Arbitrate) preamble in `internal/swarm/swarm.go`
unconditionally tells every reviewer the implementer's work is on branch
`bee-<taskid>` and to fetch/inspect it read-only — even for a task whose `Files:`
line touches ONLY beehive-layer text (`docs/`, `PLAN.md`, `ROI.md`, `docs/tasks/`),
which never has a `bee-<taskid>` code branch. The reviewer runs the
fetch/rev-parse, eats a "couldn't find remote ref" failure, then falls back to
reading the change doc directly — burning turns on a probe that could never succeed.

## Change
Reuse `filesFromCard` (already used by the Work brief) to classify a task as
doc-only in the Review and Arbitrate preamble builders:

- `isDocOnlyCard(body)` — true iff the card's `Files:` line parses to ≥1 path and
  EVERY parsed path is beehive-layer (`isBeehiveLayerPath`: `PLAN.md`, `ROI.md`, or
  under `docs/`, after stripping any `submodules/<sm>/` prefix). Conservative: an
  empty/unparseable `Files:` line, or any mixed line carrying a code path, is
  treated as code-bearing (false).
- When doc-only, the Review/Arbitrate preamble replaces the fetch/inspect sentence
  with a stated fact: "This is a DOC-ONLY task: no bee-<taskid> code branch exists
  (nothing to fetch/inspect) — judge the change doc ... and PLAN.md state directly."
- Code-bearing (and no-Files) tasks keep the existing fetch/inspect sentence
  byte-identical.

## Files
- `internal/swarm/swarm.go` — Review + Arbitrate preamble builders now branch on
  `isDocOnlyCard`.
- `internal/swarm/brief.go` — new `isDocOnlyCard` / `isBeehiveLayerPath` helpers.
- `internal/swarm/swarm_test.go` — `TestIsDocOnlyCard` (classifier table) and
  `TestReviewPreambleDocOnlyBranchHint` (doc-only vs code-bearing preamble text for
  both Review and Arbitrate).

## Verify
`CGO_ENABLED=0 go test ./...` green.
