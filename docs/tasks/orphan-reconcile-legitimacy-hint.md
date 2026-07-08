# orphan-reconcile-legitimacy-hint: tell reviewers/arbiters the reconcile-orphan shape is sanctioned

## Problem

`internal/git`'s `PushBranchReconciled` (landed by the already-DONE
`orphan-branch-reclaim-guard`/`merged-guard-branch-gate` family, F-LIVE) is legitimate,
tested runner behavior: when a branch push collides with a dead orphan left at origin by
an earlier abandoned/redispatched attempt of the SAME task (see
`claim-ttl-wallcap-race-guard` for why these occur), the runner fetches the orphan and
folds it in via `reconcileOrphan` (a `commit-tree -p ours -p theirs` merge, `-s ours`)
under a non-honeybee identity, leaving a commit message reading:

```
beehive: reconcile dead orphan <remote>/<branch> (<sha>) into <branch> (ours; supersedes, never discards)
```

`HONEYBEE.md`'s ONLY guidance on this shape was the "NEVER force-push and NEVER reconcile
a diverged origin" bullet in `## Absolute rules` — and it is phrased exclusively from the
IMPLEMENTER's own first-person perspective ("Your `bee-<taskid>` branch may ALREADY EXIST
on origin from a prior attempt that orphaned..."). It says nothing about what a
REVIEWER or ARBITER should conclude on finding this evidence already sitting in the
history they are reading (`git log`/`git show` on the implementer branch) — which is a
different situation from the implementer's own rejected push, but recognizably the same
shape (a `reconcile dead orphan ... (ours; supersedes, never discards)` commit).

Confirmed corpus-recurring, not a one-off: the `ai-edit-publish-to-main` review
(`bee-ai-edit-publish-to-main-1783511107-3680846`, 98 turns) spent ~20 of its 98 turns
(assistant turns ~6-25) re-deriving this legitimacy purely from `git log`/`git show`
archaeology and re-reading the force-push prohibition, before it could even begin judging
the actual code diff — its own verdict cites "confirmed via identical patterns on
multiple prior DONE tasks", an implicit admission of recurrence. Independently
confirmed via `git log --all --grep="reconcile dead orphan"`: the shape recurs across
the corpus (`managed-instruction-source-edit-hint`, `ai-edit-publish-to-main`,
`instruction-drift-preflight-warning`, `publish-fail-durable-warning`,
`merged-guard-branch-gate` itself, `stats-tag-model`, and more as the swarm runs). Every
occurrence costs a review/arbitration pass real turns re-proving the same fact from
scratch, purely because the injected protocol never says it is safe to take on sight.

## Change

`prompts/HONEYBEE.md`, `## Absolute rules`, the existing "NEVER force-push and NEVER
reconcile a diverged origin" bullet (unchanged wording kept verbatim; nothing about the
implementer-side prohibition weakened) — appended a short clause covering the
reviewer/arbiter discovery case:

- Explicitly scopes the bullet: it governs YOUR OWN push, not an already-existing
  runner-authored commit you happen to encounter while reading history.
- States it applies to EVERY kind, naming review/arbitration reading history as well as
  the implementer pushing.
- Names the exact recognizable shape — a commit already sitting there reading
  `beehive: reconcile dead orphan <remote>/<branch> (<sha>) into <branch> (ours;
  supersedes, never discards)`, authored under a non-honeybee identity.
- States the conclusion directly: that shape is the runner's own already-tested
  `PushBranchReconciled` mechanism — sanctioned and expected, NOT a violation to
  investigate, re-derive via git archaeology, or flag.
- Tells the pass what to do instead: recognize the shape and proceed straight to judging
  the actual code diff.

### Why the existing bullet, not a new one

The task the bullet needs to reach (review/arbiter judging a diff that carries this
history) already lands on this exact bullet first — it is the only place `HONEYBEE.md`
talks about this shape at all, and the corpus evidence shows agents already reason
their way TO this bullet before getting stuck on its implementer-only framing. Appending
to it (rather than adding a new bullet or section) means the fix sits exactly where the
confused reasoning already converges, with no new heading to find.

### Why only a prompt edit

The lever is injection/preamble — a `prompts/HONEYBEE.md` token-cost/turn-cost fix, one of
the five named tokens-per-honeybee levers the ROI's blocking clause pulls to P1. No
runner/engine code change: `PushBranchReconciled` and `reconcileOrphan` are already
correct and unchanged (this task does not touch `internal/git`); the gap was purely that
`HONEYBEE.md` never told a reader of history to trust the mechanism it names. Considered
and deferred: a stronger runner-side annotation marker on `PushBranchReconciled` commits
— not needed yet, the commit message is already legible (names the shape, the remote/
branch/sha, and "ours; supersedes, never discards" in plain English); the gap is entirely
that `HONEYBEE.md` never said the reader could trust it.

## Tests

Prompt/doc-only change; no completion predicate or code path touched.
`## Absolute rules` is the trim-anchor section `internal/swarm/inject.go`'s
`trimProtocol` always retains verbatim for every kind (it is not in `roleSections` and
not in `dropSection`), so the appended clause reaches Work, Review, Arbitration, and
Reconcile passes alike. Verified nothing regressed: `go build ./...` and
`go vet`/`go test ./internal/swarm/... ./prompts/...` green, including
`TestTrimProtocolKeepsOwnKindStepDropsOthers` (asserts `## Absolute rules` — and so this
clause — survives trimming for every kind).

## Acceptance mapping

- *force-push/reconcile guidance explicitly covers the reviewer/arbiter discovery case,
  not just the implementer's own rejected-push case* → the appended clause names
  "Every kind — a review or arbitration reading history, not only the implementer
  pushing".
- *names the `PushBranchReconciled`/"ours; supersedes, never discards" shape as
  sanctioned* → the clause quotes the exact commit-message shape and states it is "the
  runner's own already-tested `PushBranchReconciled` mechanism, sanctioned and expected".
- *no completion predicate or the existing implementer-side prohibition is weakened* →
  the original bullet's implementer-facing sentences are unchanged verbatim; the new
  text is appended after them, and the new clause opens by scoping itself ("This is about
  YOUR OWN push") so it narrows only the reviewer/arbiter-facing gap, not the
  implementer's rules.
- *a future audit pass re-sampling a review/arbitration that encounters this pattern
  confirms it proceeds to the actual code judgment without a multi-turn legitimacy
  investigation* → behavioral: with the clause in `## Absolute rules` (kept for every
  kind), a review/arbitration pass that meets a `reconcile dead orphan ... (ours;
  supersedes, never discards)` commit in history now has the "sanctioned, proceed to the
  diff" instruction already in its injected protocol instead of having to re-derive it.
