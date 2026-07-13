# review-doc-only-branch-hint

## Problem
The Review and Arbitrate preamble builders in `internal/swarm/swarm.go` unconditionally
tell every reviewer/arbiter that "Implementer's work is on branch bee-<taskid> ...
inspect read-only via git (fetch from origin if the branch is absent locally)". For a
DOC-ONLY task — one whose `Files:` line touches only beehive-layer paths (`docs/`,
`docs/tasks/`, `PLAN.md`, `ROI.md`) — no `bee-<taskid>` submodule CODE branch ever
exists (HONEYBEE.md's Review section documents this as EXPECTED). The reviewer runs the
fetch/rev-parse anyway and eats a `couldn't find remote ref` failure before falling back
to reading the change doc. Tool-call-failure mining showed `missing-git-ref` as the
dominant failure category (57.7% of recent fails), concentrated in these doc-only review
sessions. This is wasted turns against ROI Priority 1 (cut tokens per honeybee).

## Change
- `internal/swarm/brief.go`: add `isDocOnlyCard(body []string) bool`, which reuses the
  existing `filesFromCard` parser and classifies a task as doc-only when it has at least
  one parsed path and EVERY parsed path is a beehive-layer path (`isBeehiveLayerPath`:
  the `docs/` tree, or a `PLAN.md`/`ROI.md` basename). It is conservative: a card with no
  parseable `Files:` path, or a mixed line naming any code path, returns false
  (code-bearing).
- `internal/swarm/swarm.go`: the Review and Arbitrate preamble builders now branch on
  `isDocOnlyCard(sel.Task.Body)`. When doc-only, the fetch/inspect sentence is replaced by
  a stated fact: "This is a DOC-ONLY task: no bee-<taskid> code branch exists (nothing to
  fetch/inspect) — judge the change doc and PLAN.md state directly." A code-bearing (or
  mixed) card renders the historical fetch/inspect sentence byte-identical to before.

## Why additive
Only the branch-hint sentence changes, and only for doc-only cards. A code-bearing
review/arbitrate preamble is unchanged. The classifier defaults to code-bearing when in
doubt, so the risky direction (telling a code reviewer there is no branch) never fires
from an ambiguous card.

## Tests
`internal/swarm/swarm_test.go`:
- `TestReviewPreambleDocOnlyBranchHint` / `TestArbitratePreambleDocOnlyBranchHint`:
  doc-only card renders the DOC-ONLY sentence and omits the fetch/inspect instruction.
- `TestReviewPreambleCodeBearingUnchanged`: a code path, and a mixed code+docs line, both
  keep the fetch/inspect sentence and never render the doc-only hint.
- `TestIsDocOnlyCard`: unit table for the classifier including the conservative
  no-Files-line / nil cases.

`go test ./...` green under `CGO_ENABLED=0`.
